package mcptools

import (
	"os"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.9.1136 (AI Faz 3.1, K7) — get_trace tool'u 200 span'de kapılıyken
// coremetry://trace/{trace_id} RESOURCE'u GetTrace'i TAVANSIZ
// döndürüyordu (LIMIT 50000): aynı veri için tool kapıda, URI yan
// kapısı serbest. Artık iki yol da traceBodyPayload'dan geçiyor.

func mkSpan(id string, start int64, durMs float64, status string) chstore.SpanRow {
	return chstore.SpanRow{SpanID: id, StartTime: start, DurationMs: durMs, StatusCode: status}
}

func TestTraceBodyPayloadCapAndTruncatedFlag(t *testing.T) {
	cases := []struct {
		name          string
		spans         int
		wantSpans     int
		wantTruncated bool
	}{
		{"boş iz", 0, 0, false},
		{"tavan altı", 5, 5, false},
		{"tam tavan", getTraceSpanCap, getTraceSpanCap, false},
		{"tavan+1", getTraceSpanCap + 1, getTraceSpanCap, true},
		{"çok büyük iz", 5000, getTraceSpanCap, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := make([]chstore.SpanRow, 0, tc.spans)
			for i := 0; i < tc.spans; i++ {
				in = append(in, mkSpan("s", int64(i), float64(i%97), "ok"))
			}
			out := traceBodyPayload("abc123", in)

			if got := out["trace_id"]; got != "abc123" {
				t.Errorf("trace_id=%v", got)
			}
			spans, ok := out["spans"].([]chstore.SpanRow)
			if !ok {
				t.Fatalf("spans şekli yanlış: %T", out["spans"])
			}
			if len(spans) != tc.wantSpans {
				t.Errorf("span sayısı %d, want %d", len(spans), tc.wantSpans)
			}
			if got := out["span_count"].(int); got != tc.wantSpans {
				t.Errorf("span_count=%d, want %d", got, tc.wantSpans)
			}
			// total_span_count GERÇEK boyutu taşır — model "tamamını
			// gördüm" diyemesin.
			if got := out["total_span_count"].(int); got != tc.spans {
				t.Errorf("total_span_count=%d, want %d", got, tc.spans)
			}
			if got := out["truncated"].(bool); got != tc.wantTruncated {
				t.Errorf("truncated=%v, want %v", got, tc.wantTruncated)
			}
		})
	}
}

// Kırpma hata-koruyan kalır (v0.9.414 dersi) — resource yolu da aynı
// üreticiden geçtiği için bu garanti ikisinde de geçerli.
func TestTraceBodyPayloadKeepsErrorSpansWhenTruncating(t *testing.T) {
	in := make([]chstore.SpanRow, 0, getTraceSpanCap+50)
	for i := 0; i < getTraceSpanCap+49; i++ {
		in = append(in, mkSpan("ok", int64(i), 10, "ok"))
	}
	in = append(in, mkSpan("deep-err", 99999, 0.1, "error"))

	out := traceBodyPayload("t", in)
	spans := out["spans"].([]chstore.SpanRow)
	found := false
	for _, sp := range spans {
		if sp.SpanID == "deep-err" {
			found = true
		}
	}
	if !found {
		t.Fatal("derindeki hata span'i kırpmada düştü (v0.9.414 sınıfı)")
	}
}

// Kaynak pini — trace RESOURCE'u ham GetTrace çıktısını doğrudan
// dönmesin; ortak üreticiden geçsin. Bu regresyon (tool kapıda,
// resource serbest) tam olarak böyle doğdu.
func TestTraceResourceUsesSharedPayloadBuilder(t *testing.T) {
	b, err := os.ReadFile("tools.go")
	if err != nil {
		t.Fatalf("tools.go okunamadı: %v", err)
	}
	src := string(b)
	start := strings.Index(src, `const traceTpl`)
	if start < 0 {
		t.Fatal("traceTpl bloğu bulunamadı")
	}
	block := src[start:]
	if end := strings.Index(block, "\n}\n"); end > 0 {
		block = block[:end]
	}
	if !strings.Contains(block, "traceBodyPayload(") {
		t.Error("trace resource'u traceBodyPayload'dan GEÇMİYOR — tavan yalnız tool tarafında kalmış")
	}
	if strings.Contains(block, `"span_count": len(spans)`) {
		t.Error("trace resource'u ham span listesini dönüyor (tavansız yan kapı geri geldi)")
	}
}
