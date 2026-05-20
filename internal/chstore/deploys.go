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

// RecentDeployEntry is one row from GetRecentDeploys —
// powers the "what changed" page-top banner (v0.5.277).
type RecentDeployEntry struct {
	Service       string `json:"service"`
	Version       string `json:"version"`
	FirstSeenNs   int64  `json:"firstSeenNs"`
	SpanCount     uint64 `json:"spanCount"`
}

// v0.5.278 — placeholder versions that aren't real deploys.
// v0.5.283 — broadened to cover more Maven/Gradle/Node defaults
// plus unresolved literal placeholders (`${project.version}`).
//
// Operator-reported: Java apps emit Maven's default
// "0.0.1-SNAPSHOT" when devs don't override it, so the deploy
// detector flagged every pod restart as a fresh deploy. Same
// shape for k8s `latest` tags and the "dev" / "unknown"
// placeholders OTel SDKs sometimes default to.
//
// CH-readable form: any version IN this list is excluded from
// the deploy listing. Container.image.tag is consulted as a
// fallback when service.version is empty or placeholder.
const placeholderVersionList = `(
    '', '0.0.1', '0.0.1-SNAPSHOT', '0.1.0-SNAPSHOT',
    '1.0-SNAPSHOT', '1.0.0-SNAPSHOT',
    '${project.version}', '${version}',
    'latest', 'dev', 'unknown', 'snapshot',
    'main', 'master', 'HEAD',
    'null', 'none', 'n/a', 'NULL'
  )`

// effectiveVersionExpr is the SQL fragment that picks a real
// version per row: prefer service.version when non-placeholder;
// fall back to container.image.tag / k8s.container.image.tag;
// then to Helm convention labels (app.kubernetes.io/version,
// projected by the OTel k8s receiver as
// k8s.{pod,deployment}.labels.app_kubernetes_io_version);
// otherwise empty (caller filters those out).
//
// v0.5.283 — extended the chain with Helm / k8s label sources.
// On platforms where the build pipeline doesn't override
// service.version but DOES set the standard
// app.kubernetes.io/version label on the Deployment (Helm
// default), this is the only real version signal we'll see.
const effectiveVersionExpr = `
  multiIf(
    res_values[indexOf(res_keys, 'service.version')] NOT IN ` + placeholderVersionList + `,
      res_values[indexOf(res_keys, 'service.version')],
    res_values[indexOf(res_keys, 'container.image.tag')] NOT IN ` + placeholderVersionList + `,
      res_values[indexOf(res_keys, 'container.image.tag')],
    res_values[indexOf(res_keys, 'k8s.container.image.tag')] NOT IN ` + placeholderVersionList + `,
      res_values[indexOf(res_keys, 'k8s.container.image.tag')],
    res_values[indexOf(res_keys, 'k8s.deployment.labels.app_kubernetes_io_version')] NOT IN ` + placeholderVersionList + `,
      res_values[indexOf(res_keys, 'k8s.deployment.labels.app_kubernetes_io_version')],
    res_values[indexOf(res_keys, 'k8s.pod.labels.app_kubernetes_io_version')] NOT IN ` + placeholderVersionList + `,
      res_values[indexOf(res_keys, 'k8s.pod.labels.app_kubernetes_io_version')],
    res_values[indexOf(res_keys, 'k8s.deployment.labels.version')] NOT IN ` + placeholderVersionList + `,
      res_values[indexOf(res_keys, 'k8s.deployment.labels.version')],
    res_values[indexOf(res_keys, 'helm.chart.version')] NOT IN ` + placeholderVersionList + `,
      res_values[indexOf(res_keys, 'helm.chart.version')],
    ''
  )`

// GetRecentDeploys returns service.version transitions
// first-seen in the requested window, ordered most-recent
// first. Cross-service "what changed" signal for the global
// banner — operator sees "frontend just shipped v1.2.3 14m
// ago" the moment they open ANY page.
//
// CH posture: scans the (service_name, time) primary key
// inside the time bound, then min()s per (service, version)
// pair so a service that's been emitting the same version
// for hours doesn't dominate the result. Limit 20 caps the
// banner footprint; SETTINGS max_execution_time = 5 keeps it
// snappy enough to fire from a global 30s poll.
func (s *Store) GetRecentDeploys(ctx context.Context, since time.Duration, limit int) ([]RecentDeployEntry, error) {
	if since <= 0 {
		since = 30 * time.Minute
	}
	if limit <= 0 {
		limit = 20
	}
	// v0.5.309 — Operator-reported (test env w/ 3000+ services):
	// /deploys page hit "Failed to load deploys". Two clamps
	// fought it: (a) hard limit at 100 capped /deploys' 500-row
	// request, hiding most rows, and (b) max_execution_time=5
	// was tuned for the 30m banner case — 30-day spans GROUP BY
	// at billion-span scale needs more headroom. Now: cap raised
	// to 2000 (matches handler's hard cap) and timeout scales
	// with `since`: 5s for ≤30m (banner stays snappy), 30s for
	// medium windows, 60s for ≥1d.
	if limit > 2000 {
		limit = 2000
	}
	maxExecSec := 5
	switch {
	case since > 24*time.Hour:
		maxExecSec = 60
	case since > 1*time.Hour:
		maxExecSec = 30
	}
	cutoff := time.Now().Add(-since)
	// v0.5.278 — placeholder filter + image-tag fallback (see
	// effectiveVersionExpr / placeholderVersionList). Operator-
	// reported: Java apps emitting Maven's default
	// "0.0.1-SNAPSHOT" turned every pod restart into a "deploy"
	// in the banner.
	sql := fmt.Sprintf(`
		SELECT
		  service_name,
		  `+effectiveVersionExpr+` AS version,
		  toUnixTimestamp64Nano(min(time)) AS first_seen,
		  count() AS span_count
		FROM spans
		WHERE time >= ?
		  AND (has(res_keys, 'service.version')
		    OR has(res_keys, 'container.image.tag')
		    OR has(res_keys, 'k8s.container.image.tag')
		    OR has(res_keys, 'k8s.deployment.labels.app_kubernetes_io_version')
		    OR has(res_keys, 'k8s.pod.labels.app_kubernetes_io_version')
		    OR has(res_keys, 'k8s.deployment.labels.version')
		    OR has(res_keys, 'helm.chart.version'))
		GROUP BY service_name, version
		HAVING version != ''
		   AND first_seen >= ?
		ORDER BY first_seen DESC
		LIMIT ?
		SETTINGS max_execution_time = %d`, maxExecSec)
	rows, err := s.conn.Query(ctx, sql, cutoff, cutoff.UnixNano(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RecentDeployEntry{}
	for rows.Next() {
		var r RecentDeployEntry
		if err := rows.Scan(&r.Service, &r.Version, &r.FirstSeenNs, &r.SpanCount); err != nil {
			return nil, err
		}
		if r.Version == "" {
			continue
		}
		out = append(out, r)
	}
	return out, rows.Err()
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
	// v0.5.278 — same placeholder filter + image-tag fallback
	// as GetRecentDeploys. The service detail page's Deploy
	// History panel was the loudest victim of the
	// "0.0.1-SNAPSHOT" Maven default — every restart looked
	// like a deploy.
	sql := `
		SELECT
			` + effectiveVersionExpr + `                     AS version,
			toUnixTimestamp64Nano(min(time))                 AS first_seen_ns,
			count()                                          AS span_count
		FROM spans
		WHERE service_name = ?
		  AND time >= ? AND time <= ?
		  AND (has(res_keys, 'service.version')
		    OR has(res_keys, 'container.image.tag')
		    OR has(res_keys, 'k8s.container.image.tag')
		    OR has(res_keys, 'k8s.deployment.labels.app_kubernetes_io_version')
		    OR has(res_keys, 'k8s.pod.labels.app_kubernetes_io_version')
		    OR has(res_keys, 'k8s.deployment.labels.version')
		    OR has(res_keys, 'helm.chart.version'))
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
