package api

import (
	"testing"
	"time"
)

// v0.9.954 — UX denetimi F5 / Ö13.
//
// ORİJİNAL BELİRTİ: Inbox "ne oldu?" sorusunun doğal girişi ama zaman
// penceresi sorulamıyordu — en dar basamak 2 saatti. Bir olayın hemen
// ardından bakan operatör "şu 20 dakikada ortaya çıkanlar"ı
// kuramıyor, en dar seçenekte bile 2 saatlik gürültüyü birlikte
// alıyordu.
//
// ASIL RİSK İKİ FONKSİYONUN AYRIŞMASI: normalizeInboxSince bir değeri
// GEÇERLİ sayıp inboxSinceDuration onu tanımazsa, filtre seçili
// görünür ama süre 0 döner — yani seçenek SESSİZCE "hepsi" gibi
// davranır. Boş liste değil, YANLIŞ liste: en pahalı hata biçimi.
func TestInboxSinceRungs(t *testing.T) {
	want := map[string]time.Duration{
		"30m": 30 * time.Minute,
		"1h":  time.Hour,
		"2h":  2 * time.Hour,
		"24h": 24 * time.Hour,
		"7d":  7 * 24 * time.Hour,
	}
	if len(inboxSinceRungs) != len(want) {
		t.Fatalf("basamak sayısı listeyle uyuşmuyor: rungs=%d, beklenen=%d", len(inboxSinceRungs), len(want))
	}
	for _, v := range inboxSinceRungs {
		t.Run(v, func(t *testing.T) {
			if got := normalizeInboxSince(v); got != v {
				t.Errorf("normalizeInboxSince(%q) = %q — basamak GEÇERLİ sayılmalı", v, got)
			}
			if got := inboxSinceDuration(v); got != want[v] {
				t.Errorf("inboxSinceDuration(%q) = %v, beklenen %v", v, got, want[v])
			}
			if inboxSinceDuration(v) == 0 {
				t.Errorf("%q süresi 0 — filtre seçili görünür ama HİÇBİR ŞEYİ elemez", v)
			}
		})
	}
}

// F5'in ürün kararı: 30 dakika ARTIK VAR (Ö13'ün asıl isteği).
func TestInboxSinceHasSubHourRung(t *testing.T) {
	if normalizeInboxSince("30m") != "30m" {
		t.Fatal("30m basamağı yok — 'şu 20 dakikada ortaya çıkanlar' hâlâ kurulamıyor")
	}
	if inboxSinceDuration("30m") != 30*time.Minute {
		t.Fatal("30m süresi yanlış")
	}
}

// Bilinmeyen değer "" — serbest pencere sunucu cache anahtarının
// kardinalitesini patlatırdı (v0.8.270). Custom pencere BİLİNÇLİ yok.
func TestInboxSinceRejectsFreeform(t *testing.T) {
	for _, bad := range []string{"", "17m", "3h", "90s", "7 days", "1d", "24H", "  2h", "2h "} {
		if got := normalizeInboxSince(bad); got != "" {
			t.Errorf("normalizeInboxSince(%q) = %q — sabit olmayan değer REDDEDİLMELİ", bad, got)
		}
		if got := inboxSinceDuration(bad); got != 0 {
			t.Errorf("inboxSinceDuration(%q) = %v — tanınmayan değer 0 dönmeli", bad, got)
		}
	}
}

// ⚠ BİRİM TUZAĞI (v0.6.36 sınıfı): bu iki fonksiyon time.ParseDuration
// KULLANMIYOR ve bu bilinçli — Go 'd' birimini TANIMAZ, yani "7d"
// ParseDuration'a verilseydi sessizce varsayılana düşerdi. Açık switch
// o tuzağı yapısal olarak kapatıyor; bu test kararı kalıcı kılar.
func TestInboxSinceDayRungIsExplicit(t *testing.T) {
	if _, err := time.ParseDuration("7d"); err == nil {
		t.Fatal("Go artık 'd' tanıyor — bu testin gerekçesi değişti, yorumu güncelle")
	}
	if inboxSinceDuration("7d") != 7*24*time.Hour {
		t.Fatal("7d açık switch'ten gelmiyor — ParseDuration'a düşmüş olabilir")
	}
}
