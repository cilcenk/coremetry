package chstore

import (
	"strings"
	"testing"
	"time"
)

// FAZ 2 (docs/audit/traces-attribute-columns.md §6) — two-phase trace-list
// regression tests. The bug history: with an attribute column toggled on,
//
//   - the RAW path inlined the extras projection into the window-wide
//     GROUP BY trace_id, decompressing the four fat attr/res array columns
//     for EVERY span in the window before LIMIT (measured 6.97× read_bytes),
//   - the MV path's fillTraceExtras ran `trace_id IN (…)` with NO time bound
//     and NO LIMIT, so the idx_trace bloom sieved the granules of the WHOLE
//     retention (partition pruning impossible).
//
// The fix makes the raw list narrow by construction and gives the single
// common phase-2 (traceExtrasSQL) a time-bounded WHERE + LIMIT. These tests
// pin all of it as pure-function SQL assertions.

// (1) The raw-path list query must NEVER inline attribute projections again.
func TestBuildGetTracesListSQL_NoInlineExtras(t *testing.T) {
	from := time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)
	wc := buildGetTracesWhere(TraceFilter{From: from, To: from.Add(time.Hour)})
	sql := buildGetTracesListSQL(wc.sql(), "", "trace_start", "DESC")

	for _, needle := range []string{"attr_values", "res_values", "attr_keys", "res_keys", "indexOf("} {
		if strings.Contains(sql, needle) {
			t.Fatalf("FAZ 2 regression: raw list query touches %q (extras inlined again); got:\n%s", needle, sql)
		}
	}
	// Sanity: it is still the full 7-column narrow summary with its bounds.
	for _, needle := range []string{"GROUP BY trace_id", "LIMIT ? OFFSET ?", "max_execution_time"} {
		if !strings.Contains(sql, needle) {
			t.Fatalf("raw list query lost %q; got:\n%s", needle, sql)
		}
	}
}

// (2) The phase-2 extras query must carry a time-bounded WHERE + LIMIT
// (the pre-FAZ-2 shape had neither — the CLAUDE.md spans-rule violation).
func TestTraceExtrasSQL_TimeBoundAndLimit(t *testing.T) {
	sql, projArgs := traceExtrasSQL(3, []string{"CHANNEL_CODE", "FUNCTION_CODE"})

	if !strings.Contains(sql, "time >= ? AND time <= ?") {
		t.Fatalf("FAZ 2 regression: phase-2 lost its time bound; got:\n%s", sql)
	}
	if !strings.Contains(sql, "trace_id IN (?,?,?)") {
		t.Fatalf("expected a 3-id IN list; got:\n%s", sql)
	}
	if !strings.Contains(sql, "LIMIT 3") {
		t.Fatalf("FAZ 2 regression: phase-2 lost its LIMIT safety belt; got:\n%s", sql)
	}
	if !strings.Contains(sql, "max_execution_time = 15") {
		t.Fatalf("phase-2 lost max_execution_time; got:\n%s", sql)
	}
	// Custom keys are neither materialized nor well-known → the generic
	// array path with 4 bind args per key.
	if got, want := len(projArgs), 8; got != want {
		t.Fatalf("expected %d projection args for 2 custom keys, got %d", want, got)
	}
}

// (3) Projection tiers: materialized native column when mapped, semconv
// structured column via WellKnownTraceCol, array access otherwise.
func TestTraceExtrasProjection_MaterializedVsArray(t *testing.T) {
	// Empty map (the shipped state) → custom key uses the array path.
	sel, args := traceExtrasProjection([]string{"CHANNEL_CODE"})
	if !strings.Contains(sel, "attr_values[indexOf(attr_keys, ?)]") {
		t.Fatalf("custom key without a materialized column should use the array path; got:\n%s", sel)
	}
	if len(args) != 4 {
		t.Fatalf("array path binds 4 args per key, got %d", len(args))
	}

	// Well-known semconv key → its dedicated structured column, no arrays.
	sel, args = traceExtrasProjection([]string{"http.method"})
	if !strings.Contains(sel, "any(http_method)") || strings.Contains(sel, "attr_values") {
		t.Fatalf("semconv key should read the structured column only; got:\n%s", sel)
	}
	if len(args) != 0 {
		t.Fatalf("structured-column path binds no args, got %d", len(args))
	}

	// Materialized map populated (FAZ 2C applied) → native column wins,
	// no array access, no binds. Restore the package map afterwards.
	traceAttrMaterialized["CHANNEL_CODE"] = "attr_channel_code"
	defer delete(traceAttrMaterialized, "CHANNEL_CODE")
	sel, args = traceExtrasProjection([]string{"CHANNEL_CODE", "FUNCTION_CODE"})
	if !strings.Contains(sel, "anyIf(attr_channel_code, attr_channel_code != '') AS extra_0") {
		t.Fatalf("materialized key should read its native column; got:\n%s", sel)
	}
	if strings.Contains(sel, "indexOf(attr_keys, ?)], ''),nullIf(res_values") &&
		!strings.Contains(sel, "extra_1") {
		t.Fatalf("unmapped sibling key lost its array projection; got:\n%s", sel)
	}
	// Only FUNCTION_CODE (unmapped) binds args.
	if len(args) != 4 {
		t.Fatalf("expected 4 binds (unmapped key only), got %d", len(args))
	}
	if args[0] != "FUNCTION_CODE" {
		t.Fatalf("expected FUNCTION_CODE binds, got %v", args)
	}
}

// (2b) The export path can hand phase-2 up to 50k ids; each chunk's
// inlined IN-list must stay under the CH parser budget (max_query_size
// 256 KiB at ~35 bytes/id — the v0.8.363 syntax-error class).
func TestTraceExtrasChunkIDs_UnderParserBudget(t *testing.T) {
	if traceExtrasChunkIDs <= 0 {
		t.Fatal("chunk size must be positive")
	}
	if bytes := traceExtrasChunkIDs * 35; bytes >= 262144-4096 {
		t.Fatalf("chunk of %d ids ≈ %d bytes crowds the 256KiB max_query_size budget", traceExtrasChunkIDs, bytes)
	}
}

// (4) Phase-2 bounds: min(trace_start) .. max(trace_start+dur), and the
// window helper pads the upper bound by exactly the +5m slack (a trace's
// last span can start after the recorded end — late spans / MV staleness).
func TestTraceExtrasBounds_AndSlack(t *testing.T) {
	t0 := time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)
	rows := []TraceRow{
		{TraceID: "a", StartTime: t0.Add(10 * time.Minute).UnixNano(), DurationMs: 250},
		{TraceID: "b", StartTime: t0.UnixNano(), DurationMs: 1000},                                   // earliest start
		{TraceID: "c", StartTime: t0.Add(5 * time.Minute).UnixNano(), DurationMs: 20 * 60 * 1000},   // latest end (25m)
	}
	from, to := traceExtrasBounds(rows)
	if !from.Equal(t0) {
		t.Fatalf("from should be the earliest trace_start %v, got %v", t0, from)
	}
	wantTo := t0.Add(25 * time.Minute)
	if !to.Equal(wantTo) {
		t.Fatalf("to should be the latest trace end %v, got %v", wantTo, to)
	}

	wf, wt := traceExtrasWindow(from, to)
	if !wf.Equal(from) {
		t.Fatalf("lower bound must stay exact (no slack), got %v", wf)
	}
	if got, want := wt.Sub(to), 5*time.Minute; got != want {
		t.Fatalf("upper-bound slack: want +%v, got +%v", want, got)
	}
}
