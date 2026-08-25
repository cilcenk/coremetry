// v0.9.655 — korelasyon kimliğinden DIŞ log sistemine köprü.
//
// Operatör: "Request Id bulduğunda prod'ta log izleme linkini de versin
// parametrik olarak." Ardından: "test ortamlarının log adresleri
// ayrı … service isimlerinin sonunda -int -prep -uat gördüğünde o
// adreslere yönlendirsin."
//
// v0.9.580 zaten örnek request_id'leri buluyor ve cevaba yazıyor
// ("Örnek request_id: SPE0250…"). Ama operatör o değeri KOPYALAYIP
// kurumun log arayüzüne elle yapıştırıyor. Cevap bir başlangıç noktası
// veriyordu; tık verilebilirdi.
//
// ŞABLONLAR AYARDIR, KODA GÖMÜLMEZ. İki sebep:
//  1. Her kurulumun log sistemi başka; adres kod tabanına ait değil.
//  2. Bu depo bir müşteri adresi taşımaz.
//
// ORTAM BAŞINA ŞABLON: aynı kurulum birden çok ortamın verisini
// taşıyabiliyor (project-env-separation) ve her ortamın log arayüzü
// ayrı bir adres. Ortam SERVİS ADININ SONEKİNDEN çözülüyor — operatörün
// tarif ettiği gerçek: "-int", "-uat", "-prep".
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/reqid"
)

// correlationLinkPlaceholder — şablonda değerin geçeceği yer.
const correlationLinkPlaceholder = "{value}"

// Zaman yer tutucuları (v0.9.1142) — OPSİYONEL.
//
// Neden geldiler: yapılandırılmış request kimliği kendi TARİH+SAATİNİ
// taşıyor (internal/reqid). Kurumsal log arayüzleri neredeyse her zaman
// bir zaman aralığı istiyor ve o aralık girilmediğinde ya çok geniş
// arıyor ya hiç bulmuyor. Kimliği çözebiliyorsak linki de pencereleyelim.
//
// İKİ AİLE, çünkü hangi biçimi istediği log sistemine göre değişiyor ve
// TAHMİN ETMEK yanlış link üretir: ISO-8601 (ofsetli, yani belirsizlik
// yok) ve epoch milisaniye. Operatör hangisini koyarsa o dolar.
//
// {value} ZORUNLU kalır; bunların hiçbiri şart değil. Şablonda yoklarsa
// üretilen link bayt-bayt eskisidir (regresyon yok).
const (
	corrPlaceholderFrom   = "{from}"
	corrPlaceholderTo     = "{to}"
	corrPlaceholderFromMs = "{from_ms}"
	corrPlaceholderToMs   = "{to_ms}"
)

// corrTimePlaceholders — FE'ye ipucu olarak dönen liste (tek kaynak).
var corrTimePlaceholders = []string{
	corrPlaceholderFrom, corrPlaceholderTo, corrPlaceholderFromMs, corrPlaceholderToMs,
}

// corrBridgeFallbackLookback — kimlik ÇÖZÜLEMEDİĞİNDE zaman yer
// tutucularına yazılan pencerenin geriye bakışı.
//
// Kimliğin damgasını okuyamadığımız hâlde (eski/serbest biçimli id'ler,
// v0.9.709 yolu) elimizdeki tek çapa ŞİMDİ. Bu bilinçli bir SAPMA ve
// operatöre görünür: link yine çalışır, yalnız aralık dar değil.
const corrBridgeFallbackLookback = time.Hour

// corrBridgeForwardPad — pencerenin üst kenarına eklenen küçük pay
// (ingest gecikmesi / saat kayması).
const corrBridgeForwardPad = time.Minute

// correlationLinkSettingKey — system_settings anahtarı.
const correlationLinkSettingKey = "correlation.link_template"

// correlationEnvSuffixes — servis adından ORTAM çözümlemesi.
//
// Sıra önemli değil (sonekler ayrık) ama liste TEK yerde: ikinci bir
// kopya, bu kod tabanının tekrar eden hata sınıfı.
//
// "default" bilinçli olarak listede YOK: soneksiz bir servis adı prod
// demek ve prod şablonu "default" anahtarında duruyor.
var correlationEnvSuffixes = []string{"int", "uat", "prep"}

// correlationTemplates — ortam → şablon. "default" soneksiz (prod)
// servisler için.
type correlationTemplates map[string]string

// envFromServiceName — servis adının sonekinden ortam. "" = soneksiz.
//
// SAF (tablo testli). Yalnız SONEK: "integration-service" içinde "int"
// geçiyor ama sonu "-int" değil, yani prod'dur. Alt dize araması burada
// sessizce yanlış ortama yönlendirirdi.
func envFromServiceName(svc string) string {
	svc = strings.ToLower(strings.TrimSpace(svc))
	for _, e := range correlationEnvSuffixes {
		if strings.HasSuffix(svc, "-"+e) {
			return e
		}
	}
	return ""
}

// templateForService — bu servis için hangi şablon.
//
// Ortam şablonu yoksa "default"a düşülüyor: operatör yalnız prod adresi
// girdiyse test servisleri de o linki alır — yanlış ortama gitmektense
// hiç link vermemek daha doğru olurdu, ama şablon YOKSA zaten link
// çizilmiyor (buildCorrelationLink ""). Buradaki düşüş, operatörün TEK
// bir adres girdiği kurulum için.
//
// SAF (tablo testli).
func templateForService(svc string, tpls correlationTemplates) string {
	if env := envFromServiceName(svc); env != "" {
		if t := strings.TrimSpace(tpls[env]); t != "" {
			return t
		}
	}
	return strings.TrimSpace(tpls["default"])
}

// validCorrelationTemplate — şablon kullanılabilir mi?
//
// Üç koşul ve üçü de güvenlik:
//   - http/https ŞART: `javascript:` bir şablona konursa cevap
//     tıklanabilir bir betiğe dönerdi.
//   - {value} ŞART: yoksa her kimlik AYNI linke gider ve operatör
//     yanlış kaydı açar — sessiz ve fark edilmesi zor.
//   - host ŞART: şemasız/hostsuz bir metin link değil.
//
// SAF (tablo testli).
func validCorrelationTemplate(tpl string) bool {
	tpl = strings.TrimSpace(tpl)
	if tpl == "" || !strings.Contains(tpl, correlationLinkPlaceholder) {
		return false
	}
	// Yer tutucuları ayrıştırmadan ÖNCE zararsız bir değerle doldur:
	// "{value}" ham hâliyle bazı ayrıştırıcıları şaşırtabiliyor. v0.9.1142 —
	// zaman yer tutucuları da doldurulmalı, yoksa onları taşıyan geçerli
	// bir şablon ayrıştırıcıya ham süslü parantezle giderdi.
	filled := strings.ReplaceAll(tpl, correlationLinkPlaceholder, "x")
	for _, p := range corrTimePlaceholders {
		filled = strings.ReplaceAll(filled, p, "x")
	}
	u, err := url.Parse(filled)
	if err != nil {
		return false
	}
	return (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// buildCorrelationLink — şablon + değer → URL. Pencere bilinmiyor.
//
// Değer QUERY-ENCODE ediliyor: request_id'ler kurum formatında geliyor
// ve içinde &, #, boşluk bulunabilir. Ham yapıştırma linki bozar ya da
// daha kötüsü, sessizce BAŞKA bir sorgu üretir.
//
// SAF (tablo testli). Şablon geçersizse "" döner — çağıran çizmez.
func buildCorrelationLink(tpl, value string) string {
	return buildCorrelationLinkAt(tpl, value, time.Time{}, time.Time{})
}

// buildCorrelationLinkAt — şablon + değer + PENCERE → URL (v0.9.1142).
//
// from/to sıfırsa pencere ŞİMDİye çapalanır (corrBridgeFallbackLookback):
// şablonda zaman yer tutucusu varsa onları çözümsüz bırakmak linki
// KIRARDI, ve kırık link yokluktan kötüdür (v0.9.655 ilkesi).
//
// Zaman yer tutucusu taşımayan şablonlarda çıktı bayt-bayt eskisidir —
// bu, v0.9.709 davranışının regresyonsuz kalmasının garantisi.
//
// SAF (tablo testli).
func buildCorrelationLinkAt(tpl, value string, from, to time.Time) string {
	value = strings.TrimSpace(value)
	if value == "" || !validCorrelationTemplate(tpl) {
		return ""
	}
	tpl = strings.TrimSpace(tpl)
	out := strings.ReplaceAll(tpl, correlationLinkPlaceholder, url.QueryEscape(value))
	if !corrTemplateHasTime(tpl) {
		return out
	}
	if from.IsZero() || to.IsZero() || !to.After(from) {
		now := time.Now()
		from, to = now.Add(-corrBridgeFallbackLookback), now.Add(corrBridgeForwardPad)
	}
	// ISO ofsetli: log sistemi hangi saat diliminde okuduğunu tahmin
	// etmek zorunda kalmasın. ms: zaman dilimi kavramı olmayan arayüzler.
	return strings.NewReplacer(
		corrPlaceholderFrom, url.QueryEscape(from.Format(time.RFC3339)),
		corrPlaceholderTo, url.QueryEscape(to.Format(time.RFC3339)),
		corrPlaceholderFromMs, strconv.FormatInt(from.UnixMilli(), 10),
		corrPlaceholderToMs, strconv.FormatInt(to.UnixMilli(), 10),
	).Replace(out)
}

// corrTemplateHasTime — şablon zaman yer tutucusu taşıyor mu? SAF.
func corrTemplateHasTime(tpl string) bool {
	for _, p := range corrTimePlaceholders {
		if strings.Contains(tpl, p) {
			return true
		}
	}
	return false
}

// correlationLinks — örneklerden tıklanabilir köprüler.
//
// Cevap metnine DEĞİL, ayrı bir alana gidiyor: metin modelden geçiyor
// ve bir URL'yi modele emanet etmek onu bozma riski demek. Bu alan
// sunucuda deterministik üretiliyor ("Kaynak:" alt bilgisiyle aynı
// ilke).
//
// Anahtar başına en fazla iki link: örnekler zaten 3 ile sınırlı
// (corrSampleValuesPerKey) ve üç anahtar × üç değer bir çip yığını
// olurdu.
func correlationLinks(samples []chstore.CorrelationSample, service string, tpls correlationTemplates) []guidedAnswerLink {
	tpl := templateForService(service, tpls)
	if !validCorrelationTemplate(tpl) {
		return nil
	}
	env := envFromServiceName(service)
	var out []guidedAnswerLink
	for _, s := range samples {
		for i, v := range s.Values {
			if i >= 2 {
				break
			}
			href := buildCorrelationLink(tpl, v)
			if href == "" {
				continue
			}
			label := "Log: " + shortCorrValue(v)
			if env != "" {
				// Ortam etiketi GÖRÜNÜR olsun: operatör test ve prod
				// sekmelerini yan yana açıyor, hangi linkin nereye
				// gittiği çipten okunmalı.
				label = fmt.Sprintf("Log (%s): %s", env, shortCorrValue(v))
			}
			out = append(out, guidedAnswerLink{Label: label, Href: href})
		}
	}
	return out
}

// shortCorrValue — çip etiketinde kısaltılmış kimlik. Kurum request
// id'leri 40+ karakter olabiliyor ve çip şeridini taşırıyor.
func shortCorrValue(v string) string {
	const max = 14
	if len(v) <= max {
		return v
	}
	return v[:max] + "…"
}

// ── Ayar okuma ──────────────────────────────────────────────────────────────

// correlationLinkTemplates — system_settings'ten ortam→şablon haritası.
//
// Her istekte okunuyor ve bu bilinçli: analiz cevabı zaten 5 dakika
// önbellekli, yani gerçek okuma sıklığı düşük. Boot'ta yüklenen bir
// kopya, operatör şablonu değiştirdiğinde bir sonraki yeniden
// başlatmaya kadar bayat kalırdı.
//
// Hata hâlinde boş harita — link çizilmez. Kırık bir link, link
// yokluğundan kötüdür.
func (s *Server) correlationLinkTemplates(ctx context.Context) correlationTemplates {
	// v0.10.35 — NIL KORUMASI. Şablon okuması v0.10.35'te deliverExplain'e
	// de bağlandı (15 explain ucu) ve o yol store'suz kurulan Server'larla
	// da çağrılıyor; korumasız hâli testte nil deref ile PANİKLİYORDU.
	//
	// Aynı sınıf bugün v0.10.28'de de çıkmıştı (warmDependenciesCache) —
	// orada mevcut bir kusuru buldum, burada KENDİM açtım ve mevcut kapı
	// (TestDeliverExplainStreamFrameSequence) yakaladı. Şablon yoksa link
	// de yok: bu yolun zaten sessiz-düşüş sözleşmesi var.
	if s == nil || s.store == nil {
		return nil
	}
	b, err := s.store.GetSetting(ctx, correlationLinkSettingKey)
	if err != nil || len(b) == 0 {
		return nil
	}
	var tpls correlationTemplates
	if json.Unmarshal(b, &tpls) != nil {
		return nil
	}
	return tpls
}

// ── Admin ayarı ─────────────────────────────────────────────────────────────

// reqidTZSetting — yapılandırılmış request kimliğinin saat dilimi ayarı.
// AYRI system_settings anahtarı (bkz. reqid/settings.go gerekçesi): şablon
// blob'u düz bir map ve ona alan eklemek şablonları kaybetme riski.
func (s *Server) reqidTZSetting(ctx context.Context) string {
	b, err := s.store.GetSetting(ctx, reqid.SettingKey)
	if err != nil {
		return ""
	}
	return reqid.DecodeSettings(b).TZ
}

func (s *Server) getCorrelationLinkSetting(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"templates":   s.correlationLinkTemplates(r.Context()),
		"placeholder": correlationLinkPlaceholder,
		"envs":        append([]string{"default"}, correlationEnvSuffixes...),
		// v0.9.1142 — opsiyonel zaman yer tutucuları + kimliğin saat
		// dilimi. Liste SUNUCUDAN geliyor ki FE ipucu metni ile gerçek
		// çözümleyici ayrışmasın.
		"timePlaceholders": corrTimePlaceholders,
		"reqidTz":          s.reqidTZSetting(r.Context()),
		"reqidTzDefault":   reqid.DefaultTZ,
	})
}

// putCorrelationLinkSetting — ortam→şablon haritasını kaydeder.
//
// BOŞ değer o ortamı KAPATIR (silme yerine) — operatör bir ortamı
// geçici olarak devre dışı bırakıp geri açabilsin.
//
// Doğrulama SUNUCUDA: geçersiz bir şablon kaydedilirse özellik sessizce
// çalışmaz ve operatör nedenini göremezdi. 400 + hangi ortamın hatalı
// olduğu dönüyor.
func (s *Server) putCorrelationLinkSetting(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Templates correlationTemplates `json:"templates"`
		// ReqidTz (v0.9.1142) — İŞARETÇİ ve bu bilinçli: alanı GÖNDERMEYEN
		// bir çağıran (curl, eski frontend) saklı tz'yi SİLMEMELİ. nil =
		// dokunma, "" = varsayılana dön, değer = ayarla.
		ReqidTz *string `json:"reqidTz"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	// Saat dilimi ÖNCE doğrulanıyor: geçersiz bir tz ile şablonları
	// kaydedip yarım başarı döndürmek en kötü sonuç olurdu.
	tzChanged := ""
	if body.ReqidTz != nil {
		tz := strings.TrimSpace(*body.ReqidTz)
		if tz != "" {
			if _, err := time.LoadLocation(tz); err != nil {
				http.Error(w, "geçersiz saat dilimi: "+tz+" (IANA adı bekleniyor, ör. "+reqid.DefaultTZ+")",
					http.StatusBadRequest)
				return
			}
		}
		tzb, _ := json.Marshal(reqid.Settings{TZ: tz})
		if err := s.store.PutSetting(r.Context(), reqid.SettingKey, tzb); err != nil {
			writeErr(w, err)
			return
		}
		if tz == "" {
			tzChanged = "; reqidTz: varsayılan"
		} else {
			tzChanged = "; reqidTz: " + tz
		}
	}
	allowed := map[string]bool{"default": true}
	for _, e := range correlationEnvSuffixes {
		allowed[e] = true
	}
	clean := correlationTemplates{}
	for env, tpl := range body.Templates {
		if !allowed[env] {
			http.Error(w, "bilinmeyen ortam: "+env, http.StatusBadRequest)
			return
		}
		tpl = strings.TrimSpace(tpl)
		if tpl == "" {
			continue // o ortam kapalı
		}
		if !validCorrelationTemplate(tpl) {
			http.Error(w,
				env+": şablon http(s) olmalı ve "+correlationLinkPlaceholder+" yer tutucusunu taşımalı",
				http.StatusBadRequest)
			return
		}
		clean[env] = tpl
	}
	b, _ := json.Marshal(clean)
	if err := s.store.PutSetting(r.Context(), correlationLinkSettingKey, b); err != nil {
		writeErr(w, err)
		return
	}
	// CLAUDE.md sert kısıtı: durum yazan her admin işlemi denetim satırı.
	// ADRESLER DENETİME YAZILMIYOR — yalnız hangi ortamların
	// yapılandırıldığı. Denetim kaydı bir kurulum adresi taşımamalı.
	envs := make([]string, 0, len(clean))
	for e := range clean {
		envs = append(envs, e)
	}
	s.audit(r, "settings.update", "correlation-link", correlationLinkSettingKey,
		"configured: "+strings.Join(envs, ",")+tzChanged)
	writeJSON(w, map[string]any{
		"templates": clean,
		"reqidTz":   s.reqidTZSetting(r.Context()),
	})
}
