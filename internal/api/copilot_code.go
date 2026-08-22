package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/cilcenk/coremetry/internal/devops"
	"github.com/cilcenk/coremetry/internal/stackparse"
)

// copilot_code.go — "Kodu da incele" (v0.9.831).
//
// Explain uçlarına opsiyonel `{"includeCode": true}` gövdesi geldiğinde
// bu dosya devreye girer: stacktrace → frame'ler → servis→depo çözümü →
// kaynak penceresi → prompt'a KOD BAĞLAMI bloğu. Sonra çağrı, kod
// gövdesini ai_calls kaydından MASKELEYEN sarmalayıcıdan geçer.
//
// Varsayılan KAPALI ve geriye uyumlu: gövdesiz POST (bugünkü tüm
// çağrılar) bayt-bayt eski prompt'u ve eski davranışı alır.

// explainOptions — Explain uçlarının opsiyonel gövdesi.
type explainOptions struct {
	IncludeCode bool `json:"includeCode"`
}

// decodeExplainOptions — gövdeyi okur. BOŞ GÖVDE GEÇERLİDİR: bu uçlar
// v0.9.408'den beri gövdesiz POST alıyor ve her mevcut çağıran öyle
// çağırıyor. Bozuk JSON de sessizce varsayılana düşer — bir açıklama
// isteğini gövde ayrıştırma hatasıyla 400'lemek, opsiyonel bir
// zenginleştirme uğruna çalışan bir yüzeyi kırmak olurdu.
func decodeExplainOptions(r *http.Request) explainOptions {
	var o explainOptions
	if r == nil || r.Body == nil {
		return o
	}
	// 4KB yeter: gövdede tek bir bayrak var.
	b, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil || len(strings.TrimSpace(string(b))) == 0 {
		return o
	}
	_ = json.Unmarshal(b, &o)
	return o
}

// buildCodeContext — servis + stacktrace'ten kaynak pencereleri.
//
// FAIL-OPEN her adımda: devops yok, katalogda depo yok, konvansiyon
// tutmadı, dosya ağaçta yok — hepsi boş bağlam + Reason. Açıklama
// yine üretilir, yalnızca kodsuz.
//
// v0.9.1241 — FetchCode'a VARAMAYAN çıkışlar da sayılır. Sayaç
// "operatör kod istedi" anını ölçmeli; yalnız işin devops paketine
// kadar gelebilen kısmını ölçerse, en sık çıkmazlar (stack yok, depo
// çözülemedi) isabet oranının dışında kalır ve oran olduğundan iyi
// görünür. TEK sayılmayan dal s.devops==nil: sayaçlar Service'e ait,
// Service yoksa sayacak yer de yok — ve o hâlde ölçülecek bir
// entegrasyon zaten yoktur.
func (s *Server) buildCodeContext(ctx context.Context, service, stack string) devops.CodeContext {
	if s.devops == nil {
		// Sayaç YOK (Service yok), ama SINIF var: v0.9.1243'ten beri
		// sınıf tek bir çağrının kaydına da yazılıyor ve o kayıt
		// Service'ten bağımsız. "Sayılmıyorsa adı da olmasın" demek,
		// maskeli kopyada bu hâli "sebep bilinmiyor" diye göstermek
		// olurdu — oysa sebep tam olarak biliniyor.
		return devops.CodeContext{
			Reason:  "kod entegrasyonu yapılandırılmamış",
			Outcome: devops.CodeUnconfigured,
		}
	}
	if strings.TrimSpace(stack) == "" {
		const reason = "bu kayıtta stacktrace yok"
		s.devops.RecordCodeOutcome(devops.CodeNoStack, reason)
		return devops.CodeContext{Reason: reason, Outcome: devops.CodeNoStack}
	}
	// Katalog pini önce: elle girilen depo konvansiyonu EZER.
	repoPin := ""
	if service != "" && s.store != nil {
		md, err := s.store.GetServiceMetadataStrict(ctx, service)
		mdRepo := ""
		if md != nil {
			mdRepo = md.Repository
		}
		pin, abort := pinReadDecision(mdRepo, md != nil, err)
		if abort != "" {
			s.devops.RecordCodeOutcome(devops.CodeCatalogError, abort)
			return devops.CodeContext{Reason: abort, Outcome: devops.CodeCatalogError}
		}
		repoPin = pin
	}
	res := devops.ResolveRepo(service, repoPin, s.devops.ResolveConfig())
	if res.Repo == "" {
		reason := res.Reason
		if reason == "" {
			reason = "servis için depo çözülemedi"
		}
		s.devops.RecordCodeOutcome(devops.CodeRepoUnresolved, reason)
		return devops.CodeContext{Reason: reason, Source: res.Source, Outcome: devops.CodeRepoUnresolved}
	}
	// v0.9.1183 — res.Project, proje ÖNERİSİ: pinin kendi taşıdığı proje
	// (v0.9.1240) ya da servis önekinden türetilen ad (bsa-… → BSA).
	// FetchCode onu yalnız ayardaki Project boşken kullanır; öneri boşsa
	// içindeki Reason çıkmazı üç kaynak üzerinden anlatır.
	cc := s.devops.FetchCode(ctx, res.Repo, res.Project, stackparse.ParseJava(stack))
	cc.Source = res.Source
	return cc
}

// pinReadDecision — katalog okumasının ÜÇ hâli → (pin, iptal nedeni).
// Saf; tablo-testli. v0.9.1236.
//
// Ayrım neden bu kadar önemli: bu yolda FAIL-OPEN'ın yönü değişiyor.
// Her yerde "kod gelmezse açıklama yine üretilsin" doğrudur, çünkü en
// kötü sonuç eksik kanıttır. Burada değil — operatör bir depo PİNLEDİ,
// yani konvansiyonun VAR OLAN ama YANLIŞ bir depoya çözüldüğünü zaten
// biliyor. Pin okunamadığında konvansiyona düşmek, tam da operatörün
// kapattığı hataya geri açmak ve BAŞKA bir uygulamanın kaynağını
// "kanıt" diye modele ve ekrana koymak demektir. Eksik kanıt kurtarılır,
// yanlış kanıt kurtarılamaz — o yüzden bu tek adım fail-CLOSED.
//
// Üç hâl BİLİNÇLİ olarak ayrı: (a) hata → iptal, (b) satır yok →
// konvansiyon (servis henüz küratörlenmemiş, normal), (c) satır var ama
// repository boş → konvansiyon (operatör diğer alanları doldurmuş,
// depoyu bilerek boş bırakmış). (b) ile (c) aynı sonuca çıkar ama aynı
// şey değildir; birleştirmek, ileride biri değişince ötekini de sessizce
// taşırdı.
func pinReadDecision(repo string, found bool, err error) (pin, abort string) {
	if err != nil {
		// Hata METNİ taşınmıyor: bu dize operatör ekranına ve AI
		// yüzeyine gidiyor, CH hatası ise bağlantı dizesini (parola
		// dahil) içerebilir. Teşhis sunucu loglarında duruyor.
		return "", "servis kataloğu okunamadı — yanlış depoya düşmemek için kod bağlamı atlandı"
	}
	if !found {
		return "", ""
	}
	return strings.TrimSpace(repo), ""
}

// codeContextPayload — yanıtın `code` alanı. Kod GÖVDESİ tarayıcıya
// gitmez; yalnız hangi depodan hangi dosyanın hangi aralığının
// okunduğu. Operatör kaynağı görmek isterse zaten DevOps'ta açar; kodu
// bir de API yanıtında taşımak, onu tarayıcı cache'ine ve HAR
// dökümlerine kopyalamak olurdu.
type codeContextPayload struct {
	Repo   string           `json:"repo,omitempty"`
	Branch string           `json:"branch,omitempty"`
	Source string           `json:"source,omitempty"` // pin | convention
	Files  []codeFileRefDTO `json:"files,omitempty"`
	Reason string           `json:"reason,omitempty"`
}

type codeFileRefDTO struct {
	Path     string `json:"path"`
	FromLine int    `json:"fromLine"`
	ToLine   int    `json:"toLine"`
	// Line — frame'in işaret ettiği hata satırı (v0.9.1254): modelin
	// ">>>" ile işaretlendiği satırın numarası. Kod İÇERİĞİ tarayıcıya
	// gitmez (bilinçli); numara, operatörün "model neye baktı"yı
	// içeriksiz görebildiği en küçük dürüst iz.
	Line int `json:"line,omitempty"`
}

// codePayload — CodeContext → wire. requested=false ise nil döner:
// kutuyu işaretlemeyen operatöre "kod yok" diye bir alan göstermek,
// olmayan bir arıza bildirmek olurdu.
func codePayload(cc devops.CodeContext, requested bool) *codeContextPayload {
	if !requested {
		return nil
	}
	p := &codeContextPayload{
		Repo: cc.Repo, Branch: cc.Branch, Source: cc.Source, Reason: cc.Reason,
	}
	for _, w := range cc.Windows {
		p.Files = append(p.Files, codeFileRefDTO{Path: w.Path, FromLine: w.FromLine, ToLine: w.ToLine, Line: w.Line})
	}
	return p
}

// isContextOverflowErr — sağlayıcı hatası "prompt bağlama sığmadı" mı?
// Saf; tablo-testli.
//
// İki koşul BİRLİKTE aranıyor: 400/413 sınıfı bir ret VE taşmaya işaret
// eden bir ifade. Yalnız "400" yetmez — response_format'ı reddeden bir
// 400 de vardır (copilot.go'daki mevcut JSON-mode merdiveni) ve onu
// taşma sanıp kodu yarıya indirmek yanlış teşhistir: cevap yine
// gelmez, üstüne bir çağrı daha yakılır.
func isContextOverflowErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "400") && !strings.Contains(msg, "413") {
		return false
	}
	for _, kw := range []string{
		"context length", "context_length", "context window", "maximum context",
		"too long", "too large", "exceed", "token limit", "tokens limit",
		"input length", "prompt is too", "reduce the length",
	} {
		if strings.Contains(msg, kw) {
			return true
		}
	}
	return false
}

// copilotExplainCode — kod bağlamlı Explain çağrısı.
//
// İki iş yapıyor, ikisi de burada olmak zorunda:
//
//  1. MASKELEME. Sağlayıcıya TAM prompt (kod dahil) gider; ai_calls
//     kaydına kod bloğu yerine `[kod: repo/dosya:aralık · N satır]`
//     özeti yazılır. Kaynak kod telemetri deposuna yazılmaz.
//  2. TAŞMA YENİDEN DENEMESİ. Bağlam taşması 400'ünde kod bloğu YARIYA
//     indirilip BİR kez yeniden denenir. copilot.go'daki mevcut
//     400-merdivenine (JSON mode / response_format yoklaması)
//     DOKUNULMUYOR — o merdiven parametre reddini çözer, bu katman
//     prompt boyutunu; ikisi farklı sorun ve tek bir yerde
//     birleştirilirse hangi hipotezin denendiği okunamaz hale gelir.
//
// Kod bağlamı boşsa doğrudan kodsuz yola düşer (fail-open) — ama
// SESSİZCE değil: v0.9.1243'ten beri maskeli kayda "[kod alınamadı:
// <sınıf>]" işareti düşer. Bu fonksiyona YALNIZ operatör "Kodu da
// incele"yi işaretlediğinde girilir (iki çağıranın ikisi de
// opts.IncludeCode kapısının arkasında), dolayısıyla işaretin varlığı
// "istendi" demektir ve yokluğu "hiç istenmedi".
func (s *Server) copilotExplainCode(r *http.Request, systemNoCode, systemWithCode, user string, cc devops.CodeContext) (string, error) {
	block := cc.PromptBlock()
	if block == "" {
		return s.explainNoCode(r, systemNoCode, user, cc.LogMissSummary())
	}
	out, err := s.explainWithCodeBlock(r, systemWithCode, user, block, cc.LogSummary())
	if err == nil || !isContextOverflowErr(err) {
		return out, err
	}
	half := cc.Halved()
	hb := half.PromptBlock()
	if hb == "" || hb == block {
		// Küçültecek bir şey kalmadı: kodsuz dene, cevapsız bırakma.
		// Kod BURADA vardı ama prompt'a sığmadı — taksonomideki bir
		// çıkmaz değil, o yüzden sınıf değil gerekçe yazılıyor. Kayda
		// "ıska" demek yine de doğru: bu SATIRIN prompt'unda kod yok.
		return s.explainNoCode(r, systemNoCode, user,
			devops.FormatCodeMissNote("", "bağlam taşması — kod bloğu prompt'a sığmadı"))
	}
	return s.explainWithCodeBlock(r, systemWithCode, user, hb, half.LogSummary())
}

// explainNoCode — kodsuz çağrı + maskeli kayda ıska işareti.
//
// GERÇEK prompt'a dokunulmaz: işaret yalnız log kopyasına gider.
// Modele "kod alınamadı" diye bir satır göndermek, olmayan bir kanıt
// hakkında konuşmasına davetiye olurdu (kodsuz system prompt'unun
// zaten sustuğu bir konu); kayıt ise tam tersine bunu bilmek zorunda.
func (s *Server) explainNoCode(r *http.Request, system, user, missNote string) (string, error) {
	if missNote == "" {
		return s.copilotExplain(r, system, user)
	}
	return s.copilotExplainMasked(r, system, user, user+missNote)
}

// explainWithCodeBlock — tek çağrı: gerçek prompt = user + block,
// log kopyası = user + summary.
func (s *Server) explainWithCodeBlock(r *http.Request, system, user, block, summary string) (string, error) {
	full := user + block
	return s.copilotExplainMasked(r, system, full, devops.MaskCodeInPrompt(full, block, summary))
}
