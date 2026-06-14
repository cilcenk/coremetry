package chstore

import (
	"strings"
	"testing"
)

// Perf engagement Phase-2 #1 (v0.8.32): the raw-spans span-metric path
// (QuerySpanMetric / QuerySpanMetricMulti, used by Explore's default
// latency/rate chart whenever a filter/DSL/sub-5min step disqualifies the
// service/operation MV fast-path) was the heaviest uncached read in the app.
// Two guards this test pins so the optimisation can't silently regress:
//
//  1. percentile aggregations use quantileTDigest, NOT exact quantile().
//     Exact quantile() buffers every value in memory — the CLAUDE.md
//     "/clickhouse-schema" anti-pattern past ~1M rows. TDigest is ≤2% error
//     at a fraction of the RAM and matches the approximate quantilesMerge the
//     MV path already serves (consistent p99 across surfaces). If someone
//     reverts p99 to exact quantile() at billion-row scale, this fails.
//  2. aggToSQL stays a strict whitelist (unknown agg → error) so the URL
//     `agg=` param can never inject SQL.
func TestAggToSQL_PercentilesUseTDigest(t *testing.T) {
	for _, agg := range []string{"p50", "p90", "p95", "p99", "p999"} {
		got, err := aggToSQL(agg, "(duration / 1e6)", 60)
		if err != nil {
			t.Fatalf("aggToSQL(%q) unexpected error: %v", agg, err)
		}
		if !strings.Contains(got, "quantileTDigest(") {
			t.Errorf("aggToSQL(%q) = %q; want quantileTDigest(...)", agg, got)
		}
		// Guard the exact reversion specifically: "quantile(0" is the
		// signature of exact quantile(0.99)(...) — must not reappear.
		if strings.Contains(got, "quantile(0") {
			t.Errorf("aggToSQL(%q) = %q; still uses exact quantile() — the anti-pattern", agg, got)
		}
	}
}

func TestAggToSQL_NonPercentileUnchanged(t *testing.T) {
	cases := map[string]string{
		"count":      "count()",
		"rate":       "count() / 60.0",
		"per_min":    "count() / 60.0 * 60.0", // Uptrace perMin (v0.8.x) — distinct raw expr from rate
		"error_rate": "countIf(status_code = 'error')",
		"apdex":      "<= 200.0", // Apdex T=200ms matched to the MV (store.go apdexT); v0.8.x
		"avg":        "avgOrNull",
	}
	for agg, want := range cases {
		got, err := aggToSQL(agg, "(duration / 1e6)", 60)
		if err != nil {
			t.Fatalf("aggToSQL(%q) unexpected error: %v", agg, err)
		}
		if !strings.Contains(got, want) {
			t.Errorf("aggToSQL(%q) = %q; want substring %q", agg, got, want)
		}
	}
}

func TestAggToSQL_RejectsUnknown(t *testing.T) {
	if _, err := aggToSQL("drop table users; --", "1", 60); err == nil {
		t.Fatal("aggToSQL accepted an unknown aggregation; the whitelist was breached")
	}
}
