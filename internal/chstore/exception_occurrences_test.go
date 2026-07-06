package chstore

import (
	"strings"
	"testing"
	"time"
)

// Regression guard for v0.8.312 (operator-reported): the occurrences query
// crashed the external Distributed CH with `code 43 ... toUnixTimestamp64Nano:
// Expected DateTime64, got DateTime`. toStartOfInterval on a second-grain
// INTERVAL yields a DateTime (not DateTime64) on the prod spans schema, so
// wrapping the bucket in toUnixTimestamp64Nano — which only accepts
// DateTime64 — errored. Local monolithic CH yields DateTime64 from the same
// expression, so it never reproduced there. The fix returns the bare
// toStartOfInterval bucket and converts to unix-ns in Go via
// time.Time.UnixNano() (mirroring GetSpanBreakdown), which is type-agnostic.
//
// This pins the SQL *shape* — the repro lived in prod-only schema we can't
// exercise locally, so we assert the query never re-grows the type-fragile
// wrapper and never loses its raw-spans bounds.
func TestOccurrencesQuery_TypeSafeBucketAndBounds(t *testing.T) {
	q := occurrencesQuery(occurrenceBucketCap, "max_threads = 4")

	if strings.Contains(q, "toUnixTimestamp64Nano") {
		t.Errorf("occurrences query wraps the bucket in toUnixTimestamp64Nano; "+
			"that rejects the DateTime toStartOfInterval yields on the external "+
			"Distributed schema (code 43). Scan toStartOfInterval as time.Time "+
			"instead.\nquery:\n%s", q)
	}
	if !strings.Contains(q, "toStartOfInterval(time, INTERVAL ? SECOND) AS bucket") {
		t.Errorf("occurrences query no longer selects the epoch-aligned "+
			"toStartOfInterval bucket:\n%s", q)
	}
	// Raw-spans hard constraint: LIMIT + max_execution_time + indexed
	// time-bounded WHERE must all be present.
	for _, must := range []string{
		"service_name = ?",
		"time >= ?",
		"time <= ?",
		"LIMIT ",
		"max_execution_time",
	} {
		if !strings.Contains(q, must) {
			t.Errorf("occurrences query missing required clause %q:\n%s", must, q)
		}
	}
	// The runtime-interpolated bits land where expected.
	if !strings.Contains(q, "LIMIT 5000") {
		t.Errorf("bucketCap not interpolated into LIMIT:\n%s", q)
	}
	if !strings.Contains(q, "max_threads = 4") {
		t.Errorf("shardSkip setting not appended:\n%s", q)
	}
}

// Regression coverage for the v0.8.309 occurrences-over-time fix. The old
// panel bucketed the 100 newest samples client-side across the whole
// [firstSeen,lastSeen] window, so any busy group rendered as a single
// right-edge spike. fillOccurrenceBuckets is the pure core of the
// replacement: it turns the sparse SQL COUNT into a dense, epoch-aligned,
// gap-filled series that matches CH's toStartOfInterval bucket starts.
//
// Alignment MUST agree with toStartOfInterval(time, INTERVAL stepSec
// SECOND) for every rung bucketForWindow can return (10/30/60/300/1800/
// 3600s) — a drift on any rung leaves a bucket the SQL filled with no
// Go slot (or vice-versa), silently dropping or misplacing counts.

const sec = int64(time.Second)

func TestFillOccurrenceBuckets_ExactAndGapFilled(t *testing.T) {
	tests := []struct {
		name    string
		fromNs  int64
		toNs    int64
		stepSec int64
		counts  map[int64]uint64
		want    []OccurrencePoint
	}{
		{
			name:    "10s step, aligned window, gaps zero-filled",
			fromNs:  200 * sec,
			toNs:    240 * sec,
			stepSec: 10,
			counts:  map[int64]uint64{210 * sec: 3, 240 * sec: 1},
			want: []OccurrencePoint{
				{Time: 200 * sec, Count: 0},
				{Time: 210 * sec, Count: 3},
				{Time: 220 * sec, Count: 0},
				{Time: 230 * sec, Count: 0},
				{Time: 240 * sec, Count: 1},
			},
		},
		{
			name:    "from not aligned floors to the epoch boundary",
			fromNs:  205 * sec, // floors to 200s, matching toStartOfInterval
			toNs:    225 * sec,
			stepSec: 10,
			counts:  map[int64]uint64{220 * sec: 7},
			want: []OccurrencePoint{
				{Time: 200 * sec, Count: 0},
				{Time: 210 * sec, Count: 0},
				{Time: 220 * sec, Count: 7},
			},
		},
		{
			name:    "degenerate window (single occurrence) yields one bucket",
			fromNs:  200 * sec,
			toNs:    200 * sec,
			stepSec: 10,
			counts:  map[int64]uint64{200 * sec: 1},
			want:    []OccurrencePoint{{Time: 200 * sec, Count: 1}},
		},
		{
			name:    "invalid step returns nil",
			fromNs:  200 * sec,
			toNs:    240 * sec,
			stepSec: 0,
			counts:  nil,
			want:    nil,
		},
		{
			name:    "inverted window returns nil",
			fromNs:  240 * sec,
			toNs:    200 * sec,
			stepSec: 10,
			counts:  nil,
			want:    nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fillOccurrenceBuckets(tt.fromNs, tt.toNs, tt.stepSec, tt.counts)
			if len(got) != len(tt.want) {
				t.Fatalf("len = %d, want %d (got %+v)", len(got), len(tt.want), got)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("bucket %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// Invariants that must hold for EVERY rung bucketForWindow returns — the
// off-axis rungs (minutes/hours) are where alignment bugs hide.
func TestFillOccurrenceBuckets_RungInvariants(t *testing.T) {
	base := int64(1_700_000_000) * sec // a real 2023-era unix ns timestamp
	for _, stepSec := range []int64{10, 30, 60, 300, 1800, 3600} {
		stepNs := stepSec * sec
		fromNs := base + 123*sec // deliberately unaligned to the rung
		toNs := fromNs + stepNs*40 + 7*sec
		// Seed a count in the middle bucket so we can prove it's preserved.
		midBucket := ((fromNs+stepNs*20)/stepNs)*stepNs
		out := fillOccurrenceBuckets(fromNs, toNs, stepSec, map[int64]uint64{midBucket: 9})
		if len(out) == 0 {
			t.Fatalf("step=%ds: empty series", stepSec)
		}
		// First bucket floor-aligned and at/before from.
		if out[0].Time%stepNs != 0 {
			t.Errorf("step=%ds: first bucket %d not aligned to %d", stepSec, out[0].Time, stepNs)
		}
		if out[0].Time > fromNs {
			t.Errorf("step=%ds: first bucket %d is after from %d", stepSec, out[0].Time, fromNs)
		}
		// Uniform gap; series covers `to`.
		for i := 1; i < len(out); i++ {
			if out[i].Time-out[i-1].Time != stepNs {
				t.Fatalf("step=%ds: gap at %d is %d, want %d", stepSec, i, out[i].Time-out[i-1].Time, stepNs)
			}
		}
		if last := out[len(out)-1].Time; last < toNs-stepNs {
			t.Errorf("step=%ds: last bucket %d does not reach to %d", stepSec, last, toNs)
		}
		// Seeded count preserved exactly once, everything else zero.
		var total uint64
		for _, p := range out {
			total += p.Count
		}
		if total != 9 {
			t.Errorf("step=%ds: total count = %d, want 9 (seed lost or duplicated)", stepSec, total)
		}
	}
}
