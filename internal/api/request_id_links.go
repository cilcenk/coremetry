package api

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/reqid"
)

// request_id_links.go — CoSRE SOHBET cevabındaki request_id'leri log
// köprüsü linkine çevirir (v0.9.709).
//
// Operator-reported: "Request'i buluyor loglardan CoSRE ama onu log
// köprüsü veya logizleme linki ile vermiyor." Ekran görüntüsünde model
// "Logizleme Linki:" başlığı atıp ÇIPLAK ID basıyordu.
//
// Kök neden: v0.9.655 köprüyü yalnız AI-ANALİZ kartına bağladı
// (correlationLinks → AIAnalysisPanel). Sohbet çekmecesi o yoldan
// geçmiyor; answer olayı {text, exchangeId} taşıyordu, links yoktu —
// oysa çip altyapısı v0.9.419'dan beri hazırdı (guided links).
//
// TASARIM — deterministik son-adım, tool değil: lokal küçük modelde
// (gemma4) ev doktrini "prefetch+narrate > tool-loop"
// (project-copilot-runtime). Modelden "link tool'u çağırmasını" ummak
// kırılgan; metinde GEÇEN id'yi yakalayıp linki SUNUCUDA kurmak her
// cevap biçiminde çalışır. Link şablon+ortam çözümü v0.9.655'in aynı
// yardımcıları (templateForService/envFromServiceName/
// buildCorrelationLink) — ikinci bir kopya YOK.

// reqIDKeyword — "request id" / "request_id" / "requestid" / "Request
// ID'si" gibi anmalar. Genel bir "uzun token yakala" regex'i BİLEREK
// yok: cevaplar trace id'ler, sürüm damgaları, pod adları taşıyor;
// anahtar kelimeye çapalamadan yakalamak yanlış pozitif kusar.
var reqIDKeyword = regexp.MustCompile(`(?i)request[ _-]?id`)

// reqIDToken — anahtar kelimeyi izleyen penceredeki aday. En az 12
// karakter, en az bir rakam; URL parçaları elenir ('/' '?' yok).
var reqIDToken = regexp.MustCompile(`[A-Za-z0-9][A-Za-z0-9._:-]{11,79}`)

// pureHex — 16/32 hex = span/trace id; onların kendi linkleri var,
// logizleme linki DEĞİLLER.
var pureHex = regexp.MustCompile(`^[0-9a-fA-F]{16}$|^[0-9a-fA-F]{32}$`)

const (
	reqIDWindow   = 200 // anahtar kelime sonrası tarama penceresi (bayt)
	reqIDMaxLinks = 5   // cevap başına çip tavanı — çip şeridi taşmasın
)

// extractRequestIDCandidates — SAF. Metindeki request_id adaylarını
// (sıra korunarak, tekilleştirilmiş) döndürür.
func extractRequestIDCandidates(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, kw := range reqIDKeyword.FindAllStringIndex(text, -1) {
		end := kw[1] + reqIDWindow
		if end > len(text) {
			end = len(text)
		}
		for _, tok := range reqIDToken.FindAllString(text[kw[1]:end], -1) {
			// "id" anmasının hemen ardındaki gövde kelimeleri
			// (örn. "değeri", "aşağıdadır") rakamsızdır — rakam şartı
			// onları eler; hex kuralı trace/span'i eler.
			if !strings.ContainsAny(tok, "0123456789") || pureHex.MatchString(tok) {
				continue
			}
			if !seen[tok] {
				seen[tok] = true
				out = append(out, tok)
			}
			if len(out) >= reqIDMaxLinks {
				return out
			}
		}
	}
	return out
}

// requestIDLinks — SAF yarı: adaylar + şablonlar → çipler. Şablon
// yoksa/geçersizse nil — kırık link, link yokluğundan kötü (v0.9.655
// ilkesi). Etiket ortamı söylüyor (yine v0.9.655: operatör test/prod
// kaydına baktığını bilmeli).
//
// loc (v0.9.1142) — kimliğin gömülü zamanını okumak için saat dilimi;
// nil = varsayılan. Şablon zaman yer tutucusu taşımıyorsa hiçbir etkisi
// yok (üretilen link bayt-bayt eskisi).
func requestIDLinks(text, service string, tpls correlationTemplates, loc *time.Location) []guidedAnswerLink {
	if len(tpls) == 0 {
		return nil
	}
	tpl := templateForService(service, tpls)
	if !validCorrelationTemplate(tpl) {
		return nil
	}
	env := envFromServiceName(service)
	if env == "" {
		env = "prod"
	}
	var out []guidedAnswerLink
	for _, id := range extractRequestIDCandidates(text) {
		if link, ok := requestIDLink(id, tpl, env, loc); ok {
			out = append(out, link)
		}
	}
	return out
}

// requestIDLink — tek kimlik → çip (v0.9.1142).
//
// Kimlik YAPILANDIRILMIŞ biçime uyuyorsa (internal/reqid) linkin zaman
// yer tutucuları kimliğin İÇİNDEKİ damgadan doldurulur: operatörün log
// arayüzü doğru aralıkta açılır. Uymuyorsa davranış aynen v0.9.709'un
// davranışı — şablonda zaman yer tutucusu yoksa hiçbir fark yok.
func requestIDLink(id, tpl, env string, loc *time.Location) (guidedAnswerLink, bool) {
	var from, to time.Time
	if parsed, ok := reqid.Parse(id, loc); ok {
		from, to = parsed.Window()
	}
	href := buildCorrelationLinkAt(tpl, id, from, to)
	if href == "" {
		return guidedAnswerLink{}, false
	}
	short := id
	if len(short) > 14 {
		short = short[:12] + "…"
	}
	// v0.9.1143 — operatör isteği: çip "Log (prod)" değil kurumun
	// tanıdığı adla "Logizleme (Prod)" desin. Ortam adı ASCII env
	// anahtarı (prod/int/uat/prep) — Türkçe İ kuralı bilerek YOK,
	// "İnt" garip dururdu.
	return guidedAnswerLink{Label: "Logizleme (" + capASCIIFirst(env) + ") · " + short, Href: href}, true
}

// capASCIIFirst — env anahtarının ilk ASCII harfini büyütür (görsel).
func capASCIIFirst(s string) string {
	if s == "" {
		return s
	}
	if c := s[0]; c >= 'a' && c <= 'z' {
		return string(c-32) + s[1:]
	}
	return s
}

// answerRequestIDLinks — handler yarısı: ayarı okur, saf yarıya verir.
//
// Şablon yoksa saat dilimi ayarı HİÇ okunmuyor: köprü yapılandırılmamış
// kurulumlarda (varsayılan) ek bir settings point-read'i ödemeyelim.
func (s *Server) answerRequestIDLinks(ctx context.Context, text, service string) []guidedAnswerLink {
	tpls := s.correlationLinkTemplates(ctx)
	if len(tpls) == 0 {
		return nil
	}
	return requestIDLinks(text, service, tpls, reqid.Location(s.reqidTZSetting(ctx)))
}

// knownRequestIDLinks — BİLİNEN bir kimlik için köprü çipi (v0.9.1142).
//
// answerRequestIDLinks cevap METNİNDEN kimlik avlıyor ve bu her cevap
// biçiminde çalışması için bilinçli bir tercihti. Ama yapılandırılmış
// kimlik rotasında (guidedRequestID) kimliği SUNUCU zaten biliyor:
// modelin onu cevapta tekrar etmesini beklemek gereksiz kırılganlık.
// İki kaynak çakışırsa href'e göre tekilleşiyor (dedupLinksByHref).
func (s *Server) knownRequestIDLinks(ctx context.Context, id, service string) []guidedAnswerLink {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	tpls := s.correlationLinkTemplates(ctx)
	if len(tpls) == 0 {
		return nil
	}
	tpl := templateForService(service, tpls)
	if !validCorrelationTemplate(tpl) {
		return nil
	}
	env := envFromServiceName(service)
	if env == "" {
		env = "prod"
	}
	link, ok := requestIDLink(id, tpl, env, reqid.Location(s.reqidTZSetting(ctx)))
	if !ok {
		return nil
	}
	return []guidedAnswerLink{link}
}

// dedupLinksByHref — aynı href'i iki kez çizmeyelim (v0.9.1142). SAF.
// Sıra korunur: ilk görülen kazanır, yani rotadan gelen deterministik
// çipler metinden avlananların önünde kalır.
func dedupLinksByHref(in []guidedAnswerLink) []guidedAnswerLink {
	if len(in) < 2 {
		return in
	}
	seen := make(map[string]bool, len(in))
	out := make([]guidedAnswerLink, 0, len(in))
	for _, l := range in {
		if seen[l.Href] {
			continue
		}
		seen[l.Href] = true
		out = append(out, l)
	}
	return out
}
