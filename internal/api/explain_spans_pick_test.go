package api

import (
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.9.462 (dürüstlük A6) — trace-explain'in span seçimi head-100 dilimi
// olmaktan çıktı: hatalı/en yavaş span'lar 100'ün dışındaysa model onları
// hiç görmüyor, "en yavaş span" kanıtı waterfall'da yanlış span'ı
// kutulayabiliyordu.
func TestPickExplainSpans(t *testing.T) {
	mk := func(i int, durNs int64, status string) chstore.SpanRow {
		return chstore.SpanRow{
			SpanID: string(rune('a'+i%26)) + string(rune('0'+i%10)),
			Name:   "sp", StartTime: int64(i) * 1_000_000, EndTime: int64(i)*1_000_000 + durNs,
			StatusCode: status,
		}
	}

	t.Run("küçük trace aynen", func(t *testing.T) {
		spans := []chstore.SpanRow{mk(0, 10, ""), mk(1, 20, "error")}
		if got := pickExplainSpans(spans, 100); len(got) != 2 {
			t.Fatalf("len = %d", len(got))
		}
	})

	t.Run("cap sonrası hata + en yavaş garantili, çıktı kronolojik", func(t *testing.T) {
		spans := make([]chstore.SpanRow, 0, 500)
		for i := 0; i < 500; i++ {
			st := ""
			dur := int64(1000)
			if i == 400 {
				st = "error" // head-100'ün çok dışında
			}
			if i == 450 {
				dur = 9_000_000_000 // en yavaş span, dipte
			}
			spans = append(spans, mk(i, dur, st))
		}
		got := pickExplainSpans(spans, 100)
		if len(got) != 100 {
			t.Fatalf("len = %d, want 100", len(got))
		}
		var hasErr, hasSlow bool
		lastStart := int64(-1)
		for _, sp := range got {
			if sp.StatusCode == "error" {
				hasErr = true
			}
			if sp.EndTime-sp.StartTime == 9_000_000_000 {
				hasSlow = true
			}
			if sp.StartTime < lastStart {
				t.Fatal("çıktı kronolojik değil")
			}
			lastStart = sp.StartTime
		}
		if !hasErr {
			t.Error("400. sıradaki hatalı span seçilmedi — eski head-100 davranışı")
		}
		if !hasSlow {
			t.Error("dipteki en yavaş span seçilmedi")
		}
	})

	t.Run("hata fırtınası dolguyu boğmaz (≤cap/2 hata)", func(t *testing.T) {
		spans := make([]chstore.SpanRow, 0, 300)
		for i := 0; i < 300; i++ {
			spans = append(spans, mk(i, 1000, "error"))
		}
		got := pickExplainSpans(spans, 100)
		if len(got) != 100 {
			t.Fatalf("len = %d", len(got))
		}
	})
}
