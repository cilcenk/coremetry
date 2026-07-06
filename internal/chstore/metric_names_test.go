package chstore

import (
	"testing"
	"time"
)

// v0.8.311 — operator-reported: GET /api/metrics/names took ~30s and the
// /metrics list never loaded, because ListMetricNames scanned ALL of
// metric_points with an unbounded GROUP BY metric. The fix bounds the
// query to metricNameLookback via buildMetricNamesWhere. This table pins
// the load-bearing invariant — the `time >= ?` predicate is ALWAYS the
// first WHERE term, so partition pruning (PARTITION BY toDate(time))
// always applies — plus the service/pattern/wildcard handling, so a
// future edit can't silently regress to the full-history scan.
func TestBuildMetricNamesWhere(t *testing.T) {
	since := time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		service   string
		pattern   string
		wantConds []string
		wantLike  string // expected trailing ILIKE arg; "" = no ILIKE term
	}{
		{
			name:      "no filters still time-bounds",
			wantConds: []string{"time >= ?"},
		},
		{
			name:      "service only",
			service:   "payments-api",
			wantConds: []string{"time >= ?", "service_name = ?"},
		},
		{
			name:      "bare pattern → substring",
			pattern:   "http",
			wantConds: []string{"time >= ?", "metric ILIKE ?"},
			wantLike:  "%http%",
		},
		{
			name:      "star wildcard translated",
			pattern:   "http.*.count",
			wantConds: []string{"time >= ?", "metric ILIKE ?"},
			wantLike:  "http.%.count",
		},
		{
			name:      "question wildcard translated",
			pattern:   "cpu_?",
			wantConds: []string{"time >= ?", "metric ILIKE ?"},
			wantLike:  "cpu__",
		},
		{
			name:      "service + pattern, order stable",
			service:   "web-bff",
			pattern:   "latency",
			wantConds: []string{"time >= ?", "service_name = ?", "metric ILIKE ?"},
			wantLike:  "%latency%",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wc := buildMetricNamesWhere(tt.service, tt.pattern, since)
			if len(wc.conds) != len(tt.wantConds) {
				t.Fatalf("conds = %v, want %v", wc.conds, tt.wantConds)
			}
			for i := range tt.wantConds {
				if wc.conds[i] != tt.wantConds[i] {
					t.Errorf("cond[%d] = %q, want %q", i, wc.conds[i], tt.wantConds[i])
				}
			}
			// Regression guard: the time bound MUST be the first predicate
			// and carry `since` as the first arg — nothing else guarantees
			// partition pruning stays in front of the scan.
			if len(wc.conds) == 0 || wc.conds[0] != "time >= ?" {
				t.Fatalf("first cond must be the time bound, got %v", wc.conds)
			}
			if len(wc.args) == 0 || wc.args[0] != since {
				t.Fatalf("first arg must be `since` (%v), got %v", since, wc.args)
			}
			if tt.wantLike != "" {
				if last := wc.args[len(wc.args)-1]; last != tt.wantLike {
					t.Errorf("ILIKE arg = %v, want %q", last, tt.wantLike)
				}
			}
		})
	}
}
