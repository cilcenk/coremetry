package api

import (
	"testing"

	"github.com/cilcenk/coremetry/internal/logstore"
)

func logRec(ts int64, svc, body string) *logstore.LogRecord {
	return &logstore.LogRecord{Timestamp: ts, ServiceName: svc, Body: body}
}

// v0.7.15 — the live-log tailer's cursor must never re-emit a row nor drop a
// new one. selectFreshLogs returns only un-emitted rows (strictly newer than
// the cursor, or at the cursor ns but not in the boundary dedup set);
// advanceLogCursor moves the cursor to the newest ts and tracks the content
// hashes at that ns so a same-ns row that arrives on a later tick (ES refresh
// lag delivers it late) isn't re-sent. A regression here would duplicate or
// drop tailed log lines — exactly what the SSE rewrite must not do.
func TestLogTailCursor(t *testing.T) {
	empty := map[uint64]struct{}{}

	// Round 1: cursor seeded at 100, boundary empty. Rows: 90 (old → drop),
	// 100 (== cursor, never emitted → fresh), 110, 120.
	rows1 := []*logstore.LogRecord{
		logRec(90, "a", "old"), logRec(100, "a", "at"),
		logRec(110, "a", "x"), logRec(120, "a", "y"),
	}
	fresh1 := selectFreshLogs(rows1, 100, empty)
	if len(fresh1) != 3 {
		t.Fatalf("round1 fresh=%d, want 3 (100,110,120)", len(fresh1))
	}
	cur1, bound1 := advanceLogCursor(fresh1, 100, empty)
	if cur1 != 120 || len(bound1) != 1 {
		t.Fatalf("round1 cursor=%d boundary=%d, want 120/1", cur1, len(bound1))
	}

	// Round 2: the same window re-returns already-seen rows — nothing fresh.
	rows2 := []*logstore.LogRecord{logRec(110, "a", "x"), logRec(120, "a", "y")}
	if got := selectFreshLogs(rows2, cur1, bound1); len(got) != 0 {
		t.Fatalf("round2 fresh=%d, want 0 (no dupes)", len(got))
	}

	// Round 3: a NEW row lands at the SAME ns as the cursor (late ES refresh).
	// Different content hash → fresh; cursor unchanged; boundary carries over + grows.
	rows3 := []*logstore.LogRecord{logRec(120, "a", "y"), logRec(120, "a", "z-late")}
	fresh3 := selectFreshLogs(rows3, cur1, bound1)
	if len(fresh3) != 1 || fresh3[0].Body != "z-late" {
		t.Fatalf("round3 fresh=%v, want [z-late]", fresh3)
	}
	cur3, bound3 := advanceLogCursor(fresh3, cur1, bound1)
	if cur3 != 120 || len(bound3) != 2 {
		t.Fatalf("round3 cursor=%d boundary=%d, want 120/2 (carry-over + new)", cur3, len(bound3))
	}

	// Round 4: cursor advances past the boundary → boundary resets to the new max.
	rows4 := []*logstore.LogRecord{logRec(120, "a", "z-late"), logRec(130, "a", "w")}
	fresh4 := selectFreshLogs(rows4, cur3, bound3)
	if len(fresh4) != 1 || fresh4[0].Body != "w" {
		t.Fatalf("round4 fresh=%v, want [w]", fresh4)
	}
	cur4, bound4 := advanceLogCursor(fresh4, cur3, bound3)
	if cur4 != 130 || len(bound4) != 1 {
		t.Fatalf("round4 cursor=%d boundary=%d, want 130/1 (reset)", cur4, len(bound4))
	}
}
