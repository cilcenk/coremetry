package otlp

import (
	"math"
	"testing"

	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

// exp_histogram_test.go — v0.8.435 (exemplar audit Faz D). Pins the
// exp→explicit materialization: bounds math at multiple scales, the
// zero-count placement, the counts layout contract (len(bounds)+1),
// and every degrade branch (negative buckets, cap, scale range, empty).
func expDP(scale int32, offset int32, counts []uint64, zero uint64) *metricspb.ExponentialHistogramDataPoint {
	return &metricspb.ExponentialHistogramDataPoint{
		Scale:     scale,
		ZeroCount: zero,
		Positive:  &metricspb.ExponentialHistogramDataPoint_Buckets{Offset: offset, BucketCounts: counts},
	}
}

func almostEq(a, b float64) bool { return math.Abs(a-b) < 1e-9*math.Max(1, math.Abs(b)) }

func TestExpBucketsToExplicit(t *testing.T) {
	t.Run("scale 0 (γ=2), offset 0", func(t *testing.T) {
		bounds, counts, ok := expBucketsToExplicit(expDP(0, 0, []uint64{3, 5, 7}, 2))
		if !ok {
			t.Fatal("expected ok")
		}
		want := []float64{1, 2, 4, 8} // 2^0..2^3
		if len(bounds) != len(want) {
			t.Fatalf("bounds len %d, want %d", len(bounds), len(want))
		}
		for i := range want {
			if !almostEq(bounds[i], want[i]) {
				t.Fatalf("bounds[%d] = %v, want %v", i, bounds[i], want[i])
			}
		}
		wantCounts := []uint64{2, 3, 5, 7, 0} // zero + buckets + no overflow
		if len(counts) != len(bounds)+1 {
			t.Fatalf("layout contract broken: counts %d, bounds %d", len(counts), len(bounds))
		}
		for i, w := range wantCounts {
			if counts[i] != w {
				t.Fatalf("counts[%d] = %d, want %d", i, counts[i], w)
			}
		}
	})

	t.Run("scale 1 (γ=√2), negative offset → sub-1 bounds", func(t *testing.T) {
		bounds, _, ok := expBucketsToExplicit(expDP(1, -2, []uint64{1, 1}, 0))
		if !ok {
			t.Fatal("expected ok")
		}
		// γ = √2; bounds = γ^-2, γ^-1, γ^0 = 0.5, 0.7071…, 1
		if !almostEq(bounds[0], 0.5) || !almostEq(bounds[1], math.Sqrt2/2) || !almostEq(bounds[2], 1) {
			t.Fatalf("bounds = %v", bounds)
		}
	})

	t.Run("negative scale (γ=4) coarse buckets", func(t *testing.T) {
		bounds, _, ok := expBucketsToExplicit(expDP(-1, 1, []uint64{9}, 0))
		if !ok {
			t.Fatal("expected ok")
		}
		if !almostEq(bounds[0], 4) || !almostEq(bounds[1], 16) {
			t.Fatalf("bounds = %v", bounds)
		}
	})

	t.Run("degrade: negative buckets present", func(t *testing.T) {
		dp := expDP(0, 0, []uint64{1}, 0)
		dp.Negative = &metricspb.ExponentialHistogramDataPoint_Buckets{BucketCounts: []uint64{1}}
		if _, _, ok := expBucketsToExplicit(dp); ok {
			t.Fatal("negative buckets must degrade to avg-only")
		}
	})

	t.Run("degrade: over the bucket cap", func(t *testing.T) {
		if _, _, ok := expBucketsToExplicit(expDP(0, 0, make([]uint64, maxExpBuckets+1), 0)); ok {
			t.Fatal("over-cap must degrade")
		}
	})

	t.Run("degrade: scale out of OTel range", func(t *testing.T) {
		if _, _, ok := expBucketsToExplicit(expDP(21, 0, []uint64{1}, 0)); ok {
			t.Fatal("scale 21 must degrade")
		}
		if _, _, ok := expBucketsToExplicit(expDP(-11, 0, []uint64{1}, 0)); ok {
			t.Fatal("scale -11 must degrade")
		}
	})

	t.Run("degrade: no positive buckets (zero-only)", func(t *testing.T) {
		if _, _, ok := expBucketsToExplicit(expDP(0, 0, nil, 5)); ok {
			t.Fatal("zero-only point has nothing to bound")
		}
		if _, _, ok := expBucketsToExplicit(nil); ok {
			t.Fatal("nil dp must degrade")
		}
	})
}

// TestConvertExpHistogramFidelity — the convert arm end to end: min/max
// no longer literal 0,0; temporality set; bounds land on the row.
func TestConvertExpHistogramFidelity(t *testing.T) {
	mn, mx, sum := 0.5, 7.5, 30.0
	req := metricsRequest(&metricspb.Metric{
		Name: "rpc.latency",
		Data: &metricspb.Metric_ExponentialHistogram{
			ExponentialHistogram: &metricspb.ExponentialHistogram{
				AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA,
				DataPoints: []*metricspb.ExponentialHistogramDataPoint{{
					Count: 10, Sum: &sum, Min: &mn, Max: &mx,
					TimeUnixNano: 1,
					Scale:        0, ZeroCount: 1,
					Positive: &metricspb.ExponentialHistogramDataPoint_Buckets{Offset: 0, BucketCounts: []uint64{4, 5}},
				}},
			},
		},
	})
	points, _ := ConvertMetrics(req)
	if len(points) != 1 {
		t.Fatalf("points = %d, want 1", len(points))
	}
	p := points[0]
	if p.MinValue != mn || p.MaxValue != mx {
		t.Fatalf("min/max = %v/%v, want %v/%v (were literal 0,0 pre-v0.8.435)", p.MinValue, p.MaxValue, mn, mx)
	}
	if p.Temporality != "delta" {
		t.Fatalf("temporality = %q, want delta (was never set pre-v0.8.435)", p.Temporality)
	}
	if len(p.BucketBounds) != 3 || len(p.BucketCounts) != 4 {
		t.Fatalf("bounds/counts = %d/%d, want 3/4", len(p.BucketBounds), len(p.BucketCounts))
	}
}
