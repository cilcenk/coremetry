package copilot

import (
	"context"
	"errors"
	"log"
	"strings"

	"github.com/cilcenk/coremetry/internal/ai/provider"
)

// provider_calls.go — buffered explain çağrılarının TEK giriş noktası
// (docs/plans/ai-assistant-design-2026-08-16.md §2.1 / §4-Faz1).
//
// FAZ 1.1 burada tek yüzeylik bir canary tutuyordu; FAZ 1.2'de canary
// koşulu ve üç eski üretici (explainOpenAI/Anthropic/GitHubWithUsage,
// ~230 satır) birlikte silindi. Artık her sağlayıcının buffered explain
// yolu internal/ai/provider'dan geçiyor ve gövde şeklinin TEK yazılışı
// var — v0.9.1120'nin 1024 paritesi ~1000 sürüm yaşadı çünkü aynı sabit
// üç ayrı üreticide yazılıydı.
//
// GÖREV AYRIMI (bu dosyanın varlık sebebi):
//
//	provider  → tel üstündeki şekil. Durum tutmaz. Config bir SNAPSHOT.
//	Service   → DURUM. Kilit, 30s config refresh, TLS-skip, timeout,
//	            GitHub oturum jetonu, JSON yetenek kararları, merdivenin
//	            hangi basamağı deneyeceği, kota kesicisi, ai_calls kaydı.
//
// Bu dosya ikisinin arasındaki ince katman: snapshot al, çağır, kararı
// ver.

// callSnapshot — TEK RLock altında alınan çağrı yapılandırması.
//
// Neden tek kilit: httpClient() / tuneMaxTokens() / tuneTemperature()
// ayrı ayrı çağrılsaydı üç ayrı RLock olurdu ve aralarına giren bir
// Configure (30s config refresh ya da Settings PUT) yarı-eski yarı-yeni
// bir çağrı üretebilirdi. Locked yardımcılar tam bunun için var.
//
// prov/base/model ayrıca ÇIPLAK döner: JSON yetenek kararları
// (provider,baseURL,model) anahtarıyla önbellekli ve anahtarın
// VARSAYILANLARI UYGULANMIŞ hâli kullanılmalı — aynı uç, bir çağrıda
// boş model bir çağrıda "gpt-4o-mini" olarak anahtarlanırsa karar
// kaybolur.
func (s *Service) callSnapshot(ctx context.Context) (cfg provider.Config, req provider.Request, prov, base, model string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	// v0.10.175 — çağrının profili (WithProfile > yüzey haritası > varsayılan);
	// profil tuning'i küreseli ezer (0/nil = küresel).
	rt := s.resolveProfileLocked(ctx)
	p := rt.cfg
	prov, base, model = p.Provider, p.BaseURL, p.Model
	cfg = provider.Config{
		BaseURL:    base,
		APIKey:     p.APIKey,
		Model:      model,
		HTTPClient: s.clientForLocked(rt),
	}
	maxTok := s.tuneMaxTokensLocked()
	if p.MaxTokens > 0 {
		maxTok = p.MaxTokens
	}
	req = provider.Request{
		Model:     model,
		MaxTokens: maxTok,
		System:    "", // çağıran dolduruyor
		User:      "",
	}
	if p.Temperature != nil {
		temp := *p.Temperature
		req.Temperature = &temp
	} else if t, ok := s.tuneTemperatureLocked(); ok {
		temp := t
		req.Temperature = &temp
	}
	return cfg, req, prov, base, model
}

// ── OpenAI-compat ───────────────────────────────────────────────────
//
// baseURL /v1 önekini İÇERİR (örn. http://ollama:11434/v1); transport
// üstüne /chat/completions ekler. Anahtar boşsa header hiç gönderilmez
// ki auth aramayan yerel uçlar (Ollama varsayılanı) çalışsın, arayan
// geçitler de temiz 401 dönebilsin.

// explainOpenAI — buffered openai-compat explain + JSON merdiveni.
//
// Merdivenin İKİ yarısı var ve bilinçli olarak ayrı yerlerde duruyor:
//
//	Service (burada) → hangi basamak DENENİR, reddedilince ne olur,
//	                   karar önbelleğe yazılır mı. Hepsi durum.
//	provider         → seçilen basamağın gövde şekli. Saf.
func (s *Service) explainOpenAI(ctx context.Context, systemPrompt, userPrompt string) (string, uint32, uint32, error) {
	cfg, req, prov, base, model := s.callSnapshot(ctx)
	req.System, req.User = systemPrompt, userPrompt
	// Varsayılanlar yetenek-anahtarı için burada uygulanır (transport
	// aynısını kendi içinde de yapıyor; anahtar ile gövde ayrışmasın).
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	if model == "" {
		model = "gpt-4o-mini"
	}

	// İstenen basamak, o uç için ÖNCEDEN reddedilmiş basamaklara göre
	// aşağı çekilir; hiç yoklanmamışsa istenen basamakla denenir ve
	// sonuç aşağıda öğrenilir.
	lvl := jsonLevelRequested(ctx)
	spec, hasSpec := jsonSchemaFrom(ctx)
	if lvl >= jsonSchema && (!hasSpec || s.jsonSchemaBlocked(prov, base, model)) {
		lvl = jsonObject
	}
	if lvl >= jsonObject && s.jsonModeBlocked(prov, base, model) {
		lvl = jsonNone
	}
	// v0.9.1261 (CoSRE denetimi [3/S]) — KATI-JSON çağrıları sıcaklık 0'da
	// koşar: yapısal çıktıda yaratıcılık değil determinizm istenir; 0.2'de
	// aynı girdiye farklı şekiller/alan sıraları üreyebiliyordu. Kural
	// SEVİYE İSTENDİĞİNDE uygulanır (merdiven kısıtsıza düşse bile) —
	// çağıranın niyeti yapısal çıktıysa determinizm gerekçesi aynen
	// geçerli, servisin json desteğinden bağımsız. Operatörün prose
	// tuning'i (Settings temperature) yapısal yüzeyleri ETKİLEMEZ; prose
	// yüzeyleri eskisi gibi tuned/varsayılan sıcaklıkta.
	if jsonLevelRequested(ctx) > jsonNone {
		zero := 0.0
		req.Temperature = &zero
	}
	return s.explainOpenAIAtLevel(ctx, cfg, req, prov, base, model, lvl, spec)
}

// explainOpenAIAtLevel — merdivenin bir basamağı. Reddedilirse BİR ALT
// basamakla kendini çağırır: json_schema → json_object → kısıtsız.
//
// v0.9.526 — karar yalnız "bu parametreyi anlamadım" ailesinde (400/
// 422/501) ve yalnız alt basamak GERÇEKTEN başarırsa yazılır. Kanıta
// dayalı: bağlamı taşan bir 400 iki yolda da patlar, o yüzden karar
// yazılmaz; response_format'ı reddeden bir 400 alt basamakta geçer,
// karar yazılır. v0.9.517 HERHANGİ bir ≥300'ü yetenek kararı sayıyordu
// ve geçici bir 503 JSON modunu süreç ömrü boyunca kapatıyordu.
func (s *Service) explainOpenAIAtLevel(ctx context.Context, cfg provider.Config, req provider.Request,
	prov, base, model string, lvl jsonLevel, spec jsonSchemaSpec) (string, uint32, uint32, error) {

	req.JSONLevel = int(lvl)
	if lvl == jsonSchema {
		req.JSONSchemaName, req.JSONSchema = spec.Name, spec.Schema
	}
	resp, err := provider.DoOpenAI(ctx, cfg, req)
	if err != nil {
		var he *provider.HTTPError
		if lvl > jsonNone && errors.As(err, &he) && jsonModeVerdictStatus(he.Status) {
			out, pt, ct, rerr := s.explainOpenAIAtLevel(ctx, cfg, req, prov, base, model, lvl-1, spec)
			if rerr != nil {
				// Alt basamak da patladı → hata bu basamakla ilgili
				// değildi. Yeteneği kapatma; çağıranın gerçekten yaptığı
				// isteğin hatasını döndür.
				log.Printf("[copilot] %d hatası %s basamağından bağımsız (alt basamak da başarısız) — yetenek açık bırakıldı", he.Status, lvl)
				return "", 0, 0, err
			}
			s.markLevelUnsupported(lvl, prov, base, model)
			log.Printf("[copilot] response_format %s reddedildi (%d) — bu uç için kapatıldı, %s ile yanıt alındı", lvl, he.Status, lvl-1)
			return out, pt, ct, nil
		}
		// Çözümleme hatasında token sayıları DOLU gelir (başarısız çağrı
		// da faturalıdır ve /ai satırı maliyeti göstermeli); taşıma/HTTP
		// hatasında sıfırdır.
		return "", clampTokens(resp.InputTokens), clampTokens(resp.OutputTokens), err
	}
	return resp.Text, clampTokens(resp.InputTokens), clampTokens(resp.OutputTokens), nil
}

// ── Anthropic ───────────────────────────────────────────────────────

// explainAnthropic — buffered Messages explain.
//
// JSON basamağı BİLEREK taşınmıyor: bu yola bugüne kadar hiç
// response_format gönderilmedi (Anthropic'in yazılışı farklı) ve bu
// dilim davranış taşıyor, değiştirmiyor. Bir yüzey WithJSONMode ile
// gelirse anthropic'te bugün olduğu gibi kısıtsız çağrı yapılır.
func (s *Service) explainAnthropic(ctx context.Context, systemPrompt, userPrompt string) (string, uint32, uint32, error) {
	cfg, req, prov, base, model := s.callSnapshot(ctx)
	req.System, req.User = systemPrompt, userPrompt
	req.JSONLevel = provider.JSONPlain
	// v0.10.253 (prompt audit D2) — şema istenen ve o uç için reddedilmemişse
	// structured outputs (output_config.format). Sunucu 400 (unsupported)
	// derse basamak o uç için işaretlenir ve düz çağrıyla yeniden denenir
	// (openai merdiveninin aynı öğrenmesi). JSONObject seviyesi bu API'de
	// yok → düz. Sıcaklık bu yolda gönderilmez (D1); determinizm effort'la.
	if spec, ok := jsonSchemaFrom(ctx); ok && jsonLevelRequested(ctx) >= jsonSchema && !s.jsonSchemaBlocked(prov, base, model) {
		req.JSONLevel = provider.JSONSchema
		req.JSONSchemaName, req.JSONSchema = spec.Name, spec.Schema
	}
	resp, err := provider.DoAnthropic(ctx, cfg, req)
	if err != nil && req.JSONLevel == provider.JSONSchema && anthropicRejectsSchema(err) {
		s.markJSONSchemaUnsupported(prov, base, model)
		req.JSONLevel, req.JSONSchema, req.JSONSchemaName = provider.JSONPlain, nil, ""
		resp, err = provider.DoAnthropic(ctx, cfg, req)
	}
	return resp.Text, clampTokens(resp.InputTokens), clampTokens(resp.OutputTokens), err
}

// anthropicRejectsSchema — 400 + gövdede output_config/json_schema: model ya da
// hesap structured outputs'u desteklemiyor; düz çağrıya düş. SAF.
func anthropicRejectsSchema(err error) bool {
	var he *provider.HTTPError
	if !errors.As(err, &he) || he.Status != 400 {
		return false
	}
	b := strings.ToLower(he.Body)
	return strings.Contains(b, "output_config") || strings.Contains(b, "json_schema") || strings.Contains(b, "structured")
}

// ── GitHub Copilot ──────────────────────────────────────────────────

// explainGitHub — buffered Copilot explain.
//
// İki adım: (1) OAuth jetonunu (ghu_…) kısa ömürlü oturum jetonuyla
// takas et — DURUM (önbellek + son kullanma), o yüzden Service'te kalır;
// (2) çözülmüş jetonla transport'u çağır. Config.APIKey burada OAuth
// jetonu değil OTURUM jetonudur.
//
// Sıra korunuyor: takas ÖNCE, snapshot SONRA. Takas kendi kilidini
// alıyor ve jeton yazıyor; snapshot'ı önce almak, aynı çağrıda bayat
// bir istemciyle yeni bir jetonu eşleştirme riskini doğururdu.
func (s *Service) explainGitHub(ctx context.Context, systemPrompt, userPrompt string) (string, uint32, uint32, error) {
	sessTok, err := s.githubSessionToken(ctx)
	if err != nil {
		return "", 0, 0, err
	}
	cfg, req, _, _, _ := s.callSnapshot(ctx)
	cfg.APIKey = sessTok
	req.System, req.User = systemPrompt, userPrompt
	req.JSONLevel = provider.JSONPlain
	resp, err := provider.DoGitHub(ctx, cfg, req)
	return resp.Text, clampTokens(resp.InputTokens), clampTokens(resp.OutputTokens), err
}

// ── Buffered yanıt çözümleyicileri (streaming yolunun geri düşüşü) ───
//
// stream.go, bir vekil stream:true bayrağını yutup tek-atış JSON
// döndürdüğünde elindeki gövdeyi çözümlüyor (v0.8.404 — ikinci faturalı
// çağrıdan iyi). Çözümleyicinin KENDİSİ artık provider'da; bunlar
// yalnız uint32 sınırına çeviren adaptörler.

func parseBufferedOpenAI(respBody []byte) (string, uint32, uint32, error) {
	resp, err := provider.ParseOpenAIChat(respBody)
	return resp.Text, clampTokens(resp.InputTokens), clampTokens(resp.OutputTokens), err
}

func parseBufferedAnthropic(respBody []byte) (string, uint32, uint32, error) {
	resp, err := provider.ParseAnthropic(respBody)
	return resp.Text, clampTokens(resp.InputTokens), clampTokens(resp.OutputTokens), err
}

// clampTokens — provider int taşır (JSON'da sunucu neyi yollarsa),
// ai_calls uint32. Negatif/absürt bir usage değeri sarmalanıp devasa
// bir token sayısına dönmesin diye kelepçelenir.
func clampTokens(n int) uint32 {
	if n < 0 {
		return 0
	}
	const maxU32 = 1<<32 - 1
	if n > maxU32 {
		return maxU32
	}
	return uint32(n)
}
