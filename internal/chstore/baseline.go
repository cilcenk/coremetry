package chstore

import (
	"context"
	"fmt"
	"time"
)

// MetricBaseline summarises the recent distribution of one
// alertable metric for one service (or globally when service
// is empty). Drives the "✨ suggest threshold" panel on the
// alert-rule editor — operators see what NORMAL looks like
// before they pick a threshold, instead of guessing 5 / 500ms
// and getting paged at 4am because the actual P99 baseline
// was 800ms.
//
// All fields are in the SAME unit the alert evaluator
// compares against, so the operator can paste a value
// directly into the threshold input:
//
//	error_rate    → percentage (0..100)
//	p50_ms / p95_ms / p99_ms / avg_ms → milliseconds
//	request_rate  → requests per second
//	error_count   → absolute count per window (5 min default)
type MetricBaseline struct {
	Metric      string  `json:"metric"`
	Service     string  `json:"service,omitempty"` // empty = all services
	P50         float64 `json:"p50"`
	P95         float64 `json:"p95"`
	P99         float64 `json:"p99"`
	Max         float64 `json:"max"`
	Mean        float64 `json:"mean"`
	SampleCount int64   `json:"sampleCount"` // # of spans / minutes scanned
	WindowSec   int64   `json:"windowSec"`   // lookback the percentiles were computed over
}

// GetMetricBaseline runs the right percentile query for the
// requested metric over the given lookback. Service filter is
// optional — global baselines help when the operator is
// adding a "warn on any service exceeding X" cross-service
// rule. Hard cap of 7 days; longer lookbacks were measured to
// add ~3s without changing the percentile values meaningfully
// (recent distribution dominates).
func (s *Store) GetMetricBaseline(
	ctx context.Context, service, metric string, lookback time.Duration,
) (*MetricBaseline, error) {
	if lookback <= 0 {
		lookback = 7 * 24 * time.Hour
	}
	if lookback > 7*24*time.Hour {
		lookback = 7 * 24 * time.Hour
	}
	out := &MetricBaseline{
		Metric:    metric,
		Service:   service,
		WindowSec: int64(lookback / time.Second),
	}
	from := time.Now().Add(-lookback)

	// Latency metrics: query the spans table directly, span-
	// level percentiles. Includes a sample-count so the UI
	// can dim the suggestion when there's not enough data
	// to be statistically meaningful.
	switch metric {
	case "p50_ms", "p95_ms", "p99_ms", "avg_ms":
		svcFilter := ""
		var args []any
		args = append(args, from)
		if service != "" {
			svcFilter = " AND service_name = ?"
			args = append(args, service)
		}
		row := s.conn.QueryRow(ctx, `
			SELECT quantile(0.5)(duration)  / 1e6 AS p50,
			       quantile(0.95)(duration) / 1e6 AS p95,
			       quantile(0.99)(duration) / 1e6 AS p99,
			       max(duration)            / 1e6 AS mx,
			       avg(duration)            / 1e6 AS mean,
			       count()                       AS n
			FROM spans
			WHERE time >= ?`+svcFilter+`
			SETTINGS max_execution_time = 10`, args...)
		var n uint64
		if err := row.Scan(&out.P50, &out.P95, &out.P99, &out.Max, &out.Mean, &n); err != nil {
			return nil, fmt.Errorf("scan latency baseline: %w", err)
		}
		out.SampleCount = int64(n)
		return out, nil

	case "error_rate":
		// Bucketed: per-minute error % over the window, then
		// percentiles of the minute rates. Mirrors the
		// evaluator's "windowSec rolling" semantic better than
		// a single global percentage which would smooth over
		// the spike shape.
		svcFilter := ""
		var args []any
		args = append(args, from)
		if service != "" {
			svcFilter = " AND service_name = ?"
			args = append(args, service)
		}
		row := s.conn.QueryRow(ctx, `
			WITH per_min AS (
				SELECT toStartOfMinute(time) AS t,
				       countIf(status_code = 'error') / nullIf(count(), 0) * 100 AS rate
				FROM spans
				WHERE time >= ?`+svcFilter+`
				GROUP BY t
			)
			SELECT quantile(0.5)(rate),
			       quantile(0.95)(rate),
			       quantile(0.99)(rate),
			       max(rate),
			       avg(rate),
			       count()
			FROM per_min
			SETTINGS max_execution_time = 10`, args...)
		var n uint64
		if err := row.Scan(&out.P50, &out.P95, &out.P99, &out.Max, &out.Mean, &n); err != nil {
			return nil, fmt.Errorf("scan error_rate baseline: %w", err)
		}
		out.SampleCount = int64(n)
		return out, nil

	case "request_rate":
		// Spans-per-second computed per minute (count / 60).
		svcFilter := ""
		var args []any
		args = append(args, from)
		if service != "" {
			svcFilter = " AND service_name = ?"
			args = append(args, service)
		}
		row := s.conn.QueryRow(ctx, `
			WITH per_min AS (
				SELECT toStartOfMinute(time) AS t,
				       count() / 60.0 AS rps
				FROM spans
				WHERE time >= ?`+svcFilter+`
				GROUP BY t
			)
			SELECT quantile(0.5)(rps),
			       quantile(0.95)(rps),
			       quantile(0.99)(rps),
			       max(rps),
			       avg(rps),
			       count()
			FROM per_min
			SETTINGS max_execution_time = 10`, args...)
		var n uint64
		if err := row.Scan(&out.P50, &out.P95, &out.P99, &out.Max, &out.Mean, &n); err != nil {
			return nil, fmt.Errorf("scan request_rate baseline: %w", err)
		}
		out.SampleCount = int64(n)
		return out, nil

	case "error_count":
		// Absolute count per 5-min window (the evaluator's
		// default WindowSec for count-mode rules).
		svcFilter := ""
		var args []any
		args = append(args, from)
		if service != "" {
			svcFilter = " AND service_name = ?"
			args = append(args, service)
		}
		row := s.conn.QueryRow(ctx, `
			WITH per_bucket AS (
				SELECT toStartOfInterval(time, INTERVAL 5 MINUTE) AS t,
				       countIf(status_code = 'error') AS c
				FROM spans
				WHERE time >= ?`+svcFilter+`
				GROUP BY t
			)
			SELECT quantile(0.5)(c),
			       quantile(0.95)(c),
			       quantile(0.99)(c),
			       max(c),
			       avg(c),
			       count()
			FROM per_bucket
			SETTINGS max_execution_time = 10`, args...)
		var n uint64
		if err := row.Scan(&out.P50, &out.P95, &out.P99, &out.Max, &out.Mean, &n); err != nil {
			return nil, fmt.Errorf("scan error_count baseline: %w", err)
		}
		out.SampleCount = int64(n)
		return out, nil
	}

	return nil, fmt.Errorf("baseline not supported for metric %q", metric)
}
