package chstore

import (
	"strings"
	"testing"
)

// v0.8.357 — regression: /traces at 30-60m windows loaded for seconds
// (operator-reported). The no-service MV fast path skipped Stage 1 and
// computed EVERY merge state (6× argMax/min/max/count + quantiles) for
// EVERY trace in the window just to keep the top 50 — linear in window
// volume, minutes at prod scale. Contract of traceStage1LightSQL: the
// id-select carries ONE sort aggregate (a plain max(time_bucket) for the
// default time sort — no merge state at all) plus only the states active
// filters need; heavy root_* states never appear; service/operation
// sorts refuse (ok=false) since their sort key IS the heavy state.
func TestTraceStage1LightSQL(t *testing.T) {
	t.Run("default time sort uses plain column max, no merge states", func(t *testing.T) {
		sql, ok := traceStage1LightSQL(TraceFilter{Sort: "time"}, nil)
		if !ok {
			t.Fatal("time sort must take the light path")
		}
		if !strings.Contains(sql, "max(time_bucket)") {
			t.Fatalf("want plain max(time_bucket) sort, got:\n%s", sql)
		}
		for _, heavy := range []string{"argMaxIfMerge", "root_name_state", "quantile"} {
			if strings.Contains(sql, heavy) {
				t.Fatalf("light stage must not touch %s:\n%s", heavy, sql)
			}
		}
		if !strings.Contains(sql, "max_execution_time") || !strings.Contains(sql, "LIMIT ?") {
			t.Fatalf("stage 1 must stay bounded:\n%s", sql)
		}
	})

	t.Run("empty sort behaves as time", func(t *testing.T) {
		sql, ok := traceStage1LightSQL(TraceFilter{}, nil)
		if !ok || !strings.Contains(sql, "max(time_bucket)") {
			t.Fatalf("empty sort must default to the light time path:\n%s", sql)
		}
	})

	t.Run("duration sort carries exactly its two states", func(t *testing.T) {
		sql, ok := traceStage1LightSQL(TraceFilter{Sort: "duration"}, nil)
		if !ok || !strings.Contains(sql, "maxMerge(trace_end_state)") {
			t.Fatalf("duration light path wrong:\n%s", sql)
		}
		if strings.Contains(sql, "argMaxIfMerge") {
			t.Fatalf("duration sort must not pull root states:\n%s", sql)
		}
	})

	t.Run("active filters ride the HAVING so top-N ids are correct", func(t *testing.T) {
		having := []string{"countMerge(error_count_state) > 0"}
		sql, ok := traceStage1LightSQL(TraceFilter{Sort: "time"}, having)
		if !ok || !strings.Contains(sql, "HAVING countMerge(error_count_state) > 0") {
			t.Fatalf("having must be pushed into stage 1:\n%s", sql)
		}
	})

	t.Run("service and operation sorts refuse the light path", func(t *testing.T) {
		for _, sort := range []string{"service", "operation"} {
			if _, ok := traceStage1LightSQL(TraceFilter{Sort: sort}, nil); ok {
				t.Fatalf("sort=%s must fall back to single-stage (its key IS the heavy state)", sort)
			}
		}
	})

	t.Run("asc order propagates", func(t *testing.T) {
		sql, _ := traceStage1LightSQL(TraceFilter{Sort: "time", Order: "asc"}, nil)
		if !strings.Contains(sql, "ASC") {
			t.Fatalf("order asc must reach the sort:\n%s", sql)
		}
	})
}
