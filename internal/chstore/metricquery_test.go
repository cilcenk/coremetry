package chstore

import (
	"strings"
	"testing"
	"time"
)

// v0.8.4 (scale-audit) — the metric_points GROUP BY scan behind the
// Grafana-style MetricQueryEditor / MetricsExplorer must satisfy the
// CLAUDE.md CH-bounds hard constraint: LIMIT + SETTINGS max_execution_time +
// a time-bounded WHERE that prunes partitions. Pre-v0.8.4 the query had LIMIT
// but NO max_execution_time (unlike its QueryMetricHistogram twin) and the
// time bound was CONDITIONAL — absent on a from/to-less call, so a degenerate
// request scanned every partition unbounded. These assertions pin all three
// guards across agg/groupBy shapes and the zero-window default so they can't
// re-regress.

// timeArgs collects the time.Time bound args from a query arg slice.
func timeArgs(args []any) []time.Time {
	var out []time.Time
	for _, a := range args {
		if tm, ok := a.(time.Time); ok {
			out = append(out, tm)
		}
	}
	return out
}

func TestBuildMetricQuerySQL_CHBounds(t *testing.T) {
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	from := now.Add(-1 * time.Hour)

	cases := []struct {
		name      string
		f         MetricQueryFilter
		zeroWindow bool
	}{
		{"ungrouped avg, explicit window", MetricQueryFilter{Name: "http.server.duration", Aggregation: "avg", From: from, To: now}, false},
		{"grouped p99, explicit window", MetricQueryFilter{Name: "http.server.duration", Aggregation: "p99", GroupBy: []string{"http.method"}, From: from, To: now}, false},
		{"sum, NO window (defaults 24h)", MetricQueryFilter{Name: "db.client.duration", Aggregation: "sum"}, true},
	}

	// Every shape must carry all three CH-bounds guards.
	mustContain := []string{
		"FROM metric_points",
		"time >= ?",
		"time <= ?",
		"GROUP BY bucket, gk",
		"LIMIT 50000",
		"SETTINGS max_execution_time = 30",
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sql, args, err := buildMetricQuerySQL(tc.f, now)
			if err != nil {
				t.Fatalf("buildMetricQuerySQL: %v", err)
			}
			for _, want := range mustContain {
				if !strings.Contains(sql, want) {
					t.Errorf("SQL missing %q (CH-bounds guard regressed)\n--- SQL ---\n%s", want, sql)
				}
			}
			// A time-bounded WHERE must always exist — both bounds present
			// as args even when the caller passed no window.
			times := timeArgs(args)
			if len(times) != 2 {
				t.Fatalf("want exactly 2 time bound args (from,to), got %d", len(times))
			}
			if tc.zeroWindow {
				// Defaulted: To == now, window == 24h (so the clause prunes).
				to := times[1]
				fromArg := times[0]
				if !to.Equal(now) {
					t.Errorf("zero-window To = %v, want now %v", to, now)
				}
				if d := to.Sub(fromArg); d != 24*time.Hour {
					t.Errorf("zero-window span = %v, want 24h (degenerate call must self-bound)", d)
				}
			}
		})
	}
}

func TestBuildMetricQuerySQL_BadAgg(t *testing.T) {
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	_, _, err := buildMetricQuerySQL(MetricQueryFilter{Name: "m", Aggregation: "nope"}, now)
	if err == nil {
		t.Fatal("want error for unknown aggregation, got nil")
	}
}
