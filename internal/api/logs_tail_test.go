package api

import (
	"testing"

	"github.com/cilcenk/coremetry/internal/logstore"
)

// tailStep carries the two load-bearing invariants of the live-tail SSE
// cursor (v0.8.x): NEVER silently drop a row (a saturated batch must raise
// gap), and ALWAYS make forward progress (even when >cap rows share one
// nanosecond, the cursor must advance). This table exercises every branch.

func rec(ts, id int64) *logstore.LogRecord { return &logstore.LogRecord{Timestamp: ts, ID: id} }

func ids(rs []*logstore.LogRecord) []int64 {
	out := make([]int64, len(rs))
	for i, r := range rs {
		out[i] = r.ID
	}
	return out
}

func TestTailStep(t *testing.T) {
	const cap = 3
	tests := []struct {
		name      string
		since     int64
		boundary  []int64 // ids already emitted at `since`
		rows      []*logstore.LogRecord
		wantEmit  []int64
		wantSince int64
		wantBound []int64 // ids in the returned boundary set
		wantGap   bool
	}{
		{
			name:      "empty batch — cursor holds, nothing emitted",
			since:     100,
			rows:      nil,
			wantEmit:  []int64{},
			wantSince: 100,
			wantBound: []int64{},
		},
		{
			name:      "normal forward — all emitted, advance to maxTs",
			since:     100,
			rows:      []*logstore.LogRecord{rec(101, 1), rec(102, 2), rec(103, 3)},
			wantEmit:  []int64{1, 2, 3},
			wantSince: 103,
			wantBound: []int64{3}, // ids at the new max ns
			wantGap:   true,       // len==cap → gap (more may exist past 103)
		},
		{
			name:      "boundary dedup — skip already-emitted same-ns id",
			since:     100,
			boundary:  []int64{1},
			rows:      []*logstore.LogRecord{rec(100, 1), rec(100, 2), rec(105, 3)},
			wantEmit:  []int64{2, 3}, // id 1 at ns 100 already sent
			wantSince: 105,
			wantBound: []int64{3},
			wantGap:   true, // 3 rows == cap → saturated read, gap raised
		},
		{
			name:      "quiet re-read — boundary row not re-emitted, cursor holds",
			since:     100,
			boundary:  []int64{1},
			rows:      []*logstore.LogRecord{rec(100, 1)},
			wantEmit:  []int64{},
			wantSince: 100,
			wantBound: []int64{1},
		},
		{
			name:      "late same-ns ingest — emitted once, added to boundary",
			since:     100,
			boundary:  []int64{1},
			rows:      []*logstore.LogRecord{rec(100, 1), rec(100, 2)}, // id2 arrived late
			wantEmit:  []int64{2},
			wantSince: 100,
			wantBound: []int64{1, 2},
		},
		{
			name:      "saturation at one ns — gap + escape forward (no infinite loop)",
			since:     100,
			rows:      []*logstore.LogRecord{rec(100, 1), rec(100, 2), rec(100, 3)}, // all at 100, len==cap
			wantEmit:  []int64{1, 2, 3},
			wantSince: 101, // stepped past the saturated ns
			wantBound: []int64{},
			wantGap:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := map[int64]struct{}{}
			for _, id := range tc.boundary {
				b[id] = struct{}{}
			}
			emit, next, nb, gap := tailStep(tc.since, b, tc.rows, cap)

			if got := ids(emit); !sameSet(got, tc.wantEmit) {
				t.Errorf("emit ids = %v, want %v", got, tc.wantEmit)
			}
			if next != tc.wantSince {
				t.Errorf("nextSince = %d, want %d", next, tc.wantSince)
			}
			gotBound := make([]int64, 0, len(nb))
			for id := range nb {
				gotBound = append(gotBound, id)
			}
			if !sameSet(gotBound, tc.wantBound) {
				t.Errorf("boundary = %v, want %v", gotBound, tc.wantBound)
			}
			if gap != tc.wantGap {
				t.Errorf("gap = %v, want %v", gap, tc.wantGap)
			}
			// Forward-progress invariant: the cursor never goes backward.
			if next < tc.since {
				t.Errorf("cursor went BACKWARD: %d < %d", next, tc.since)
			}
		})
	}
}

// sameSet compares two int64 slices as multisets-of-distinct (order-free).
func sameSet(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[int64]int{}
	for _, x := range a {
		m[x]++
	}
	for _, x := range b {
		m[x]--
	}
	for _, v := range m {
		if v != 0 {
			return false
		}
	}
	return true
}
