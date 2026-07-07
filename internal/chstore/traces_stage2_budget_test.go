package chstore

import "testing"

// v0.8.363 — self-telemetry (coremetry-monolithic error span) caught
// Stage 2 of the two-stage /traces read failing with ClickHouse code
// 62 "Syntax error at position 262126": clickhouse-go inlines bind
// args client-side, so a large Stage-1 id budget produced a
// trace_id IN (...) list that crossed the server's max_query_size
// parser budget (default 262144) — exactly the v0.8.357 light path
// under a deep offset or a large page. These tests pin the byte-safe
// budget rule.
func TestTraceStage1Budget(t *testing.T) {
	cases := []struct {
		name       string
		offset     int
		pageLimit  int
		stage1     int
		wantBudget int
		wantOK     bool
	}{
		{"default page floors at stage1Limit", 0, 51, 510, 510, true},
		{"deep offset grows the budget", 1000, 51, 510, 2102, true},
		{"budget clamps at the IN-list cap", 2800, 201, 2010, traceStage2MaxIDs, true},
		{"need past the cap falls back to single-stage", 5900, 201, 2010, 0, false},
		{"exactly at the cap still runs two-stage", traceStage2MaxIDs - 51, 51, 510, traceStage2MaxIDs, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := traceStage1Budget(c.offset, c.pageLimit, c.stage1)
			if ok != c.wantOK || got != c.wantBudget {
				t.Fatalf("traceStage1Budget(%d,%d,%d) = (%d,%v), want (%d,%v)",
					c.offset, c.pageLimit, c.stage1, got, ok, c.wantBudget, c.wantOK)
			}
			// Whatever the inputs, an accepted budget must keep the
			// inlined IN list inside the CH parser budget: ~35 bytes
			// per id ('<32 hex>', + comma) + 4 KiB statement slack.
			if ok && got*35+4096 >= 262144 {
				t.Fatalf("budget %d would inline %d bytes — past max_query_size", got, got*35)
			}
		})
	}
}
