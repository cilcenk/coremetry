// v0.9.586 — span attribute'larında UTF-8 güvenliği.
//
// Operator-reported, prod:
//
//	traces export: rpc error: code = Internal desc = grpc: error while
//	marshaling: string field contains invalid UTF-8
//
// protobuf'un string alanı geçerli UTF-8 ŞART koşar ve marshaling
// hatası TÜM BATCH'i düşürür. Yani filodaki TEK bir bozuk bayt, o
// turdaki bütün self-telemetri span'lerini yok ediyordu — sessiz ve
// toplu bir kayıp, üstelik gözlemlenebilirlik katmanının kendisinde.
package selfobs

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSafeAttr(t *testing.T) {
	// Geçerli girdi AYNEN dönmeli — sıcak yolda gereksiz ayırma yok.
	for _, s := range []string{
		"", "plain ascii", "Türkçe: ğüşiöç", "日本語", "emoji 🎯",
		"Cannot convert string '2026-08-02 08:26:15' to type DateTime",
	} {
		if got := SafeAttr(s); got != s {
			t.Errorf("geçerli girdi değiştirildi: %q → %q", s, got)
		}
	}

	// Bozuk bayt DİZİSİ temizlenmeli.
	bad := "ClickHouse: Cannot convert string '" + string([]byte{0xff, 0xfe, 0x80}) + "' to type"
	if utf8.ValidString(bad) {
		t.Fatal("test girdisi zaten geçerli — vaka anlamsız")
	}
	got := SafeAttr(bad)
	if !utf8.ValidString(got) {
		t.Errorf("temizlenmiş metin HÂLÂ geçersiz — gRPC marshaling patlar ve "+
			"TÜM batch düşer: %q", got)
	}
	// Anlamlı kısım korunmalı: hata mesajının kendisi tanı için gerekli.
	if !strings.Contains(got, "Cannot convert string") {
		t.Errorf("mesajın anlamlı kısmı kaybolmuş: %q", got)
	}
}

// CH hata mesajları filodan gelen veriyi AYNEN alıntılar — bozuk
// baytın gerçek kaynağı orası. Bu vaka o senaryonun kendisi.
func TestSafeAttrHandlesQuotedFleetData(t *testing.T) {
	fleet := string([]byte{0x41, 0xc3, 0x28, 0x42}) // geçersiz 2-bayt dizisi
	msg := "code: 53, message: Cannot parse '" + fleet + "'"
	got := SafeAttr(msg)
	if !utf8.ValidString(got) {
		t.Errorf("filo verisi alıntılayan hata mesajı temizlenmedi: %q", got)
	}
	if !strings.Contains(got, "code: 53") {
		t.Errorf("hata kodu kaybolmuş — tanı için gerekli: %q", got)
	}
}
