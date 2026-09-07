package chstore

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Latency SLI on spanmetrics_1m — v0.10.518.
//
// Prod log (2026-09-06): "[slo] durum hesabı başarısız … code 159 … 20 s"
// — a 140-SLO fan-out where every LATENCY SLI scanned raw `spans` over the
// SLO's WindowDays with `countIf(duration <= threshold)`. Availability has
// ridden the summary MVs since v0.8.200; latency stayed raw because no MV
// pre-computes a per-span threshold compare. MV-first invariant (#3) says a
// raw-spans aggregate is a bug.
//
// spanmetrics_1m carries `kind` (entry-span scope, v0.9.241) and a t-digest
// of duration per (service, name, kind, status, route, minute). Merging
// the digests over the window and reading the quantile curve at a dense
// level grid gives the inverse CDF: the fraction of spans at or under the
// threshold is the level where the curve crosses it (linear interpolation
// between neighbouring grid points). Measured locally (api-gateway,
// thresholds 500/1000/2000 ms, windows 6h/24h/7d): |SLI_mv − SLI_raw| ≤
// 0.13 pp at 6h, ≤ 0.05 pp at 24h+ — inside the MV's own row loss
// (service_summary_5m shows the same 1.5 % vs raw locally).
//
// Guards: the MV is forward-only and TTL'd at 30 days, so a window that
// starts before spanmetricsCoverageStart falls back to the raw path
// unchanged (fail-safe: the probe returns now() on error → raw).

// sloLatencyLevels — the level grid. 1 % steps across the body, 0.1 % steps
// from 99 %, 0.01 % steps from 99.9 %: an SLO at 99.9 % needs the crossing
// resolved finer than its own error budget (0.1 pp), and t-digest is
// accurate exactly in that tail.
var sloLatencyLevels = func() []float64 {
	var lv []float64
	for i := 1; i <= 98; i++ {
		lv = append(lv, float64(i)/100)
	}
	for i := 990; i <= 998; i++ {
		lv = append(lv, float64(i)/1000)
	}
	for i := 9990; i <= 9999; i++ {
		lv = append(lv, float64(i)/10000)
	}
	lv = append(lv, 0.99995, 0.99999)
	return lv
}()

// sloLatencyLevelsSQL — the grid as a ClickHouse parameter list.
var sloLatencyLevelsSQL = func() string {
	parts := make([]string, len(sloLatencyLevels))
	for i, l := range sloLatencyLevels {
		parts[i] = strconv.FormatFloat(l, 'f', -1, 64)
	}
	return strings.Join(parts, ",")
}()

// sloGoodFraction — inverse CDF: share of the population with duration ≤
// thresholdNs, from the quantile curve `qs` sampled at `levels` (both
// ascending, same length). Linear between grid points; below the first
// point it interpolates from (0, 0); above the last point it returns 1
// (everything the digest saw is under the threshold, up to grid
// resolution). Clamped to [0, 1]. A degenerate curve (empty, length
// mismatch) reports 0 so a caller never invents "all good".
func sloGoodFraction(levels, qs []float64, thresholdNs float64) float64 {
	n := len(qs)
	if n == 0 || n != len(levels) {
		return 0
	}
	if thresholdNs >= qs[n-1] {
		return 1
	}
	prevL, prevQ := 0.0, 0.0
	for i := 0; i < n; i++ {
		if qs[i] > thresholdNs {
			span := qs[i] - prevQ
			if span <= 0 {
				return clamp01(prevL)
			}
			return clamp01(prevL + (levels[i]-prevL)*((thresholdNs-prevQ)/span))
		}
		prevL, prevQ = levels[i], qs[i]
	}
	return 1
}

func clamp01(v float64) float64 {
	if v < 0 || math.IsNaN(v) {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// sloGoodCount — total × fraction, rounded, never above total.
func sloGoodCount(total uint64, frac float64) uint64 {
	g := uint64(math.Round(float64(total) * frac))
	if g > total {
		return total
	}
	return g
}

// sloLatencyMVEligible — the MV can serve a window starting at `since`
// only when it holds every bucket from there on (forward-only cutover +
// 30-day TTL). coverageStart is spanmetricsCoverageStart (fail-safe now()).
func sloLatencyMVEligible(since, coverageStart time.Time) bool {
	return !since.Before(coverageStart)
}

// sloLatencyMVSQL — the MV read. grouped=true adds the per-day bucket used
// by the burn-series sparkline. The quantile array is Float32 in ClickHouse
// (t-digest); arrayMap → Float64 so the driver scans []float64. Column
// order: [bucket,] total, qs.
func sloLatencyMVSQL(source string, withOperation, grouped bool, maxExecSec int) string {
	var b strings.Builder
	b.WriteString("SELECT ")
	if grouped {
		b.WriteString("toStartOfDay(time_bucket) AS bucket, ")
	}
	b.WriteString("countMerge(calls_state) AS total, ")
	b.WriteString("arrayMap(x -> toFloat64(x), quantilesTDigestMerge(" + sloLatencyLevelsSQL + ")(duration_q_state)) AS qs ")
	b.WriteString("FROM " + source + " WHERE service_name = ? AND time_bucket >= ?" + sloLatencyEntryWhere)
	if withOperation {
		b.WriteString(" AND name = ?")
	}
	if grouped {
		b.WriteString(" GROUP BY bucket ORDER BY bucket")
	}
	b.WriteString(fmt.Sprintf(" SETTINGS max_execution_time = %d", maxExecSec))
	return b.String()
}
