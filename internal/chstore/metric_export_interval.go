package chstore

import (
	"context"
	"math"
	"time"
)

// v0.8.243 — granularity slice B (operator: "metric charts aren't as
// smooth as Grafana"). Requesting buckets FINER than a metric's actual
// export cadence doesn't add resolution — it adds holes: a 10s-exported
// gauge drawn at step=1s is 90% empty buckets rendered as sawtooth or
// gaps. Grafana solves this with $__rate_interval (never below the
// scrape interval); Coremetry's equivalent is this observed-export-
// interval clamp: the effective step never drops below what the metric
// actually ships.

// metricIvEntry is one cached probe result. iv == 0 means "couldn't
// infer" (young/sparse metric, probe error) → no clamp applied.
type metricIvEntry struct {
	at time.Time
	iv int
}

const (
	metricIvTTL         = 60 * time.Second
	metricIvProbeWindow = 10 * time.Minute
	// metricIvMinPoints — below this many points the interval estimate
	// is noise (a metric that just started reporting); don't clamp.
	metricIvMinPoints = 5
	// metricIvMaxSeconds — an inferred interval above this is treated
	// as "effectively sparse"; clamping a chart to >1h buckets would
	// fight the operator, and such metrics look fine unclamped.
	metricIvMaxSeconds = 3600
)

// exportIntervalFrom converts the densest series' (point count, first→
// last span) into an export-interval estimate in seconds. Pure so the
// young-metric / sparse / implausible branches are table-tested.
func exportIntervalFrom(cnt uint64, spanSec int64) int {
	if cnt < metricIvMinPoints || spanSec <= 0 {
		return 0
	}
	iv := int(math.Round(float64(spanSec) / float64(cnt-1)))
	if iv < 1 {
		iv = 1
	}
	if iv > metricIvMaxSeconds {
		return 0
	}
	return iv
}

// clampStepToExport lifts a requested step to the metric's observed
// export interval. Only ever RAISES the step — a coarse request stays
// coarse. Pure.
func clampStepToExport(step, exportIv int) int {
	if exportIv > step {
		return exportIv
	}
	return step
}

// metricExportIntervalSQL builds the bounded probe: the densest
// series' point count + covered span within the recent window. Pure so
// the CH-bounds contract (time-bounded WHERE, LIMIT, max_execution_
// time) is unit-tested. Series identity = (service_name, host_name,
// attr_values) — the same tuple the read path groups by.
func metricExportIntervalSQL(withService bool) string {
	q := `
		SELECT cnt, spanSec FROM (
			SELECT count() AS cnt,
			       dateDiff('second', min(time), max(time)) AS spanSec
			FROM metric_points
			WHERE metric = ? AND time >= ? AND time <= ?`
	if withService {
		q += `
			  AND service_name = ?`
	}
	q += `
			GROUP BY service_name, host_name, attr_values
			ORDER BY cnt DESC
			LIMIT 1
		)
		SETTINGS max_execution_time = 5`
	return q
}

// metricExportInterval returns the cached/probed export interval for a
// metric (optionally service-scoped — a metric can ship at different
// cadences per service). 0 = unknown → caller applies no clamp; a
// probe failure must never break the chart read.
func (s *Store) metricExportInterval(ctx context.Context, name, service string) int {
	key := name + "\x00" + service
	s.metricIvMu.RLock()
	if e, ok := s.metricIv[key]; ok && time.Since(e.at) < metricIvTTL {
		s.metricIvMu.RUnlock()
		return e.iv
	}
	s.metricIvMu.RUnlock()

	to := time.Now()
	from := to.Add(-metricIvProbeWindow)
	args := []any{name, from, to}
	if service != "" {
		args = append(args, service)
	}
	iv := 0
	var cnt uint64
	var spanSec int64
	if err := s.conn.QueryRow(ctx, metricExportIntervalSQL(service != ""), args...).Scan(&cnt, &spanSec); err == nil {
		iv = exportIntervalFrom(cnt, spanSec)
	}

	s.metricIvMu.Lock()
	if s.metricIv == nil {
		s.metricIv = map[string]metricIvEntry{}
	}
	s.metricIv[key] = metricIvEntry{at: time.Now(), iv: iv}
	s.metricIvMu.Unlock()
	return iv
}
