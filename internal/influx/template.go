package influx

// template.go — Flux şablon doldurma (v0.10.222, audit §2 + R1).
//
// {{from}} / {{to}} / {{op}} / {{err}} gibi yer tutucular operatörün
// yazdığı Flux'ın STRING LITERAL'ının içine giriyor. Kaçış yerine DAR
// KAPI: değer ^[A-Za-z0-9_.:\-]{1,128}$ değilse doldurulmaz ve hata döner
// — `"`, `\`, yeni satır, boşluk hiçbir yoldan Influx'a ulaşamaz.
// Kapıdan geçmeyen OPERATIONCODE/ERRORCODE değeri enrichment'ta atlanır
// ve sayaca yazılır (D4). Bilinmeyen yer tutucu da HATA: sessizce boş
// kalan bir filtre "tüm gruplar"a genişlerdi.

import (
	"fmt"
	"os"
	"regexp"
)

var (
	placeholderRe = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)
	// 128: operatörün OPERATIONCODE değerleri 40-60 karakter
	// (CASHMANAGEMENT_NYT_INSTRUCTION_INQUIRY, Grafana 2026-09-01).
	valueRe       = regexp.MustCompile(`^[A-Za-z0-9_.:\-]{1,128}$`)
)

// ValidValue — şablona girebilecek tek değer biçimi (RFC3339 zaman
// damgaları da bu kapıdan geçer: 2026-09-01T10:00:00Z).
func ValidValue(v string) bool { return valueRe.MatchString(v) }

// FillTemplate — tüm yer tutucuları doldurur; ilk hata döner.
func FillTemplate(tpl string, vars map[string]string) (string, error) {
	var firstErr error
	out := placeholderRe.ReplaceAllStringFunc(tpl, func(m string) string {
		name := placeholderRe.FindStringSubmatch(m)[1]
		v, ok := vars[name]
		if !ok {
			if firstErr == nil {
				firstErr = fmt.Errorf("şablon: bilinmeyen yer tutucu {{%s}}", name)
			}
			return m
		}
		if !ValidValue(v) {
			if firstErr == nil {
				firstErr = fmt.Errorf("şablon: {{%s}} değeri kapıdan geçmedi (%q)", name, v)
			}
			return m
		}
		return v
	})
	if firstErr != nil {
		return "", firstErr
	}
	return out, nil
}

// osGetenv / osReadFile — secret.go'nun üretim bağlayıcıları (testler
// resolveTokenRef'e kendi fonksiyonlarını verir).
var (
	osGetenv   = os.Getenv
	osReadFile = os.ReadFile
)
