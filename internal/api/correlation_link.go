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
//   1. Her kurulumun log sistemi başka; adres kod tabanına ait değil.
//   2. Bu depo bir müşteri adresi taşımaz.
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
	"strings"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// correlationLinkPlaceholder — şablonda değerin geçeceği yer.
const correlationLinkPlaceholder = "{value}"

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
	// Yer tutucuyu ayrıştırmadan ÖNCE zararsız bir değerle doldur:
	// "{value}" ham hâliyle bazı ayrıştırıcıları şaşırtabiliyor.
	u, err := url.Parse(strings.ReplaceAll(tpl, correlationLinkPlaceholder, "x"))
	if err != nil {
		return false
	}
	return (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// buildCorrelationLink — şablon + değer → URL.
//
// Değer QUERY-ENCODE ediliyor: request_id'ler kurum formatında geliyor
// ve içinde &, #, boşluk bulunabilir. Ham yapıştırma linki bozar ya da
// daha kötüsü, sessizce BAŞKA bir sorgu üretir.
//
// SAF (tablo testli). Şablon geçersizse "" döner — çağıran çizmez.
func buildCorrelationLink(tpl, value string) string {
	value = strings.TrimSpace(value)
	if value == "" || !validCorrelationTemplate(tpl) {
		return ""
	}
	return strings.ReplaceAll(strings.TrimSpace(tpl), correlationLinkPlaceholder, url.QueryEscape(value))
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

func (s *Server) getCorrelationLinkSetting(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"templates":   s.correlationLinkTemplates(r.Context()),
		"placeholder": correlationLinkPlaceholder,
		"envs":        append([]string{"default"}, correlationEnvSuffixes...),
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
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
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
		"configured: "+strings.Join(envs, ","))
	writeJSON(w, map[string]any{"templates": clean})
}
