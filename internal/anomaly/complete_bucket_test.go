package anomaly

import (
	"testing"
	"time"
)

// v0.8.316 — regression: request_rate anomalies false-resolved at every
// bucket boundary. fetchBuckets included the STILL-FILLING 5m bucket, and
// metricValueExpr divides request_rate by a fixed 300s — one minute into a
// new bucket a genuine 100-rps spike (baseline 20) read as 100·60/300 =
// 20 rps ≈ baseline, z≈0, and the v0.8.220 fast-resolve closed the OPEN
// anomaly while the spike was still running (then a 3-bucket dwell blocked
// re-open for 15 minutes — a flap per bucket).
//
// Contract of lastCompleteBucketStart: the exclusive upper bound for the
// series read — the current bucket's START (UTC 5m grid, same as
// toStartOfInterval). Filtering time_bucket < bound keeps only COMPLETE
// buckets, so every sample was divided by the 300 seconds it truly spans.
func TestLastCompleteBucketStart(t *testing.T) {
	at := func(h, m, s int) time.Time {
		return time.Date(2026, 7, 6, h, m, s, 0, time.UTC)
	}
	cases := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{"mid-bucket excludes the filling bucket", at(10, 7, 13), at(10, 5, 0)},
		{"one second in still excludes it", at(10, 5, 1), at(10, 5, 0)},
		{"exact boundary: the just-closed bucket is complete", at(10, 5, 0), at(10, 5, 0)},
		{"last second of a bucket", at(10, 9, 59), at(10, 5, 0)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := lastCompleteBucketStart(c.now); !got.Equal(c.want) {
				t.Fatalf("lastCompleteBucketStart(%s) = %s, want %s", c.now, got, c.want)
			}
		})
	}
}
