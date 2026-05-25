package chstore

import (
	"strings"
	"testing"
	"time"
)

// v0.5.440 — locks the narrow service-scope semantics of
// GetTraces. The bug history:
//
//   • v0.5.371 widened `WHERE service_name = ?` to
//     `trace_id GLOBAL IN (SELECT DISTINCT trace_id FROM
//     spans WHERE service_name = ?)` so a cross-service
//     search like "POST /payment" would find the route
//     even when service X's span name was just the verb
//     and service Y's span name held the path.
//
//   • The side-effect: every trace where ANY service
//     called service X was returned for `service=X`,
//     including traces where the operator never directly
//     wanted X — they wanted X's OWN traces. Operator-
//     reported as "Tempo's semantic is just X's traces."
//
//   • v0.5.372 broadened the search HAVING to do
//     arrayExists() over attr_values, so X's own client
//     span attrs (url.path / http.target) supply the
//     cross-service route match — making the trace_id
//     subquery widening unnecessary.
//
//   • v0.5.440 reverted to plain `service_name = ?` and
//     dropped the subquery branch. These tests guard
//     against a re-widening: any reintroduction of
//     `trace_id IN (SELECT ... FROM spans` for the
//     Service-set / Search-set combination fails here.

func TestBuildGetTracesWhere_v0_5_440_NarrowServiceScope(t *testing.T) {
	from := time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC)
	to := from.Add(1 * time.Hour)
	f := TraceFilter{Service: "checkout", Search: "POST /payment", From: from, To: to}

	wc := buildGetTracesWhere(f)
	sql := wc.sql()

	if !strings.Contains(sql, "service_name = ?") {
		t.Fatalf("expected direct `service_name = ?` predicate; got:\n%s", sql)
	}
	// Either GLOBAL IN or plain IN reintroducing the subquery is a regression.
	if strings.Contains(sql, "trace_id IN (SELECT") ||
		strings.Contains(sql, "trace_id GLOBAL IN (SELECT") {
		t.Fatalf("v0.5.440 regression: WHERE widened back to trace_id-subquery shape; got:\n%s", sql)
	}
	// The subquery used to read from spans inside the IN — verify no
	// `FROM spans` literal sneaks in via any future re-widening even
	// if the operator chose a different connector keyword.
	if strings.Contains(sql, "FROM spans") {
		t.Fatalf("v0.5.440 regression: subquery `FROM spans` leaked into outer WHERE; got:\n%s", sql)
	}
}

func TestBuildGetTracesWhere_RequireServicesUsesINList(t *testing.T) {
	// Adjacent invariant: RequireServices stays as a flat IN list,
	// not a subquery. The trace-level participant check happens via
	// HAVING countIf(service_name = ?) > 0 downstream — the WHERE
	// only narrows the rows to scan.
	f := TraceFilter{
		RequireServices: []string{"checkout", "payments"},
		From:            time.Now().Add(-1 * time.Hour),
		To:              time.Now(),
	}
	wc := buildGetTracesWhere(f)
	sql := wc.sql()

	if !strings.Contains(sql, "service_name IN (?,?)") {
		t.Fatalf("expected `service_name IN (?,?)` for RequireServices; got:\n%s", sql)
	}
	if strings.Contains(sql, "SELECT") {
		t.Fatalf("RequireServices should not pull in a SELECT subquery; got:\n%s", sql)
	}
}

func TestBuildGetTracesWhere_EmptyFilter(t *testing.T) {
	// No predicates → empty WHERE. Downstream callers expect
	// `wc.sql()` to be the empty string here so the assembled
	// query collapses to `SELECT ... FROM spans` with no WHERE
	// (the caller adds time bounds upstream when needed).
	empty := buildGetTracesWhere(TraceFilter{})
	if got := empty.sql(); got != "" {
		t.Fatalf("expected empty WHERE for empty filter; got %q", got)
	}
}

func TestBuildGetTracesWhere_TimeBoundsAlignTo5Min(t *testing.T) {
	// v0.5.356 — alignment guard. From is truncated to the 5-minute
	// boundary when the requested window is > 5 minutes so traces
	// living in the bucket-overlap region surface (consistent with
	// operation_summary_5m's read semantics from v0.5.299).
	from := time.Date(2026, 5, 25, 12, 7, 33, 0, time.UTC) // not aligned
	to := from.Add(1 * time.Hour)
	wc := buildGetTracesWhere(TraceFilter{From: from, To: to})

	// First arg is the aligned `from` — minute should be a multiple of 5.
	if len(wc.args) < 1 {
		t.Fatalf("expected at least the time arg; got %v", wc.args)
	}
	aligned, ok := wc.args[0].(time.Time)
	if !ok {
		t.Fatalf("expected first arg to be time.Time; got %T", wc.args[0])
	}
	if aligned.Minute()%5 != 0 || aligned.Second() != 0 {
		t.Fatalf("v0.5.356 regression: From not truncated to 5min; got %v", aligned)
	}
}

func TestBuildGetTracesWhere_TraceIDLengthDispatch(t *testing.T) {
	// 32-char hex → exact equality; shorter → prefix match. Bloom
	// filter index on trace_id makes both efficient, but the
	// distinction matters for the operator pasting a partial ID.
	full := "0123456789abcdef0123456789abcdef"
	wcFull := buildGetTracesWhere(TraceFilter{TraceID: full})
	if sqlFull := wcFull.sql(); !strings.Contains(sqlFull, "trace_id = ?") {
		t.Fatalf("32-char trace ID should use equality; got:\n%s", sqlFull)
	}

	wcPrefix := buildGetTracesWhere(TraceFilter{TraceID: "0123abcd"})
	if sqlPrefix := wcPrefix.sql(); !strings.Contains(sqlPrefix, "startsWith(trace_id, ?)") {
		t.Fatalf("partial trace ID should use startsWith; got:\n%s", sqlPrefix)
	}
}
