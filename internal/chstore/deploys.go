package chstore

import (
	"context"
	"fmt"
	"log"
	"math"
	"strings"
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
	Service string `json:"service"`
	Version string `json:"version"`
	// TimeUnixNs is the first-seen timestamp of this version
	// in the queried window — the marker position on the chart.
	TimeUnixNs int64 `json:"timeUnixNs"`
	// SpanCount = how many spans this version has produced
	// since first appearance. Helps the UI dim out noise: a
	// version that produced 3 spans is probably a stuck
	// straggler instance, not a real deploy.
	SpanCount int `json:"spanCount"`
}

// RecentDeployEntry is one row from GetRecentDeploys —
// powers the "what changed" page-top banner (v0.5.277).
type RecentDeployEntry struct {
	Service     string `json:"service"`
	Version     string `json:"version"`
	FirstSeenNs int64  `json:"firstSeenNs"`
	SpanCount   uint64 `json:"spanCount"`
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
// version per row.
//
// v0.9.66 (operator-reported) — ÖNCELİK TERSİNE DÖNDÜ: container
// image tag'i artık service.version'ın ÖNÜNDE. Filoda service.version
// placeholder DEĞİL ama SABİT ("hep aynı") — placeholder filtresi onu
// elemediğinden zincir image tag'e hiç düşmüyor ve gerçek rollout'lar
// (container.image.tag: release.20260707.1 → release.20260708.1)
// görünmez kalıyordu. Image tag mevcutsa deployment gerçeğinin
// kendisidir (tag değişimi ⇔ rollout); yoksa service.version, sonra
// Helm/k8s label'ları (v0.5.283 zinciri) devrededir — image-tag'siz
// kurulumlar (SDK-only) davranış değiştirmez.
const effectiveVersionExpr = `
  multiIf(
    res_values[indexOf(res_keys, 'container.image.tag')] NOT IN ` + placeholderVersionList + `,
      res_values[indexOf(res_keys, 'container.image.tag')],
    res_values[indexOf(res_keys, 'k8s.container.image.tag')] NOT IN ` + placeholderVersionList + `,
      res_values[indexOf(res_keys, 'k8s.container.image.tag')],
    res_values[indexOf(res_keys, 'service.version')] NOT IN ` + placeholderVersionList + `,
      res_values[indexOf(res_keys, 'service.version')],
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
	cutoff := time.Now().Add(-since)
	// v0.9.493 — MV hızlı yol: filo-geneli ham taramanın (aşağıda) iki
	// derdi birden burada kapanır. (1) Perf: banner her sayfanın 30s
	// poll'ünden ateşlenir; ham yol prod'da sürücünün 30s ReadTimeout'una
	// çarpıyordu. (2) Doğruluk: ham sorgunun HAVING first_seen >= cutoff
	// hükmü tarama penceresi == hüküm penceresi olduğu için TOTOLOJİYDİ
	// (v0.9.478'in /deploys'ta düzelttiği sınıf — banner/marker/problem
	// korelasyonu "pencerede yaşayan her sürümü" deploy sanıyordu). MV
	// yolu 24h lookback'le tarar, yalnız pencerede DOĞAN sürümler döner.
	// Ham fallback (48h MV ısınması) eski totolojik davranışıyla kalır —
	// yanlış-pozitif eski durumun aynısı, regresyon değil.
	if s.deployMVCovers(ctx, cutoff.Add(-deployJudgmentLookback)) {
		out, err := s.deploysInWindowFromMV(ctx, cutoff, time.Now(), limit)
		if err == nil {
			return out, nil
		}
		log.Printf("[deploys] recent MV path: %v — falling back to raw spans", err)
	}
	maxExecSec := 5
	if since > time.Hour {
		// v0.9.493 — 30/60s kademeleri sürücü ReadTimeout'unun (30s,
		// v0.8.340) üstündeydi; tavan 25s (GetDeploysInWindow ile aynı).
		maxExecSec = 25
	}
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
	rows, err := s.telemetryReadConn().Query(ctx, sql, cutoff, cutoff.UnixNano(), limit)
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
	Count     uint64  `json:"count"`
	RPS       float64 `json:"rps"`
	ErrorRate float64 `json:"errorRate"` // 0..1
	P99Ms     float64 `json:"p99Ms"`
	AvgMs     float64 `json:"avgMs"`
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
	row := s.telemetryReadConn().QueryRow(ctx, `
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
		         `+s.shardSkipSetting(),
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
		// v0.9.1095 (MCP smoke'un yakaladığı bug) — boş taraf penceresi:
		// quantileIf/avgIf boş kümede NaN döndürür; Go'nun json.Marshal'ı
		// NaN yazamaz ve Impact taşıyan TÜM cevap gövdesi "unsupported
		// value: NaN" ile düşer (get_deploy_diff'te göründü; Impact'i
		// gömen her API cevabı aynı sınıfta). NaN/Inf burada, kaynağında
		// sıfırlanır — delta hesapları zaten >0 kapılı, 0 tabanı "kıyas
		// yok" anlamını korur (Count=0 zaten yanında taşınıyor).
		st := DeployImpactStats{Count: c, P99Ms: nanToZero(p99), AvgMs: nanToZero(avg)}
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

// nanToZero — JSON-taşınabilirlik kapısı (saf, tablo testli):
// NaN/±Inf → 0. encoding/json bu değerleri YAZAMAZ; tek kaçak float
// tüm cevabı düşürür.
func nanToZero(f float64) float64 {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return f
}

// deployLookback (v0.9.205) — bir sürümün pencere İÇİNDE mi başladığını
// ayırt etmek için pencerenin gerisine bakılan tampon. 48h: günlük
// batch servislerinin sessiz aralıklarını da örter; service+time PK
// budaması ile taranan dilim servis-scoped kalır.
const deployLookback = 48 * time.Hour

// serviceDeploysSQL — GetServiceDeploys'un gövdesi; const olarak dışarıda
// ki deploys_test.go pencere-içi-başlangıç sözleşmesini (HAVING
// first_seen_ns >= ?) shape-assert edebilsin (v0.9.205 regresyon pini).
// Arg sırası: service, scanFrom(=from-deployLookback), to, fromNs.
const serviceDeploysSQL = `
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
		   AND first_seen_ns >= ?
		ORDER BY first_seen_ns ASC
		LIMIT 50
		SETTINGS max_execution_time = 15,
		         `

func (s *Store) GetServiceDeploys(
	ctx context.Context, service string, from, to time.Time,
) ([]Deploy, error) {
	// v0.5.278 — same placeholder filter + image-tag fallback
	// as GetRecentDeploys. The service detail page's Deploy
	// History panel was the loudest victim of the
	// "0.0.1-SNAPSHOT" Maven default — every restart looked
	// like a deploy.
	//
	// v0.9.205 (operator-reported) — SABİT sürüm sahte marker'ı:
	// image-tag'siz bir serviste zincir service.version'a düşer
	// ("2.1.6.Final-redhat-00001" gibi bir artifact sürümü, hiç
	// değişmez); pencere-içi min(time) her pencerenin SOL KENARINA
	// bir "deploy" işaretçisi basıyordu. Fix: 48h lookback'li tarama +
	// HAVING first_seen >= pencere-başı — yalnız pencere İÇİNDE
	// gerçekten BAŞLAYAN sürümler (gerçek rollout'lar) döner; sabit
	// sürüm lookback'te görünür olduğundan elenir. (GetRecentDeploys
	// banner'ında aynı sınıf vacuous filtre duruyor — ayrı iş.)
	// v0.9.249 — MV fast path when service_version_5m covers the whole
	// lookback; otherwise the raw-spans query below (see
	// deployMVCovers for why the coverage check matters).
	if s.deployMVCovers(ctx, from.Add(-deployLookback)) {
		out, err := s.serviceDeploysFromMV(ctx, service, from, to)
		if err == nil {
			return out, nil
		}
		// Soft-fail to the raw path: a malformed/absent MV must
		// degrade to the slower-but-correct query, not to an error.
		log.Printf("[deploys] MV path %s: %v — falling back to raw spans", service, err)
	}

	sql := serviceDeploysSQL + s.shardSkipSetting()
	rows, err := s.telemetryReadConn().Query(ctx, sql, service, from.Add(-deployLookback), to, from.UnixNano())
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
	TimeUnixNs    int64    `json:"timeUnixNs"`
	PodsAdded     int      `json:"podsAdded"`
	PodsRemoved   int      `json:"podsRemoved"`
	ActivePods    int      `json:"activePods"`            // active set size after the rollout
	AddedPods     []string `json:"addedPods,omitempty"`   // up to 5 sample ids
	RemovedPods   []string `json:"removedPods,omitempty"` // up to 5 sample ids
	VersionBefore string   `json:"versionBefore,omitempty"`
	// Kind (v0.8.405): "deploy" when the effective version changed
	// across the churn, "restart" when pods were replaced at the SAME
	// version (reschedule / node drain / crash-restart / HPA wave) —
	// the operator-reported false-deploy class. Deploy chips, markers
	// and impact analysis key on "deploy"; restarts render as their
	// own muted event type.
	Kind         string        `json:"kind"`
	VersionAfter string        `json:"versionAfter,omitempty"`
	Impact       *DeployImpact `json:"impact,omitempty"` // before/after RED, filled by the API layer
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
		         ` + s.shardSkipSetting()
	rows, err := s.telemetryReadConn().Query(ctx, sql, service, from, to)
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
	// v0.8.405 — lookback widened 2→3: presence smoothing (below)
	// carries bucket i-1 into i, which delays a cutover's "old pods
	// gone" signal by one bucket; a 2-bucket baseline then lands ON
	// the mixed transition bucket (new pods already in base → added=0,
	// detection lost). 3 buckets straddles smoothing + transition.
	const lookback = 3 // ≈ a 15-minute rollout window
	lastRolloutIdx := -100
	for i := 1; i < len(buckets); i++ {
		// v0.8.405 (operator-reported: "deploy yapılmasa da deploy
		// algılıyor") — bucket presence means "emitted spans that 5
		// minutes", NOT liveness: a pod quiet for one bucket (low
		// traffic, collector restart, sampling burst) read as
		// "removed", then "added" when it spoke again — a fabricated
		// rollout. Smooth presence over [i-1, i]: one quiet bucket no
		// longer counts as a removal; a real rollout (pods actually
		// replaced for ≥2 buckets) still trips the wider baseline.
		cur := podSetSmoothed(buckets, i)
		if len(cur) == 0 {
			continue
		}
		j := i - lookback
		if j < 0 {
			j = 0
		}
		base := podSetSmoothed(buckets, j)
		if len(base) == 0 {
			continue
		}
		added := podDiff(cur, base)      // in cur, not in baseline
		removed := podDiff(base, cur)    // in baseline, not in cur
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
		r.Kind = "restart"
		if buckets[i].version != "" && buckets[j].version != "" && buckets[i].version != buckets[j].version {
			r.VersionBefore = buckets[j].version
			r.VersionAfter = buckets[i].version
			r.Kind = "deploy"
		}
		// v0.9.451 (operator-reported: /deploys'ta olmamış restart —
		// web-fund-bff-pilot 08:30 "+1/-1", OpenShift'te 4 pod da 21
		// Mayıs'tan beri 0 restart'la Running) — bucket varlığı canlılık
		// değil "o 5 dakikada span üretti"dir; ~0.002 core'luk pilot
		// pod 10+ dk susunca tek-bucket yumuşatma (v0.8.405) yetmiyor:
		// susan pod "gitti", yeniden konuşan pod "geldi" okunuyordu.
		// Ayırt edici sinyal pod adındaki ReplicaSet template-hash'i:
		// GERÇEK rollout yeni RS hash'i üretir (k8s pod adı
		// <deploy>-<rs-hash>-<rand5>); eklenen ve giden pod'lar AYNI
		// hash'teyse şablon değişmemiştir → rollout/restart yok,
		// telemetri flıker'ı. Sürüm DEĞİŞTİYSE guard atlanır (deploy
		// kanıtı hash'ten güçlü); hash çıkarılamayan adlarda (host adı,
		// UUID) guard devre dışı — eski davranış.
		if r.Kind == "restart" && sameTemplateHash(added, removed) {
			lastRolloutIdx = i // aynı flıker kuyruğu yeni satır üretmesin
			continue
		}
		res.Rollouts = append(res.Rollouts, r)
		lastRolloutIdx = i
	}
	return res
}

// podSetSmoothed is bucket i's pod set unioned with bucket i-1's
// (v0.8.405): presence = "spoke within the last ~10 minutes", so a
// single quiet bucket can't fabricate a removal. Pure — table-tested.
func podSetSmoothed(buckets []rolloutBucket, i int) map[string]struct{} {
	cur := podSet(buckets[i].pods)
	if i > 0 {
		for p := range podSet(buckets[i-1].pods) {
			cur[p] = struct{}{}
		}
	}
	return cur
}

// podTemplateHash extracts the ReplicaSet pod-template-hash segment
// from a k8s pod name (<deploy>-<rs-hash>-<rand5>): son segment 5
// karakterlik random sonekse sondan ikinci segment hash'tir.
// DeploymentConfig adlarında (<name>-<deployNo>-<rand>) aynı kural
// deploy numarasını yakalar — o da rollout'ta değişir. Uymayan adlar
// (host.name, service.instance.id ilk-8) "" döner → guard devre dışı.
// Saf — tablo-testli.
func podTemplateHash(pod string) string {
	parts := strings.Split(pod, "-")
	if len(parts) < 3 {
		return ""
	}
	last := parts[len(parts)-1]
	if len(last) != 5 || !isLowerAlnum(last) {
		return ""
	}
	hash := parts[len(parts)-2]
	if hash == "" || !isLowerAlnum(hash) {
		return ""
	}
	return hash
}

func isLowerAlnum(s string) bool {
	for _, c := range s {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return len(s) > 0
}

// sameTemplateHash — eklenen ve giden pod kümeleri AYNI template-hash
// kümesini paylaşıyor mu? Guard yalnız İKİ taraf da tamamen
// çıkarılabilir olduğunda uygulanır (kısmi bilgiyle bastırma =
// kaçırılmış gerçek rollout riski).
func sameTemplateHash(added, removed []string) bool {
	ah, ok := hashSet(added)
	if !ok {
		return false
	}
	rh, ok := hashSet(removed)
	if !ok {
		return false
	}
	if len(ah) != len(rh) {
		return false
	}
	for h := range ah {
		if _, in := rh[h]; !in {
			return false
		}
	}
	return true
}

func hashSet(pods []string) (map[string]struct{}, bool) {
	out := make(map[string]struct{}, len(pods))
	for _, p := range pods {
		h := podTemplateHash(p)
		if h == "" {
			return nil, false
		}
		out[h] = struct{}{}
	}
	return out, len(out) > 0
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

// ── service_version_5m fast path (v0.9.249) ─────────────────────────

// serviceVersionMVSQL mirrors serviceDeploysSQL's contract against the
// rollup: same HAVING first_seen >= window-start gate (v0.9.205
// phantom-marker fix), same ordering, same cap. Arg order: service,
// scanFrom(=from-deployLookback), to, fromNs.
const serviceVersionMVSQL = `
	SELECT version,
	       toUnixTimestamp64Nano(minMerge(first_seen_state)) AS first_seen_ns,
	       countMerge(span_count_state)                      AS span_count
	FROM service_version_5m
	WHERE service_name = ?
	  AND time_bucket >= ? AND time_bucket < ?
	GROUP BY version
	HAVING version != ''
	   AND first_seen_ns >= ?
	ORDER BY first_seen_ns ASC
	LIMIT 50
	SETTINGS max_execution_time = 10, `

func (s *Store) serviceDeploysFromMV(
	ctx context.Context, service string, from, to time.Time,
) ([]Deploy, error) {
	rows, err := s.telemetryReadConn().Query(ctx, serviceVersionMVSQL+s.shardSkipSetting(),
		service, from.Add(-deployLookback), to, from.UnixNano())
	if err != nil {
		return nil, fmt.Errorf("query service_version_5m: %w", err)
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

// deployMVCovers reports whether service_version_5m holds data going
// back at least as far as `need`.
//
// This gate is the whole reason the MV can ship without a backfill. A
// materialized view only populates from inserts made AFTER it exists,
// so for the first deployLookback (48h) after an upgrade the lookback
// window is partially empty — and a version that was already running
// would have no rows before `from`, making it look like it STARTED
// in the window. That is precisely the v0.9.205 phantom-deploy-marker
// bug, and reading the MV early would re-open it for two days. So
// until the MV's own history spans the lookback, reads stay on the
// raw-spans path (which v0.9.244 already moved off the request's
// critical path).
//
// The probe is GLOBAL, not per-service, on purpose: a service that
// genuinely started emitting an hour ago has a recent min(time_bucket)
// through no fault of the MV, and a per-service check would pin every
// young service to the slow path forever. What we actually want to
// know is how long the MV itself has been running.
//
// v0.9.493 — LATCH KALDIRILDI (verify bulgusu). Eski hâli ilk başarılı
// probe'dan sonra `need`'i HİÇ karşılaştırmadan true dönüyordu; tek
// kapı hem 48h'lik (GetServiceDeploys) hem 24h'lik (pencere/banner)
// ihtiyaçlara hizmet etmeye başlayınca bu iki gerçek hataya dönüştü:
// (1) 24h'lik bir probe MV 24.5 saatlikken kilidi kapatıp 48h'lik yolu
// da erken açıyordu (24-48h sessiz servislerde v0.9.205 hayalet-marker
// sınıfı geri gelirdi), (2) kilitli kapı MV'nin DOĞUMUNDAN eski
// tarihsel pencereleri MV'ye yolluyordu — tamamen-eski pencere boş
// "deploy yok" dönerdi (raw fallback hiç devreye girmeden). Şimdi:
// probe'un EN ESKİ değeri önbelleklenir, her çağrı kendi need'ini onunla
// karşılaştırır. 10 dakikalık tazeleme TTL trim'ini de izler (45 gün
// sonra earliest İLERLER); başarısız probe 1 dakikalık geri çekilmeyle
// tekrar denenir.
// deployMVCoverageProbeSQL — hoisted as a const so deploys_mv_test.go can
// pin WHICH column the gate trusts (v0.9.250).
const deployMVCoverageProbeSQL = `
	SELECT minMerge(first_seen_state) FROM service_version_5m
	SETTINGS max_execution_time = 5, `

// deployMVProbeRefresh — earliest önbelleğinin tazelik penceresi.
const deployMVProbeRefresh = 10 * time.Minute

// deployMVCoverageAnswers — saf karşılaştırma: earliest bilinmiyorsa
// (zero) kapı kapalı; biliniyorsa yalnız need'i kapsıyorsa açık.
func deployMVCoverageAnswers(earliest, need time.Time) bool {
	return !earliest.IsZero() && !earliest.After(need)
}

func (s *Store) deployMVCovers(ctx context.Context, need time.Time) bool {
	s.deployMVMu.Lock()
	defer s.deployMVMu.Unlock()
	fresh := !s.deployMVProbedAt.IsZero() && time.Since(s.deployMVProbedAt) < deployMVProbeRefresh
	// Başarılı probe taze ise sorgusuz cevapla; başarısız (zero) probe
	// 1 dakika geri çekilir ki her deploys okuması warm-up sırasında
	// fazladan sorgu ödemesin.
	if fresh && !s.deployMVEarliest.IsZero() {
		return deployMVCoverageAnswers(s.deployMVEarliest, need)
	}
	if s.deployMVEarliest.IsZero() && !s.deployMVProbedAt.IsZero() && time.Since(s.deployMVProbedAt) < time.Minute {
		return false
	}
	s.deployMVProbedAt = time.Now()

	// minMerge(first_seen_state), NOT min(time_bucket) — v0.9.250.
	// The bucket LABEL overstates coverage by up to one bucket width: a
	// span ingested at 14:58 lands in the bucket named 14:55, so a
	// freshly-created MV reports coverage from 14:55 while its earliest
	// real datum is 14:58. The state's own min is exact.
	var earliest time.Time
	err := s.telemetryReadConn().QueryRow(ctx,
		deployMVCoverageProbeSQL+s.shardSkipSetting()).Scan(&earliest)
	if err != nil {
		// Table missing (pre-migration binary, or an operator dropped
		// it) or CH blip — stay on the raw path, which is always
		// correct.
		return false
	}
	if earliest.IsZero() {
		// MV exists but has no rows yet.
		return false
	}
	if s.deployMVEarliest.IsZero() {
		log.Printf("[deploys] service_version_5m earliest datum %s — MV path serves windows starting after it",
			earliest.UTC().Format(time.RFC3339))
	}
	s.deployMVEarliest = earliest
	return deployMVCoverageAnswers(earliest, need)
}

// GetDeploysInWindow (v0.9.435) — GetRecentDeploys'un PENCERE-sınırlı
// ikizi (/deploys sayfası için). GetRecentDeploys [since, ŞİMDİ] tarar
// ve en yeni N'i tutar — geçmiş bir custom pencerede (to << now) o N
// satırın tamamı to'dan sonra kalıp kırpmada elenebiliyordu (sayfa
// dolu pencerede boş görünürdü — verify bulgusu). Burada hem taramanın
// hem HAVING'in sağ ucu to'ya bağlı; LIMIT pencere İÇİNDE uygulanır.
// Placeholder filtresi + effectiveVersion zinciri birebir aynı.
// deployJudgmentLookback (v0.9.478) — "bu pencerede DOĞDU" hükmü için
// geriye bakış ufku. Operator-reported: 15 dakikalık pencerede tüm filo
// "deploy" görünüyordu — tarama penceresi hüküm penceresiyle AYNI
// olunca min(time) her aktif sürüm için kaçınılmaz pencere içindeydi ve
// HAVING first_seen >= from TOTOLOJİYDİ: sayfa "pencerede yaşayan her
// sürümü" listeliyordu, "pencerede doğan"ı değil. Tarama artık 24 saat
// geriden başlar; ufuktan önce de koşan sürümün first_seen'i from'un
// altına düşer ve elenir. Bilinen kenar: 24+ saat susup aynı sürümle
// geri gelen servis "deploy" sayılır — operasyonel olarak da öyledir
// (bir gün sonra aynı imajı geri açmak bir yayındır).
const deployJudgmentLookback = 24 * time.Hour

// deploysWindowMVSQL (v0.9.493) — GetDeploysInWindow'un MV hızlı yolu:
// serviceVersionMVSQL'in (v0.9.249) FİLO-geneli ikizi. Operator-reported:
// prod'da /deploys 30 saniyede timeout — v0.9.478 lookback'i tarama
// penceresini +24h yapınca filo-geneli HAM spans taraması (satır başına
// 14 indexOf'lu effectiveVersionExpr) sürücünün 30s ReadTimeout'unu
// aşıyordu. MV okuma (service, version, bucket) satırlarına iner.
// Kenar notları (hepsi ≤5dk, 5 dakikalık bucket granülaritesi): span_count
// from'u İÇEREN bucket'ın tamamını sayar (fazla sayım); tarama sınırları
// bucket etiketiyle kesildiğinden to'yu içeren bucket'ta doğan bir sürüm
// first_seen > to ile dönebilir, 24h ufku da ≤5dk kayar. first_seen
// DEĞERİ minMerge ile kesindir (v0.9.250); bulanık olan zaten keyfi 24h
// hüküm ufkudur — kabul edilen yaklaşıklıklar (v0.9.493 verify).
//
// v0.9.1172 — bu notun BİR YARISI kabul edilmiş bir yaklaşıklık değil, düpedüz
// fazla kovaydı ve bedavaya kalkıyor. Üst sınır `<=`ten `<`e döndü: `to` tam
// kova sınırına oturduğunda `<= to` başlangıcı tam to olan kovayı alıyordu,
// yani pencereden SONRA doğan bir sürüm (first_seen ∈ [to, to+5dk)) HAVING'in
// alt kapısını geçip "bu pencerede dağıtıldı" diye listeleniyor, span_count'un
// sumIf'i de o kovayı sayıyordu.
//
// Notun AYAKTA kalan yarısı: hizalanmamış bir `to`'da (11:02) to'yu İÇEREN kova
// (11:00) hâlâ alınır ve içinde 11:01'de doğan bir sürüm first_seen > to ile
// dönebilir. O gerçekten kova granülaritesinin bedeli; from'u içeren kovanın
// tamamen sayılması ve 24h ufkunun ≤5dk kayması da öyle. Değişen tek şey,
// granülariteyle GEREKÇELENDİRİLEMEYEN fazladan bir kovanın kalkması.
const deploysWindowMVSQL = `
	SELECT
	  service_name,
	  version,
	  toUnixTimestamp64Nano(minMerge(first_seen_state)) AS first_seen,
	  toUInt64(sumIf(finalizeAggregation(span_count_state),
	                 time_bucket >= toStartOfInterval(?, INTERVAL 5 MINUTE))) AS span_count
	FROM service_version_5m
	WHERE time_bucket >= ? AND time_bucket < ?
	GROUP BY service_name, version
	HAVING version != ''
	   AND first_seen >= ?
	ORDER BY first_seen DESC
	LIMIT ?
	SETTINGS max_execution_time = 15, `

func (s *Store) deploysInWindowFromMV(ctx context.Context, from, to time.Time, limit int) ([]RecentDeployEntry, error) {
	rows, err := s.telemetryReadConn().Query(ctx, deploysWindowMVSQL+s.shardSkipSetting(),
		from, from.Add(-deployJudgmentLookback), to, from.UnixNano(), limit)
	if err != nil {
		return nil, fmt.Errorf("query service_version_5m window: %w", err)
	}
	defer rows.Close()
	out := []RecentDeployEntry{}
	for rows.Next() {
		var e RecentDeployEntry
		if err := rows.Scan(&e.Service, &e.Version, &e.FirstSeenNs, &e.SpanCount); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) GetDeploysInWindow(ctx context.Context, from, to time.Time, limit int) ([]RecentDeployEntry, error) {
	if limit <= 0 || limit > 2000 {
		limit = 200
	}
	scanFrom := from.Add(-deployJudgmentLookback)
	// v0.9.493 — MV hızlı yol; kapı + soft-fail v0.9.249 deseniyle aynı
	// (kapsama yetmiyorsa ya da MV bozuksa ham yol her zaman doğru).
	if s.deployMVCovers(ctx, scanFrom) {
		out, err := s.deploysInWindowFromMV(ctx, from, to, limit)
		if err == nil {
			return out, nil
		}
		log.Printf("[deploys] window MV path: %v — falling back to raw spans", err)
	}
	horizon := to.Sub(scanFrom)
	maxExecSec := 5
	if horizon > time.Hour {
		// v0.9.493 — tavan 25s: eski 30/60s kademeleri sürücünün 30s
		// ReadTimeout'unun (v0.8.340) ÜSTÜNDEYDİ — sunucu sorguya devam
		// ederken istemci 30. saniyede bağlantıyı kesiyordu (operatörün
		// gördüğü "30 saniye sonra timeout" tam buydu). Taşımanın
		// veremeyeceği süreyi vaat etme; ham yol artık yalnız MV
		// ısınma penceresinin (48h) yedeği.
		maxExecSec = 25
	}
	sql := fmt.Sprintf(`
		SELECT
		  service_name,
		  `+effectiveVersionExpr+` AS version,
		  toUnixTimestamp64Nano(min(time)) AS first_seen,
		  countIf(time >= ?) AS span_count
		FROM spans
		WHERE time >= ? AND time <= ?
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
	rows, err := s.telemetryReadConn().Query(ctx, sql, from, scanFrom, to, from.UnixNano(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RecentDeployEntry{}
	for rows.Next() {
		var e RecentDeployEntry
		if err := rows.Scan(&e.Service, &e.Version, &e.FirstSeenNs, &e.SpanCount); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
