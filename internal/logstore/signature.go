package logstore

// signature.go — log mesajı imzası (v0.10.229, Influx D4 audit §4 adım 4).
//
// Aynı şablondan gelen mesajları (farklı UUID / id / zaman / IP / sayı)
// tek imzada toplar: yer tutucu `<x>`, boşluk sıkıştırma, 512 karakter
// tavanı. Bu bir GRUPLAMA anahtarıdır, redaksiyon DEĞİL: örnek mesaj
// verbatim saklanır (feedback-no-redaction). Drain şablonlamasından ayrı:
// burada örnek tabanı yok, tek mesaj → tek imza, deterministik.

import (
	"regexp"
	"strings"

	"github.com/cespare/xxhash/v2"
)

const signatureMaxLen = 512

var (
	sigUUID = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	sigISO  = regexp.MustCompile(`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:[.,]\d+)?(?:Z|[+-]\d{2}:?\d{2})?`)
	sigIP   = regexp.MustCompile(`\b\d{1,3}(?:\.\d{1,3}){3}(?::\d{1,5})?\b`)
	sigHex  = regexp.MustCompile(`(?i)\b[0-9a-f]{16,}\b`)
	sigNum  = regexp.MustCompile(`\b\d{2,}`) // sondaki sınır YOK: "1500ms" → "<x>ms"; "OP12" başında sınır yok, kalır
	sigWS   = regexp.MustCompile(`\s+`)
)

// NormalizeSignature — mesajın imza şablonu. Sıra önemli: UUID önce (hex
// kuralı parçalarını yakalamasın), ISO zaman IP'den önce (tarih noktaları
// IP değil), hex sayıdan önce (uzun hex'in içindeki rakamlar sayı değil).
func NormalizeSignature(msg string) string {
	s := strings.TrimSpace(msg)
	if s == "" {
		return ""
	}
	s = sigUUID.ReplaceAllString(s, "<x>")
	s = sigISO.ReplaceAllString(s, "<x>")
	s = sigIP.ReplaceAllString(s, "<x>")
	s = sigHex.ReplaceAllString(s, "<x>")
	s = sigNum.ReplaceAllString(s, "<x>")
	s = strings.TrimSpace(sigWS.ReplaceAllString(s, " "))
	if len(s) > signatureMaxLen {
		s = s[:signatureMaxLen]
	}
	return s
}

// SignatureHash — imzanın xxhash64'ü (grup anahtarı; UI'da kısa kimlik).
func SignatureHash(sig string) uint64 { return xxhash.Sum64String(sig) }
