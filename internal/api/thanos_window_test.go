package api

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// v0.9.1370 — /clusters trend pencere tavanı (operatör-bildirimi:
// "Infrastructure'da hangi aralığı seçersem seçeyim hep aynı zamanı
// gösteriyor — 6 saate kadar takip ediyor, 6 saat ve üstünde son 6
// saati gösteriyor").
//
// Kelepçe SPAN'i sınırlar, ÇAPAYI değil: bu ayrım sessizce kaybolursa
// geçmişe bakan operatör "şimdinin son 24 saatine" fırlatılır ve bunu
// fark etmesi çok zordur (grafik dolu görünür, yalnız yanlış zamandır).
// Onun için her satır `to`nun DEĞİŞMEDİĞİNİ ayrıca doğruluyor.
func TestClampThanosWindow(t *testing.T) {
	// Sabit çapa: time.Now() kullanmak testi saate bağlardı.
	to := time.Date(2026, 8, 25, 14, 30, 0, 0, time.UTC)

	cases := []struct {
		name        string
		span        time.Duration
		wantSpan    time.Duration
		wantClamped bool
	}{
		{"15dk — dokunulmaz", 15 * time.Minute, 15 * time.Minute, false},
		{"1s — dokunulmaz", time.Hour, time.Hour, false},
		{"6s — dokunulmaz (eski tavan artık kelepçe DEĞİL)", 6 * time.Hour, 6 * time.Hour, false},
		{"6s+1sn — eskiden kelepçelenirdi, artık geçer", 6*time.Hour + time.Second, 6*time.Hour + time.Second, false},
		{"12s — yeni tavanın altı, tam geçer", 12 * time.Hour, 12 * time.Hour, false},
		{"24s — TAM tavan, kelepçe yok", 24 * time.Hour, 24 * time.Hour, false},
		{"24s+1sn — kelepçe başlar", 24*time.Hour + time.Second, 24 * time.Hour, true},
		{"7g — tavana iner", 7 * 24 * time.Hour, 24 * time.Hour, true},
		{"30g — tavana iner", 30 * 24 * time.Hour, 24 * time.Hour, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			from := to.Add(-c.span)
			gotFrom, gotTo, clamped := clampThanosWindow(from, to)

			if got := gotTo.Sub(gotFrom); got != c.wantSpan {
				t.Errorf("span = %v, want %v", got, c.wantSpan)
			}
			if clamped != c.wantClamped {
				t.Errorf("clamped = %v, want %v", clamped, c.wantClamped)
			}
			// ÇAPA KORUNUR — kelepçe pencereyi "şimdi"ye kaydırmaz.
			if !gotTo.Equal(to) {
				t.Errorf("to kaydı: %v, want %v (kelepçe SPAN'i sınırlar, çapayı değil)", gotTo, to)
			}
			if !clamped && !gotFrom.Equal(from) {
				t.Errorf("kelepçesiz çağrı from'u değiştirdi: %v, want %v", gotFrom, from)
			}
		})
	}
}

// Geçmişe bakan (brush'lanmış) pencere: çapa "şimdi" DEĞİL. Kelepçe o
// pencerenin SON 24 saatini vermeli — bugüne kaydırmamalı.
func TestClampThanosWindowPastAnchor(t *testing.T) {
	to := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) // aylar öncesi
	from := to.Add(-7 * 24 * time.Hour)

	gotFrom, gotTo, clamped := clampThanosWindow(from, to)
	if !clamped {
		t.Fatal("7g pencere kelepçelenmeliydi")
	}
	if !gotTo.Equal(to) {
		t.Errorf("to = %v, want %v — geçmiş pencere bugüne kaydırılamaz", gotTo, to)
	}
	if want := to.Add(-24 * time.Hour); !gotFrom.Equal(want) {
		t.Errorf("from = %v, want %v", gotFrom, want)
	}
}

// Tavan tek gövdeden okunur; sayı testte de kaynakta da elle yazılıp
// sürüklenmesin.
func TestThanosMaxWindowValue(t *testing.T) {
	if thanosMaxWindow != 24*time.Hour {
		t.Fatalf("thanosMaxWindow = %v, want 24h", thanosMaxWindow)
	}
}

// AYNALI KURAL, İKİ DİL — sürüklenme kapısı.
//
// Aynı tavan istemcide de yaşıyor (frontend/src/lib/thanosWindow.ts):
// panel BAŞLIKLARI kelepçeyi ifşa ediyor ve eksen o pencereye
// mıhlanıyor. İki taraf ayrışırsa sunucu 24h döndürürken başlık "son
// 6h" der ya da tersi — operatöre yalan söyleyen tam da bu ayrışma
// olurdu (v0.9.21 bu ifşayı DÜRÜSTLÜK için eklemişti).
//
// Kapı iki yönlü çalışsın diye TS dosyasından sayıyı ayrıştırıp
// karşılaştırıyoruz: hangi taraf değişirse değişsin test kırılır.
func TestThanosMaxWindowMatchesFrontend(t *testing.T) {
	const tsPath = "../../frontend/src/lib/thanosWindow.ts"
	raw, err := os.ReadFile(tsPath)
	if err != nil {
		t.Fatalf("istemci tavanı okunamadı (%s): %v", tsPath, err)
	}
	src := string(raw)

	// Yorumlar elenir: gerekçe metninde geçen "6h"/"24" sayıları
	// kapıyı yanıltmasın.
	src = regexp.MustCompile(`(?m)//.*$`).ReplaceAllString(src, "")
	src = regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(src, "")

	m := regexp.MustCompile(`THANOS_MAX_WINDOW_HOURS\s*=\s*(\d+)`).FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("THANOS_MAX_WINDOW_HOURS bulunamadı — istemci tavanı yeniden adlandırıldıysa bu kapı da güncellenmeli.\nkaynak:\n%s", thanosWindowFirstLines(src, 40))
	}
	wantHours := int(thanosMaxWindow / time.Hour)
	if m[1] != strconv.Itoa(wantHours) {
		t.Fatalf("istemci tavanı %s saat, sunucu %d saat — AYRIŞMA", m[1], wantHours)
	}
}

func thanosWindowFirstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
