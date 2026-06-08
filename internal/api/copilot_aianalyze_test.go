package api

import (
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// TestAggRED pins the span-weighted bucket aggregation: totals sum, error rate
// is errors/spans, percentiles are span-weighted means, rate is spans/window.
func TestAggRED(t *testing.T) {
	rows := []chstore.ServiceSummaryRow{
		{SpanCount: 100, ErrorCount: 10, P50Ms: 50, P95Ms: 200, P99Ms: 400, AvgMs: 80},
		{SpanCount: 300, ErrorCount: 30, P50Ms: 70, P95Ms: 300, P99Ms: 600, AvgMs: 100},
	}
	got := aggRED(rows, 60) // 400 spans over 60s
	if got.Spans != 400 || got.ErrorCount != 40 {
		t.Fatalf("totals: spans=%d errors=%d, want 400/40", got.Spans, got.ErrorCount)
	}
	if got.ErrorRate != 10 { // 40/400
		t.Errorf("errorRate=%.2f want 10", got.ErrorRate)
	}
	// span-weighted p99 = (100*400 + 300*600)/400 = 550
	if got.P99Ms != 550 {
		t.Errorf("p99=%.1f want 550 (span-weighted)", got.P99Ms)
	}
	// rate = 400/60 ≈ 6.667
	if got.Rate < 6.66 || got.Rate > 6.67 {
		t.Errorf("rate=%.3f want ~6.667", got.Rate)
	}
}

func TestAggREDEmpty(t *testing.T) {
	got := aggRED(nil, 60)
	if got.Spans != 0 || got.ErrorRate != 0 || got.Rate != 0 {
		t.Errorf("empty agg should be zero, got %+v", got)
	}
}

// TestParseServiceAnalysis covers the tolerant JSON extraction: bare JSON,
// code-fenced JSON, and surrounding prose; plus a non-JSON miss → nil.
func TestParseServiceAnalysis(t *testing.T) {
	good := `{"ozet":"x","olasi_neden":"y","kanit":["a","b"],"oneriler":["c"],"guven":"orta"}`
	cases := []struct {
		name, in string
		nilWant  bool
	}{
		{"bare", good, false},
		{"fenced", "```json\n" + good + "\n```", false},
		{"prose-wrapped", "İşte analiz:\n" + good + "\nUmarım yardımcı olur.", false},
		{"no-json", "model refused to answer", true},
		{"empty", "", true},
	}
	for _, c := range cases {
		got := parseServiceAnalysis(c.in)
		if c.nilWant && got != nil {
			t.Errorf("%s: want nil, got %+v", c.name, got)
		}
		if !c.nilWant {
			if got == nil {
				t.Errorf("%s: want parsed, got nil", c.name)
			} else if got.Guven != "orta" || len(got.Kanit) != 2 {
				t.Errorf("%s: fields wrong: %+v", c.name, got)
			}
		}
	}
}

// TestPostCheckServiceAnalysis verifies hallucination detection: a service name
// not present in the gathered context is flagged; known services + technical
// hyphenated terms are not.
func TestPostCheckServiceAnalysis(t *testing.T) {
	cx := &aiServiceContext{
		Service:    "payment-service",
		Downstream: []string{"ledger-service"},
		Upstream:   []string{"mobile-bff"},
	}
	// Mentions a known downstream + a technical term → verified.
	clean := &serviceAnalysis{
		Ozet:       "payment-service bozuldu",
		OlasiNeden: "ledger-service çağrılarında timeout",
		Kanit:      []string{"error-rate %0.4 → %8.3", "p99 artışı"},
		Oneriler:   []string{"ledger-service DB havuzunu incele"},
	}
	if pc := postCheckServiceAnalysis(clean, cx); !pc.Verified || len(pc.UnknownServices) != 0 {
		t.Errorf("clean analysis should verify, got %+v", pc)
	}
	// Invents "fraud-detector" which is not in the context → flagged.
	halluc := &serviceAnalysis{
		Ozet:       "sorun fraud-detector kaynaklı",
		OlasiNeden: "auth-gateway yavaş",
		Kanit:      []string{"error-rate yüksek"},
		Oneriler:   []string{"x"},
	}
	pc := postCheckServiceAnalysis(halluc, cx)
	if pc.Verified {
		t.Error("hallucinated services should fail verification")
	}
	found := map[string]bool{}
	for _, u := range pc.UnknownServices {
		found[u] = true
	}
	if !found["fraud-detector"] || !found["auth-gateway"] {
		t.Errorf("expected fraud-detector + auth-gateway flagged, got %v", pc.UnknownServices)
	}
	// error-rate is a technical term, must NOT be flagged.
	if found["error-rate"] {
		t.Error("error-rate is a technical term, must not be flagged as a service")
	}
}
