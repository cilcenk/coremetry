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

// trimTopNByArea (v0.8.x payload trim): /api/spans/metric on a high-card
// groupBy used to return every series (~2.5MB / thousands of lines) even though
// PanelStack only ever renders the top ≤TOP_N_MAX by area. The server now caps
// to spanMetricTopN by the SAME area metric (sum of abs(point value)) the
// frontend ranks by, so the kept set is a superset of anything displayed and
// the wire payload is bounded. This pins:
//   1. no-op below the cap (returns the input untouched, total == len),
//   2. the cap keeps exactly N and reports the true pre-trim total,
//   3. the kept set is the HIGHEST-area series (so it's a superset of the
//      frontend's top-≤N selection — displayed lines stay identical),
//   4. boundary (== cap) is NOT trimmed.
func mkSeries(key string, vals ...float64) SpanMetricSeries {
	pts := make([]SpanMetricPoint, len(vals))
	for i, v := range vals {
		pts[i] = SpanMetricPoint{Time: int64(i), Value: v}
	}
	return SpanMetricSeries{GroupKey: []string{key}, Points: pts}
}

func TestTrimTopNByArea(t *testing.T) {
	t.Run("below cap is untouched", func(t *testing.T) {
		in := []SpanMetricSeries{mkSeries("a", 1, 2), mkSeries("b", 3)}
		got, total := trimTopNByArea(in, 5)
		if total != 2 {
			t.Errorf("total = %d; want 2", total)
		}
		if len(got) != 2 {
			t.Fatalf("len(got) = %d; want 2 (no trim below cap)", len(got))
		}
	})

	t.Run("at cap is untouched", func(t *testing.T) {
		in := []SpanMetricSeries{mkSeries("a", 1), mkSeries("b", 2), mkSeries("c", 3)}
		got, total := trimTopNByArea(in, 3)
		if total != 3 || len(got) != 3 {
			t.Fatalf("got len=%d total=%d; want len=3 total=3 at the boundary", len(got), total)
		}
		// Boundary must preserve ORIGINAL order (no sort triggered).
		if got[0].GroupKey[0] != "a" || got[2].GroupKey[0] != "c" {
			t.Errorf("boundary reordered the series: %v", []string{got[0].GroupKey[0], got[1].GroupKey[0], got[2].GroupKey[0]})
		}
	})

	t.Run("above cap keeps highest-area and reports pre-trim total", func(t *testing.T) {
		// Areas: a=1, b=10, c=2, d=100, e=3. Top-2 by area = d, b.
		in := []SpanMetricSeries{
			mkSeries("a", 1),
			mkSeries("b", -6, 4), // |−6|+|4| = 10
			mkSeries("c", 2),
			mkSeries("d", 100),
			mkSeries("e", 1, 1, 1), // 3
		}
		got, total := trimTopNByArea(in, 2)
		if total != 5 {
			t.Errorf("total = %d; want 5 (pre-trim count for an accurate +N more)", total)
		}
		if len(got) != 2 {
			t.Fatalf("len(got) = %d; want 2 (capped)", len(got))
		}
		if got[0].GroupKey[0] != "d" || got[1].GroupKey[0] != "b" {
			t.Errorf("kept = [%s %s]; want [d b] (top-2 by area, desc)", got[0].GroupKey[0], got[1].GroupKey[0])
		}
	})

	t.Run("equal-area ties keep stable input order", func(t *testing.T) {
		in := []SpanMetricSeries{mkSeries("x", 5), mkSeries("y", 5), mkSeries("z", 5)}
		got, total := trimTopNByArea(in, 2)
		if total != 3 {
			t.Errorf("total = %d; want 3", total)
		}
		if got[0].GroupKey[0] != "x" || got[1].GroupKey[0] != "y" {
			t.Errorf("tie order = [%s %s]; want [x y] (stable)", got[0].GroupKey[0], got[1].GroupKey[0])
		}
	})
}
