package chstore

import (
	"context"
	"fmt"
	"time"
)

// LatencyHeatmap is a 2D histogram of span counts bucketed by
// (time, log-scale duration). Honeycomb's signature
// visualisation — the eye reads "where is the population
// dense" and "where are the outliers" instantly because each
// pixel = one (time, latency) cell, not an aggregated line.
//
// Bucket grid:
//   • Time axis  — N buckets across the requested window
//                  (caller picks N; default 60).
//   • Latency axis — log10(ms) with sub-decade granularity
//                    (4 sub-bins per decade by default → 7
//                    decades = 28 bins covering 0.1ms → 1Ms).
//
// The wire format avoids a sparse map: we send Times[],
// DurationBins[], and Counts[][] (rows × cols) so the frontend
// can render a fixed grid without lookups. Empty cells = 0.
type LatencyHeatmap struct {
	// Time bucket starts (unix nanoseconds) — len = N time buckets.
	Times []int64 `json:"times"`
	// Duration bin upper bounds in ms (e.g. 1, 1.78, 3.16, 5.62, 10, …).
	// len = M latency bins. Counts[i][j] is the span count for
	// the time bucket i and the latency bin whose upper bound
	// is DurationBins[j].
	DurationBins []float64 `json:"durationBins"`
	// Counts[time_idx][dur_idx] = span count in that cell.
	Counts [][]uint32 `json:"counts"`
	// MaxCount = peak cell value, useful for the frontend to
	// pick a colour scale without a full re-scan.
	MaxCount uint32 `json:"maxCount"`
	// SamplingRate (v0.5.238) — fraction of trace IDs read to
	// produce this heatmap. 1.0 = full pass; <1.0 = sampled. UI
	// surfaces a small "sampled at 10%" tag when this drops below
	// 1 so the operator knows the absolute counts are
	// extrapolated. Shape stays accurate; only the absolute
	// scale is multiplied by 1/SamplingRate.
	SamplingRate float64 `json:"samplingRate,omitempty"`
}

// GetLatencyHeatmap runs a single CH GROUP BY against the
// spans table, bucketing by (time, log-scale duration). The
// log-bin formula floor(log10(ms+ε)*subBins) is fast (no
// quantile state) and matches Honeycomb's heatmap binning.
// Filters mirror the rest of the span-metric API so a chart
// drawn from /explore reads the same dataset as the metric
// trend chart on the same page.
//
// Posture: at billion-span scale a 60×28 = 1680-bucket result
// set is trivial; the cost is in the GROUP BY which is bound
// by the (service_name, time) primary key + any filter on
// service.name. Sub-second on a 24h window for a single
// service.
func (s *Store) GetLatencyHeatmap(
	ctx context.Context,
	filters []FilterExpr,
	from, to time.Time,
	timeBuckets int,
) (*LatencyHeatmap, error) {
	if timeBuckets <= 0 || timeBuckets > 240 {
		timeBuckets = 60
	}
	// Sub-decade granularity. 4 sub-bins per decade gives a
	// 28-row heatmap covering 0.1ms → 1 Ms (which is past
	// any realistic span). Honeycomb uses 4-6 sub-bins
	// depending on zoom; 4 reads as "smooth-but-distinct".
	const subBins = 4
	const minLogMs = -1 // 0.1 ms
	const maxLogMs = 6  // 1 Ms
	const totalBins = (maxLogMs - minLogMs) * subBins // 28

	// Time bucket size in seconds — pick to fit the requested
	// number of buckets across the visible window.
	spanSec := int64(to.Sub(from).Seconds())
	if spanSec < int64(timeBuckets) {
		spanSec = int64(timeBuckets)
	}
	stepSec := spanSec / int64(timeBuckets)
	if stepSec < 1 {
		stepSec = 1
	}

	// Scale guardrail (v0.5.238): on wide windows the per-row
	// log10 + floor + GROUP BY hashing blew past the 30s
	// max_execution_time on billion-span installs. Solution:
	// hash-sample trace IDs deterministically and multiply the
	// counts back up in Go. Shape is preserved (distribution
	// over time × duration); absolute counts are extrapolated.
	//
	// Sampling kicks in only when the window exceeds 1h — short
	// windows are small enough to scan fully, and operators
	// staring at a 15-min window expect exact counts. The
	// fraction picks 1-in-N where N = 10 for ≤6h, N = 25 for
	// ≤24h, N = 100 beyond. Hash on trace_id so all spans of a
	// trace land in or out together (preserves multi-modal
	// signal — outlier traces aren't half-counted).
	sampleN := 1
	windowHours := to.Sub(from).Hours()
	switch {
	case windowHours > 24:
		sampleN = 100
	case windowHours > 6:
		sampleN = 25
	case windowHours > 1:
		sampleN = 10
	}

	wc := whereClause{}
	wc.add("time >= ?", from)
	wc.add("time <= ?", to)
	if sampleN > 1 {
		// cityHash64 is built-in to CH and lighter than sipHash64
		// for non-cryptographic sampling.
		wc.add(fmt.Sprintf("cityHash64(trace_id) %% %d = 0", sampleN))
	}
	ApplyFilters(&wc, filters)

	sql := fmt.Sprintf(`
		SELECT
		  toUnixTimestamp(toStartOfInterval(time, INTERVAL %d SECOND)) AS t_bucket,
		  toUInt8(greatest(0, least(%d, toInt32(floor((log10(duration / 1e6 + 0.0001) - %d) * %d))))) AS d_bin,
		  count() AS cnt
		FROM spans
		%s
		GROUP BY t_bucket, d_bin
		ORDER BY t_bucket, d_bin
		LIMIT 100000
		SETTINGS max_execution_time = 30`,
		stepSec, totalBins-1, minLogMs, subBins, wc.sql())

	rows, err := s.conn.Query(ctx, sql, wc.args...)
	if err != nil {
		return nil, fmt.Errorf("query heatmap: %w", err)
	}
	defer rows.Close()

	// Pre-build the time axis so the result table is a fixed
	// grid (frontend doesn't have to interpolate blank
	// columns). Precompute each bucket's start in nanoseconds.
	times := make([]int64, timeBuckets)
	startSec := from.Unix()
	for i := 0; i < timeBuckets; i++ {
		times[i] = (startSec + int64(i)*stepSec) * int64(time.Second/time.Nanosecond)
	}

	// Pre-build the latency-bin upper bounds in ms.
	durBins := make([]float64, totalBins)
	for j := 0; j < totalBins; j++ {
		exp := float64(minLogMs) + float64(j+1)/float64(subBins)
		durBins[j] = pow10(exp)
	}

	counts := make([][]uint32, timeBuckets)
	for i := range counts {
		counts[i] = make([]uint32, totalBins)
	}

	var maxCnt uint32 = 0
	for rows.Next() {
		var tBucket uint32
		var dBin uint8
		var cnt uint64
		if err := rows.Scan(&tBucket, &dBin, &cnt); err != nil {
			return nil, err
		}
		// Map t_bucket (unix-seconds aligned to stepSec) back
		// to the column index. Out-of-range rows are skipped
		// rather than clamped — a slightly-out-of-window edge
		// case shouldn't smear the first/last column.
		col := int((int64(tBucket) - startSec) / stepSec)
		if col < 0 || col >= timeBuckets {
			continue
		}
		row := int(dBin)
		if row < 0 || row >= totalBins {
			continue
		}
		// Scale back up when sampling is in effect — the SELECT
		// only saw 1/N of the trace IDs, so multiply each
		// per-cell count by N to estimate the full distribution.
		scaled := uint32(cnt) * uint32(sampleN)
		counts[col][row] = scaled
		if scaled > maxCnt {
			maxCnt = scaled
		}
	}

	return &LatencyHeatmap{
		Times:        times,
		DurationBins: durBins,
		Counts:       counts,
		MaxCount:     maxCnt,
		SamplingRate: 1.0 / float64(sampleN),
	}, rows.Err()
}

// pow10 — integer-friendly stand-in for math.Pow(10, x). Avoids
// a math import for what's a one-call helper.
func pow10(x float64) float64 {
	// 10^x = e^(x*ln10)
	const ln10 = 2.302585092994046
	return expSeries(x * ln10)
}

// expSeries computes e^v with a Taylor expansion that's
// accurate enough for log10 ms boundary values (we only call
// it to label axes, not for math correctness). Avoids the
// math package import.
func expSeries(v float64) float64 {
	// Reduce |v| via doubling: e^v = (e^(v/n))^n.
	// For v in [-15, 15] (log10 0.1ms..1Ms) we need ~10
	// halvings to land in [-0.1, 0.1] where the Taylor
	// expansion converges in 4 terms.
	n := 0
	for v > 0.5 || v < -0.5 {
		v /= 2
		n++
	}
	r := 1 + v + v*v/2 + v*v*v/6 + v*v*v*v/24
	for i := 0; i < n; i++ {
		r *= r
	}
	return r
}
