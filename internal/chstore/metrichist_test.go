package chstore

import (
	"reflect"
	"testing"
)

// v0.6.56 — histogram-viz read-path helpers, TDD'd before the query
// orchestration that uses them. percentileFromBuckets does read-time
// quantile estimation from an explicit OTel histogram; bucketDeltas /
// cumulativeToDelta turn a cumulative-temporality bucket series into
// per-interval counts so a heatmap doesn't grow monotonically to the
// right (the whole reason temporality is captured at ingest).
//
// Unit-mixing rule (CLAUDE.md + memory feedback-unit-mixing-needs-both-
// branches): a value+unit / multi-branch helper MUST exercise EVERY
// branch at ship time. Here that means every percentile edge (empty,
// overflow +Inf bucket, single bound, boundary, interpolation) AND both
// temporalities (delta vs cumulative vs unknown).

func almostEq(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 1e-6
}

func TestPercentileFromBuckets(t *testing.T) {
	// counts has len(bounds)+1 elements: counts[i] = observations in
	// bucket i, where bucket 0 = (-Inf, bounds[0]], bucket i =
	// (bounds[i-1], bounds[i]], and the last = (bounds[N-1], +Inf].
	tests := []struct {
		name   string
		bounds []float64
		counts []uint64
		p      float64
		want   float64
	}{
		{"p50 lands on the bucket boundary",
			[]float64{10, 20, 30}, []uint64{0, 5, 5, 0}, 0.5, 20},
		{"p90 interpolates inside the holding bucket",
			[]float64{10, 20, 30}, []uint64{0, 5, 5, 0}, 0.9, 28},
		{"p100 clamps to the last finite bound",
			[]float64{10, 20, 30}, []uint64{0, 5, 5, 0}, 1.0, 30},
		{"empty histogram returns 0",
			[]float64{10, 20}, []uint64{0, 0, 0}, 0.5, 0},
		{"no bounds returns 0",
			[]float64{}, []uint64{}, 0.5, 0},
		{"overflow +Inf bucket clamps to last finite bound",
			[]float64{10, 20}, []uint64{0, 0, 10}, 0.5, 20},
		{"first bucket interpolates up from 0",
			[]float64{10}, []uint64{3, 7}, 0.1, 10.0 / 3.0},
		{"high percentile in the overflow bucket clamps",
			[]float64{10}, []uint64{3, 7}, 0.5, 10},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := percentileFromBuckets(tc.bounds, tc.counts, tc.p)
			if !almostEq(got, tc.want) {
				t.Fatalf("percentileFromBuckets(%v, %v, %v) = %v, want %v",
					tc.bounds, tc.counts, tc.p, got, tc.want)
			}
		})
	}
}

func TestCumulativeToDelta(t *testing.T) {
	tests := []struct {
		name string
		in   [][]uint64
		want [][]uint64
	}{
		{"first point is a zero baseline",
			[][]uint64{{5, 10}}, [][]uint64{{0, 0}}},
		{"steady increase deltas per bucket",
			[][]uint64{{5, 10}, {8, 14}, {10, 20}},
			[][]uint64{{0, 0}, {3, 4}, {2, 6}}},
		{"counter reset uses the full value as the delta",
			[][]uint64{{5, 10}, {2, 3}},
			[][]uint64{{0, 0}, {2, 3}}},
		{"bucket-layout change is treated as a reset",
			[][]uint64{{5, 10}, {8, 14, 2}},
			[][]uint64{{0, 0}, {8, 14, 2}}},
		{"empty series",
			[][]uint64{}, [][]uint64{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := cumulativeToDelta(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("cumulativeToDelta(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestBucketDeltasBothBranches(t *testing.T) {
	cum := [][]uint64{{3}, {7}}
	if got := bucketDeltas("cumulative", cum); !reflect.DeepEqual(got, [][]uint64{{0}, {4}}) {
		t.Fatalf("cumulative branch: got %v, want [[0] [4]]", got)
	}
	if got := bucketDeltas("delta", cum); !reflect.DeepEqual(got, cum) {
		t.Fatalf("delta branch must pass through unchanged: got %v", got)
	}
	// Unknown/unspecified temporality is delta-assumed (best-effort) so we
	// never mis-delta a series we couldn't classify.
	if got := bucketDeltas("", cum); !reflect.DeepEqual(got, cum) {
		t.Fatalf("unknown branch must pass through unchanged: got %v", got)
	}
}
