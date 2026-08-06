package api

import (
	"context"
	"regexp"
	"strings"
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
func requestIDLinks(text, service string, tpls correlationTemplates) []guidedAnswerLink {
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
		short := id
		if len(short) > 14 {
			short = short[:12] + "…"
		}
		out = append(out, guidedAnswerLink{
			Label: "Log (" + env + ") · " + short,
			Href:  buildCorrelationLink(tpl, id),
		})
	}
	return out
}

// answerRequestIDLinks — handler yarısı: ayarı okur, saf yarıya verir.
func (s *Server) answerRequestIDLinks(ctx context.Context, text, service string) []guidedAnswerLink {
	return requestIDLinks(text, service, s.correlationLinkTemplates(ctx))
}
