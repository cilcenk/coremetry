package api

import (
	"strings"
	"testing"
	"time"
)

// v0.10.16 — F0.2a. /service DB-queries cache anahtarı nanosaniye
// taşıyordu; `to` şimdi-tabanlı olduğu için anahtar her istekte
// benzersizdi ve TTL bir dakikaymış gibi görünen cache HİÇ tutmuyordu.
//
// Kusuru ölçmek için canlı CH gerekmiyor: anahtarın kendisi saf, ve
// "aynı dakikadaki iki istek aynı anahtarı üretir mi" sorusu tablo
// testiyle çivilenebilir.

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// TestSameMinuteSharesOneKey — kusurun ta kendisi.
//
// Aynı dakika içindeki iki tık aynı anahtara düşmezse cache asla
// tutmaz. Bu test, düzeltmeden ÖNCE kırmızıydı.
func TestSameMinuteSharesOneKey(t *testing.T) {
	from := ts("2026-08-25T09:45:00Z")
	a := serviceDBQueriesKey("portfolio-service", from, ts("2026-08-25T10:00:03Z"), 50)
	b := serviceDBQueriesKey("portfolio-service", from, ts("2026-08-25T10:00:47Z"), 50)
	if a != b {
		t.Errorf("aynı dakikadaki iki istek AYRI anahtar üretti — cache ölü kalır:\n  %s\n  %s", a, b)
	}
}

// TestNextMinuteRotatesTheKey — negatif kontrol.
//
// Kovalama tazeliği öldürmemeli: bir sonraki dakika YENİ anahtar
// istemeli, yoksa panel donar.
func TestNextMinuteRotatesTheKey(t *testing.T) {
	from := ts("2026-08-25T09:45:00Z")
	a := serviceDBQueriesKey("svc", from, ts("2026-08-25T10:00:47Z"), 50)
	b := serviceDBQueriesKey("svc", from, ts("2026-08-25T10:01:02Z"), 50)
	if a == b {
		t.Error("dakika döndü ama anahtar aynı kaldı — veri donar")
	}
}

// TestKeyCarriesEveryInput — CLAUDE.md sert kısıtı: anahtar TÜM
// girdileri taşır. Birini düşürmek çapraz-zehirlenme demek (v0.5.187).
func TestKeyCarriesEveryInput(t *testing.T) {
	base := func() string {
		return serviceDBQueriesKey("svc-a", ts("2026-08-25T09:45:00Z"), ts("2026-08-25T10:00:00Z"), 50)
	}
	for _, tc := range []struct {
		name string
		got  string
	}{
		{"servis", serviceDBQueriesKey("svc-B", ts("2026-08-25T09:45:00Z"), ts("2026-08-25T10:00:00Z"), 50)},
		{"from", serviceDBQueriesKey("svc-a", ts("2026-08-25T09:30:00Z"), ts("2026-08-25T10:00:00Z"), 50)},
		{"to", serviceDBQueriesKey("svc-a", ts("2026-08-25T09:45:00Z"), ts("2026-08-25T10:05:00Z"), 50)},
		{"limit", serviceDBQueriesKey("svc-a", ts("2026-08-25T09:45:00Z"), ts("2026-08-25T10:00:00Z"), 20)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got == base() {
				t.Errorf("%s değişti ama anahtar aynı — çapraz-zehirlenme", tc.name)
			}
		})
	}
}

// ⚠ TestWindowNeverCollapses — bu testin sebebi, düzeltmenin İLK
// yazımının sessizce yanlış olması.
//
// İki ucu da aşağı kesmek doğal görünüyor ve sub-dakika bir pencerede
// `from == to` üretiyor: sorgu sıfır satır döner, panel boşalır, HATA
// VERMEZ. "Boş küme kaybolur, sıfır olmaz" sınıfı. `to` bu yüzden
// yukarı yuvarlanıyor.
func TestWindowNeverCollapses(t *testing.T) {
	for _, tc := range []struct{ name, from, to string }{
		{"40 saniyelik pencere", "2026-08-25T10:00:10Z", "2026-08-25T10:00:50Z"},
		{"1 saniyelik pencere", "2026-08-25T10:00:10Z", "2026-08-25T10:00:11Z"},
		{"dakika sınırını aşan 2sn", "2026-08-25T10:00:59Z", "2026-08-25T10:01:01Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bf, bt := serviceDBQueriesWindow(ts(tc.from), ts(tc.to))
			if !bt.After(bf) {
				t.Fatalf("pencere çöktü: from=%s to=%s — sorgu sessizce SIFIR satır döner", bf, bt)
			}
			// Kovalanmış pencere istenen aralığı KAPSAMALI, kırpmamalı.
			if bf.After(ts(tc.from)) {
				t.Errorf("from ileri kaydı: %s > %s", bf, tc.from)
			}
			if bt.Before(ts(tc.to)) {
				t.Errorf("to geri kaydı: %s < %s", bt, tc.to)
			}
		})
	}
}

// TestCeilIsIdempotentOnBoundary — tam dakikada duran bir `to` bir
// dakika daha ileri atılmamalı; yoksa her tam-dakika isteği gereksiz
// bir kova ileri gider ve komşu isteklerle anahtar paylaşmaz.
func TestCeilIsIdempotentOnBoundary(t *testing.T) {
	b := ts("2026-08-25T10:00:00Z")
	if got := dbQueriesCeil(b); !got.Equal(b) {
		t.Errorf("dbQueriesCeil(%s) = %s; sınırda sabit kalmalıydı", b, got)
	}
}

// TestHandlerUsesTheSharedHelpers — saf çekirdek yeşil ama KABLOLAMA
// pinli değilse kusur yerinde kalır (v0.9.1334 sınıfı).
//
// Ayrıca eski nanosaniyeli kuruluşun geri gelmesini de yasaklıyor.
func TestHandlerUsesTheSharedHelpers(t *testing.T) {
	src := readSourceFile(t, "api.go")
	if strings.Contains(src, `"service-db-queries:svc=%s:from=%d:to=%d:limit=%d"`) {
		t.Error("anahtar yeniden api.go içinde elle kuruluyor — nanosaniyeli sürüm geri gelmiş olabilir")
	}
	for _, must := range []string{
		"from, to = serviceDBQueriesWindow(from, to)",
		"serviceDBQueriesKey(name, from, to, limit)",
	} {
		if !strings.Contains(src, must) {
			t.Errorf("handler paylaşılan yardımcıyı kullanmıyor, kayıp: %s", must)
		}
	}
}
