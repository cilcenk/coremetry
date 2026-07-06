package evaluator

import "testing"

// v0.8.314 — regression: traffic-drop alerts silently suppressed. The
// MinSamples gate exists so RATIO metrics (error_rate) and latency
// percentiles don't fire on statistical noise from a handful of spans.
// request_rate is ABSOLUTE and inherently sample-aware — the call-site
// comment says so — but metricNeedsSampleFloor caught it via the generic
// "_rate" suffix. Failure: a `request_rate < 10` outage rule with
// MinSamples=100 is skipped EXACTLY when traffic collapses (low traffic ⇒
// low sample count ⇒ gate trips ⇒ eval skipped + breach stamp wiped), so
// the total-traffic-loss alert never opens.
func TestMetricNeedsSampleFloor(t *testing.T) {
	cases := []struct {
		metric string
		want   bool
	}{
		{"request_rate", false}, // absolute — the v0.8.314 bug
		{"error_count", false},  // absolute — already exempt, keep pinned
		{"error_rate", true},    // ratio — noise-prone at low samples
		{"avg_ms", true},
		{"p50_ms", true},
		{"p95_ms", true},
		{"p99_ms", true},
		{"custom_thing_rate", true}, // unknown ratios keep the conservative gate
		{"custom_thing_ms", true},
		{"anomaly_ratio", false},
	}
	for _, c := range cases {
		t.Run(c.metric, func(t *testing.T) {
			if got := metricNeedsSampleFloor(c.metric); got != c.want {
				t.Fatalf("metricNeedsSampleFloor(%q) = %v, want %v", c.metric, got, c.want)
			}
		})
	}
}
