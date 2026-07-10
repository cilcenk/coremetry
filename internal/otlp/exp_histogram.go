package otlp

import (
	"math"

	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

// exp_histogram.go — v0.8.435 (exemplar audit Faz D). OTLP exponential
// histograms carry buckets as (scale, offset, counts[]): bucket k spans
// (γ^k, γ^(k+1)] with γ = 2^(2^-scale). Instead of new exp_* columns
// (a distributed-column hazard class — v0.8.185/186), the buckets are
// materialized at ingest into the SAME explicit bound/count layout the
// v0.5.358 histogram path stores, so metric_points needs no schema
// change and every existing bucket consumer (percentileFromBuckets,
// cumulativeToDelta, heat/quantile reads) handles exp rows verbatim.
//
// Layout contract (mirrors chstore metrichist: counts has len(bounds)+1
// elements; counts[0] = (-inf, bounds[0]], counts[i] = (bounds[i-1],
// bounds[i]], last = overflow):
//   bounds  = [γ^o, γ^(o+1), …, γ^(o+n)]          (n+1 bounds)
//   counts  = [zeroCount, c0, …, c(n-1), 0]        (n+2 counts)
// zeroCount lands in the (-inf, γ^o] bucket — zeros and subnormals sit
// below the first positive bucket's lower edge by definition.

// maxExpBuckets caps the materialized bound count. OTel SDKs default to
// max 160 exponential buckets; 320 tolerates producers that widened it.
// Beyond the cap the row degrades to avg-only (empty arrays), same as a
// boundless explicit histogram.
const maxExpBuckets = 320

// expBucketsToExplicit converts one ExponentialHistogramDataPoint's
// positive+zero buckets to the explicit layout. ok=false (degrade to
// avg-only) when there is nothing to materialize, the bucket count
// exceeds the cap, the scale is outside the OTel-valid [-10, 20], or
// the point carries NEGATIVE buckets — duration/size metrics never have
// them, and folding a negative range into ascending positive bounds
// would misplace every quantile.
func expBucketsToExplicit(dp *metricspb.ExponentialHistogramDataPoint) ([]float64, []uint64, bool) {
	if dp == nil || dp.Positive == nil || len(dp.Positive.BucketCounts) == 0 {
		return nil, nil, false
	}
	if dp.Negative != nil && len(dp.Negative.BucketCounts) > 0 {
		return nil, nil, false
	}
	if dp.Scale < -10 || dp.Scale > 20 {
		return nil, nil, false
	}
	n := len(dp.Positive.BucketCounts)
	if n > maxExpBuckets {
		return nil, nil, false
	}

	// γ^k = 2^(2^-scale · k) — Exp2 keeps precision across the scale range.
	inv := math.Exp2(-float64(dp.Scale))
	pow := func(k int64) float64 { return math.Exp2(inv * float64(k)) }

	o := int64(dp.Positive.Offset)
	bounds := make([]float64, n+1)
	for i := 0; i <= n; i++ {
		bounds[i] = pow(o + int64(i))
	}
	counts := make([]uint64, n+2)
	counts[0] = dp.ZeroCount
	copy(counts[1:], dp.Positive.BucketCounts)
	// counts[n+1] stays 0 — the exp representation has no overflow bucket.
	return bounds, counts, true
}
