package mcptools

import (
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.9.1050 (Faz 0.10) regresyon pini — get_trace gövdesi SINIRLI.
// GetTrace 50k span'e dek döndürebilir; kırpma hata-koruyandır (v0.9.414
// dersi: düz head-cap derindeki hata span'ini düşürür) ve sonuç
// kronolojik kalır. truncated bayrağı modele "tamamını görmedin" der.
func TestCapTraceSpans(t *testing.T) {
	mk := func(id string, start int64, durMs float64, status string) chstore.SpanRow {
		return chstore.SpanRow{SpanID: id, StartTime: start, DurationMs: durMs, StatusCode: status}
	}

	t.Run("tavan altında aynen, truncated=false", func(t *testing.T) {
		in := []chstore.SpanRow{mk("a", 1, 5, "ok"), mk("b", 2, 9, "error")}
		out, tr := capTraceSpans(in, 200)
		if tr || len(out) != 2 {
			t.Fatalf("got truncated=%v len=%d, want false/2", tr, len(out))
		}
	})

	t.Run("derindeki hata span'i kırpmada YAŞAR, sıra kronolojik", func(t *testing.T) {
		var in []chstore.SpanRow
		for i := 0; i < 10; i++ {
			in = append(in, mk(string(rune('a'+i)), int64(i), float64(100-i), "ok"))
		}
		in = append(in, mk("deep-err", 99, 0.1, "error")) // en sonda, en kısa
		out, tr := capTraceSpans(in, 4)
		if !tr || len(out) != 4 {
			t.Fatalf("got truncated=%v len=%d, want true/4", tr, len(out))
		}
		foundErr := false
		for i, sp := range out {
			if sp.SpanID == "deep-err" {
				foundErr = true
			}
			if i > 0 && out[i-1].StartTime > sp.StartTime {
				t.Fatalf("kronolojik değil: %v", out)
			}
		}
		if !foundErr {
			t.Fatalf("derindeki hata span'i düştü (v0.9.414 sınıfı): %v", out)
		}
	})

	t.Run("dolgu en YAVAŞ span'lerden", func(t *testing.T) {
		in := []chstore.SpanRow{
			mk("fast", 1, 1, "ok"), mk("slow", 2, 500, "ok"),
			mk("mid", 3, 50, "ok"), mk("err", 4, 2, "error"),
		}
		out, _ := capTraceSpans(in, 2)
		ids := map[string]bool{}
		for _, sp := range out {
			ids[sp.SpanID] = true
		}
		if !ids["err"] || !ids["slow"] {
			t.Fatalf("beklenen {err, slow}, got %v", out)
		}
	})
}
