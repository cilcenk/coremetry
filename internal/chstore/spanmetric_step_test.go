// spanmetric_step_test.go — v0.9.391 (grafik-audit Faz B) regression.
// clampSpanMetricStep: batch/tekil span-metric okumalarının tek step
// kapısı. Pinler: (1) 90g + step=0 → seri başına ≤2000 nokta (eski sabit
// ladder 3600s'te takılıp pencereyle sınırsız büyüyordu), (2) explicit
// step=1 + 7g → bütçe tavanı (eskiden 604.800 bucket hedefleyip LIMIT
// 50000'in KEYFÎ satır kesmesine düşüyordu), (3) mdp px-adaptif yol,
// (4) mdp=0 kısa pencerelerde ESKİ ladder birebir korunur.
package chstore

import (
	"testing"
	"time"
)

func TestClampSpanMetricStep(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	win := func(d time.Duration) (time.Time, time.Time) { return now.Add(-d), now }

	cases := []struct {
		name     string
		dur      time.Duration
		step     int
		mdp      int
		wantStep int
	}{
		// (4) mdp=0, step=0 → eski ladder BİREBİR (davranış koruması).
		{"10m eski ladder", 10 * time.Minute, 0, 0, 10},
		{"1h eski ladder", time.Hour, 0, 0, 30},
		{"6h eski ladder", 6 * time.Hour, 0, 0, 60},
		{"24h eski ladder", 24 * time.Hour, 0, 0, 300},
		{"7g eski ladder", 7 * 24 * time.Hour, 0, 0, 1800},

		// (1) 90g + step=0 + mdp=0: eski ladder 3600 verirdi → 2160 nokta.
		// Bütçe tavanı 2000 → ladder'da 3600'ün üstüne (7200) çıkar.
		{"90g bütçe tavanı", 90 * 24 * time.Hour, 0, 0, 7200},

		// (2) explicit step=1 + 7g: 604.800 bucket → bütçe 2000 → ≥303s,
		// ladder'a yukarı snap (600).
		{"7g step=1 kelepçesi", 7 * 24 * time.Hour, 1, 0, 600},

		// (3) mdp px-adaptif: 24h + mdp=1200 → ideal 72s → ladder 120.
		{"24h mdp=1200", 24 * time.Hour, 0, 1200, 120},
		// dar panel: 24h + mdp=200 → ideal 432 → ladder 600.
		{"24h mdp=200", 24 * time.Hour, 0, 200, 600},

		// explicit step bütçeye sığıyorsa DOKUNULMAZ.
		{"1h step=30 aynen", time.Hour, 30, 0, 30},
	}
	for _, c := range cases {
		from, to := win(c.dur)
		got := clampSpanMetricStep(c.step, from, to, c.mdp)
		if got != c.wantStep {
			t.Errorf("%s: step=%d, want %d", c.name, got, c.wantStep)
		}
		// Evrensel değişmez: nokta sayısı bütçeyi aşamaz.
		budget := c.mdp
		if budget <= 0 {
			budget = 2000
		}
		if pts := int(c.dur.Seconds()) / got; pts > budget {
			t.Errorf("%s: %d nokta > bütçe %d", c.name, pts, budget)
		}
	}
}

// v0.9.407 notu — tek-tuple tdigest: ölçüm kanıtı commit gövdesinde
// (AYRI 175ms/8MB → TEK-alias 84ms/4MB; inline-özdeş 190ms — CH
// farklı-paramlı state'leri CSE'lemez). SQL üretimi bu dosyada pinli
// DEĞİL: Multi'nin SELECT montajı dışa açık değil ve sırf pin için
// kancalamak montajı karmaşıklaştırır; doğrulama gemiye alım sırasında
// canlı e2e ile yapıldı (batch p50/p95/p99 ↔ CH tuple sorgusu birebir).
// Montaj ileride dışa açılırsa rollupREDSQL deseninde pinlenecek.
