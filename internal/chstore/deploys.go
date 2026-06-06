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

// instanceIdExpr picks a stable per-pod identity from a span's
// resource attributes: k8s.pod.name (the rollout-tracking gold
// standard) → service.instance.id → the host_name column. Empty
// when none are present — the service emits no pod identity, so we
// can't detect pod churn, which is the honest answer (the UI then
// shows nothing rather than a misleading empty rollout list).
const instanceIdExpr = `
  multiIf(
    res_values[indexOf(res_keys, 'k8s.pod.name')] != '',       res_values[indexOf(res_keys, 'k8s.pod.name')],
    res_values[indexOf(res_keys, 'service.instance.id')] != '', res_values[indexOf(res_keys, 'service.instance.id')],
    host_name != '',                                            host_name,
    ''
  )`

// Rollout is one detected pod-churn event — a 5-minute bucket where
// the service's active instance set materially turned over (old pods
// gone + new pods in), i.e. a rollout / restart. Replaces the
// version-bump deploy marker in environments where service.version
// is constant (the common case when the build pipeline doesn't set
// it). v0.8.x — operator-reported: constant service.version made the
// version-based markers pure noise.
type Rollout struct {
	TimeUnixNs    int64         `json:"timeUnixNs"`
	PodsAdded     int           `json:"podsAdded"`
	PodsRemoved   int           `json:"podsRemoved"`
	ActivePods    int           `json:"activePods"` // active set size after the rollout
	AddedPods     []string      `json:"addedPods,omitempty"`   // up to 5 sample ids
	RemovedPods   []string      `json:"removedPods,omitempty"` // up to 5 sample ids
	VersionBefore string        `json:"versionBefore,omitempty"`
	VersionAfter  string        `json:"versionAfter,omitempty"`
	Impact        *DeployImpact `json:"impact,omitempty"` // before/after RED, filled by the API layer
}

// RolloutsResult is the GetServiceRollouts payload.
type RolloutsResult struct {
	Service  string    `json:"service"`
	Rollouts []Rollout `json:"rollouts"`
	// VersionConstant is true when the effective service.version never
	// changes across the window — the UI uses it to HIDE the version
	// chip/column so "1.0.0" isn't rendered on every surface.
	VersionConstant bool `json:"versionConstant"`
	// InstancesTracked is false when no pod identity (k8s.pod.name /
	// service.instance.id / host_name) is present, so churn can't be
	// computed — the UI shows nothing rather than a misleading empty.
	InstancesTracked bool `json:"instancesTracked"`
}

// GetServiceRollouts detects pod-churn rollouts for a service by
// reading the DISTINCT active instance set per 5-minute bucket from
// raw spans and diffing consecutive buckets: a rollout is a bucket
// where ≥50% of the previous active pods disappeared AND ≥1 new pod
// appeared (full turnover, not autoscaling jitter). Adjacent churn
// buckets coalesce into one event (a staggered rollout spans a few
// buckets). Also reports whether the effective service.version is
// constant across the window.
//
// CH posture: single-service + time-bound WHERE prunes by the
// (service_name, time) primary key; groupUniqArray over a bucket is
// bounded by pod count (tens), not span count. LIMIT on buckets +
// max_execution_time cap it. At billions-of-spans/day a per-bucket
// distinct-instance scan over a WIDE window could get hot — add a
// service_instances_5m MV if system.query_log flags it; bounded raw
// is fine for the service-detail windows this serves.
func (s *Store) GetServiceRollouts(
	ctx context.Context, service string, from, to time.Time,
) (*RolloutsResult, error) {
	sql := `
		SELECT bucket, groupUniqArrayIf(iid, iid != '') AS pods, argMax(ver, t) AS version
		FROM (
			SELECT
				toStartOfInterval(time, INTERVAL 5 MINUTE) AS bucket,
				time                                       AS t,
				` + instanceIdExpr + `                     AS iid,
				` + effectiveVersionExpr + `               AS ver
			FROM spans
			WHERE service_name = ? AND time >= ? AND time <= ?
		)
		GROUP BY bucket
		ORDER BY bucket ASC
		LIMIT 2000
		SETTINGS max_execution_time = 15,
		         optimize_skip_unused_shards = 1`
	rows, err := s.conn.Query(ctx, sql, service, from, to)
	if err != nil {
		return nil, fmt.Errorf("query rollouts: %w", err)
	}
	defer rows.Close()

	var buckets []rolloutBucket
	for rows.Next() {
		var b rolloutBucket
		if err := rows.Scan(&b.t, &b.pods, &b.version); err != nil {
			return nil, err
		}
		buckets = append(buckets, b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return analyzeRollouts(service, buckets), nil
}

// rolloutBucket is one 5-minute slice of a service's active pod set +
// its dominant effective version — the input to analyzeRollouts.
type rolloutBucket struct {
	t       time.Time
	pods    []string
	version string
}

// analyzeRollouts is the PURE pod-churn detection over the per-bucket
// active sets (no I/O), split out so the subtle instant-cutover
// windowing is table-testable. See deploys_test.go. v0.8.x.
func analyzeRollouts(service string, buckets []rolloutBucket) *RolloutsResult {
	res := &RolloutsResult{Service: service, Rollouts: []Rollout{}}

	// Version-constancy + instance-availability across the window.
	seenVer := ""
	res.VersionConstant = true
	for _, b := range buckets {
		if len(b.pods) > 0 {
			res.InstancesTracked = true
		}
		if b.version != "" {
			if seenVer == "" {
				seenVer = b.version
			} else if b.version != seenVer {
				res.VersionConstant = false
			}
		}
	}

	// Churn detection. Compare each bucket's active pod set to the set
	// `lookback` buckets EARLIER — not just i-1. An instant cutover
	// (all old pods stop + all new pods start at the same moment, e.g.
	// a process restart) puts the new pods in one single-bucket diff
	// and the gone pods in the NEXT one, so neither adjacent diff alone
	// shows both add + remove. The wider baseline straddles the whole
	// transition. A rollout = ≥50% of the baseline pods gone AND ≥1 new
	// pod present. Detections within `lookback` of the last are the
	// same (staggered) rollout, not a fresh one.
	const lookback = 2 // 2 × 5-min buckets ≈ a 10-minute rollout window
	lastRolloutIdx := -100
	for i := 1; i < len(buckets); i++ {
		cur := podSet(buckets[i].pods)
		if len(cur) == 0 {
			continue
		}
		j := i - lookback
		if j < 0 {
			j = 0
		}
		base := podSet(buckets[j].pods)
		if len(base) == 0 {
			continue
		}
		added := podDiff(cur, base)   // in cur, not in baseline
		removed := podDiff(base, cur) // in baseline, not in cur
		threshold := (len(base) + 1) / 2 // ceil(0.5 * len(base))
		if len(added) < 1 || len(removed) < threshold {
			continue
		}
		if i-lastRolloutIdx <= lookback {
			lastRolloutIdx = i // same (staggered) rollout, already recorded
			continue
		}
		r := Rollout{
			TimeUnixNs:  buckets[i].t.UnixNano(),
			PodsAdded:   len(added),
			PodsRemoved: len(removed),
			ActivePods:  len(cur),
			AddedPods:   podSample(added, 5),
			RemovedPods: podSample(removed, 5),
		}
		if buckets[i].version != "" && buckets[j].version != "" && buckets[i].version != buckets[j].version {
			r.VersionBefore = buckets[j].version
			r.VersionAfter = buckets[i].version
		}
		res.Rollouts = append(res.Rollouts, r)
		lastRolloutIdx = i
	}
	return res
}

func podSet(xs []string) map[string]struct{} {
	m := make(map[string]struct{}, len(xs))
	for _, x := range xs {
		if x != "" {
			m[x] = struct{}{}
		}
	}
	return m
}

// podDiff returns keys in a that are absent from b.
func podDiff(a, b map[string]struct{}) []string {
	var out []string
	for k := range a {
		if _, ok := b[k]; !ok {
			out = append(out, k)
		}
	}
	return out
}

func podSample(xs []string, n int) []string {
	if len(xs) <= n {
		return xs
	}
	return xs[:n]
}
