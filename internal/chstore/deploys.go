package chstore

import (
	"context"
	"fmt"
	"time"
)

// Deploy is one observed (service, service.version) entry. The
// frontend renders one vertical dashed line per Deploy on the
// metric / latency / error charts so an operator can read at a
// glance whether a regression coincides with a deploy.
//
// "Deploy" here is the moment a previously-unseen version of
// the service first emitted a span — that's what an operator
// reads as "the new code shipped". OTel populates
// resource.service.version from the SDK; if your build process
// doesn't set it (no SDK env var, no .ServiceVersion()), there
// will be nothing to show, which is the right answer.
type Deploy struct {
	Service       string `json:"service"`
	Version       string `json:"version"`
	// TimeUnixNs is the first-seen timestamp of this version
	// in the queried window — the marker position on the chart.
	TimeUnixNs    int64  `json:"timeUnixNs"`
	// SpanCount = how many spans this version has produced
	// since first appearance. Helps the UI dim out noise: a
	// version that produced 3 spans is probably a stuck
	// straggler instance, not a real deploy.
	SpanCount     int    `json:"spanCount"`
}

// GetServiceDeploys returns every distinct service.version
// observed for `service` in the time window, ordered by first
// appearance. Each row carries the first-seen timestamp — the
// position the deploy marker lands on the chart.
//
// Why min(time): in a continuous-deployment shop, an old
// version may have stragglers running for a few minutes after
// the new one ships. Using min(time) per version finds the
// *earliest* moment that version became active — the actual
// deploy timestamp — rather than the moment some pod last saw
// it.
//
// CH posture: the (service_name, time) primary key prunes by
// the time bound; the resource-attribute lookup is a single
// indexOf per row, cheap. Limit 50 is a hard cap so a chatty
// CD pipeline doesn't return thousands of rows.
// DeployImpactStats is one window's worth of RED for a deploy
// comparison (v0.5.189). Always reported as a pair (before /
// after) so the operator can read the delta directly without
// math-by-eye.
type DeployImpactStats struct {
	Count      uint64  `json:"count"`
	RPS        float64 `json:"rps"`
	ErrorRate  float64 `json:"errorRate"` // 0..1
	P99Ms      float64 `json:"p99Ms"`
	AvgMs      float64 `json:"avgMs"`
}

// DeployImpact captures a service.version transition's before/
// after RED + computed delta. Surfaced as the "last deploy
// impact" panel on the Service detail page so the operator gets
// a "did the new code regress something?" answer at a glance
// without opening the AI Copilot.
type DeployImpact struct {
	Service      string            `json:"service"`
	Version      string            `json:"version"`
	DeployTimeNs int64             `json:"deployTimeNs"`
	WindowSec    int               `json:"windowSec"`
	Before       DeployImpactStats `json:"before"`
	After        DeployImpactStats `json:"after"`
	// Delta — friendly signed deltas the UI renders as
	// colour-coded chips. Positive = worse, negative = better.
	P99DeltaPct       float64 `json:"p99DeltaPct"`       // % change
	AvgDeltaPct       float64 `json:"avgDeltaPct"`       // % change
	ErrorRateDeltaPct float64 `json:"errorRateDeltaPct"` // absolute pct points (after - before) * 100
}

// ComputeDeployImpact runs the side-by-side window comparison
// for one (service, deployTime). Single CH pass via quantileIf /
// countIf gates so before + after come back together without
// two scans. Cost is bounded by the window size (default 10
// min) — at 1B-span/day this is sub-second on the partition-
// pruned spans table.
func (s *Store) ComputeDeployImpact(
	ctx context.Context, service, version string, deployTimeNs int64, windowSec int,
) (*DeployImpact, error) {
	if windowSec <= 0 {
		windowSec = 600
	}
	if windowSec > 6*3600 {
		windowSec = 6 * 3600
	}
	deployT := time.Unix(0, deployTimeNs)
	beforeStart := deployT.Add(-time.Duration(windowSec) * time.Second)
	afterEnd := deployT.Add(time.Duration(windowSec) * time.Second)
	row := s.conn.QueryRow(ctx, `
		SELECT
		  countIf(time < ?)                                        AS bef_count,
		  countIf(time >= ?)                                       AS aft_count,
		  countIf(time < ?  AND status_code = 'error')             AS bef_err,
		  countIf(time >= ? AND status_code = 'error')             AS aft_err,
		  quantileIf(0.99)(duration, time < ?)  / 1e6              AS bef_p99,
		  quantileIf(0.99)(duration, time >= ?) / 1e6              AS aft_p99,
		  avgIf(duration,            time < ?)  / 1e6              AS bef_avg,
		  avgIf(duration,            time >= ?) / 1e6              AS aft_avg
		FROM spans
		WHERE service_name = ? AND time >= ? AND time <= ?
		SETTINGS max_execution_time = 15,
		         optimize_skip_unused_shards = 1`,
		deployT, deployT,
		deployT, deployT,
		deployT, deployT,
		deployT, deployT,
		service, beforeStart, afterEnd)
	var befCount, aftCount, befErr, aftErr uint64
	var befP99, aftP99, befAvg, aftAvg float64
	if err := row.Scan(&befCount, &aftCount, &befErr, &aftErr,
		&befP99, &aftP99, &befAvg, &aftAvg); err != nil {
		return nil, fmt.Errorf("compute deploy impact: %w", err)
	}
	mkStats := func(c, e uint64, p99, avg float64) DeployImpactStats {
		st := DeployImpactStats{Count: c, P99Ms: p99, AvgMs: avg}
		if c > 0 {
			st.ErrorRate = float64(e) / float64(c)
			st.RPS = float64(c) / float64(windowSec)
		}
		return st
	}
	before := mkStats(befCount, befErr, befP99, befAvg)
	after := mkStats(aftCount, aftErr, aftP99, aftAvg)
	out := &DeployImpact{
		Service:      service,
		Version:      version,
		DeployTimeNs: deployTimeNs,
		WindowSec:    windowSec,
		Before:       before,
		After:        after,
	}
	if before.P99Ms > 0 {
		out.P99DeltaPct = (after.P99Ms - before.P99Ms) / before.P99Ms * 100
	}
	if before.AvgMs > 0 {
		out.AvgDeltaPct = (after.AvgMs - before.AvgMs) / before.AvgMs * 100
	}
	out.ErrorRateDeltaPct = (after.ErrorRate - before.ErrorRate) * 100
	return out, nil
}

func (s *Store) GetServiceDeploys(
	ctx context.Context, service string, from, to time.Time,
) ([]Deploy, error) {
	const sql = `
		SELECT
			res_values[indexOf(res_keys, 'service.version')] AS version,
			toUnixTimestamp64Nano(min(time))                 AS first_seen_ns,
			count()                                          AS span_count
		FROM spans
		WHERE service_name = ?
		  AND time >= ? AND time <= ?
		  AND has(res_keys, 'service.version')
		GROUP BY version
		HAVING version != ''
		ORDER BY first_seen_ns ASC
		LIMIT 50
		SETTINGS max_execution_time = 15,
		         optimize_skip_unused_shards = 1`
	rows, err := s.conn.Query(ctx, sql, service, from, to)
	if err != nil {
		return nil, fmt.Errorf("query deploys: %w", err)
	}
	defer rows.Close()

	out := []Deploy{}
	for rows.Next() {
		var d Deploy
		var spanCnt uint64
		if err := rows.Scan(&d.Version, &d.TimeUnixNs, &spanCnt); err != nil {
			return nil, err
		}
		d.Service = service
		d.SpanCount = int(spanCnt)
		out = append(out, d)
	}
	return out, rows.Err()
}
