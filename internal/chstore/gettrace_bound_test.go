package chstore

import (
	"testing"
	"time"
)

// v0.8.210 — GetTrace was `FROM spans WHERE trace_id = ?` with NO time bound:
// an unbounded full-partition fan-out across every shard. The fix derives the
// trace's window from trace_summary_5m (minMerge(trace_start_state) /
// maxMerge(trace_end_state), store.go:2132-2133) and bounds the spans scan to
// ~1-2 partitions. traceTimeBound is the pure piece: it widens a valid window
// and — critically — returns ok=false on a summary MISS so the caller falls
// back to the unbounded scan and a trace is never un-fetchable. Pin both.
func TestTraceTimeBound(t *testing.T) {
	base := time.Date(2026, 6, 29, 10, 0, 0, 0, time.UTC)
	const margin = 5 * time.Minute

	t.Run("valid window widens by margin", func(t *testing.T) {
		start := base
		end := base.Add(2 * time.Second)
		lo, hi, ok := traceTimeBound(start, end.UnixNano())
		if !ok {
			t.Fatal("expected ok for a valid window")
		}
		if !lo.Equal(start.Add(-margin)) {
			t.Fatalf("lo = %v, want %v", lo, start.Add(-margin))
		}
		if !hi.Equal(end.Add(margin)) {
			t.Fatalf("hi = %v, want %v", hi, end.Add(margin))
		}
	})

	t.Run("summary miss (endNanos 0) => unbounded fallback", func(t *testing.T) {
		if _, _, ok := traceTimeBound(time.Time{}, 0); ok {
			t.Fatal("endNanos 0 must yield ok=false (fall back to unbounded scan)")
		}
	})

	t.Run("negative endNanos => fallback", func(t *testing.T) {
		if _, _, ok := traceTimeBound(base, -1); ok {
			t.Fatal("negative endNanos must yield ok=false")
		}
	})

	t.Run("end before start => fallback (never bound to an empty/invalid window)", func(t *testing.T) {
		start := base
		end := base.Add(-1 * time.Hour)
		if _, _, ok := traceTimeBound(start, end.UnixNano()); ok {
			t.Fatal("end<start must yield ok=false so we don't bound to an inverted window")
		}
	})
}
