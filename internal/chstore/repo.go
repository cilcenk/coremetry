package chstore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
)

// safeF returns the dereferenced float64, treating nil, NaN, and
// ±Inf as 0. v0.5.301 — Operator-reported: at scale, MV queries
// can return NaN from quantilesMerge() on empty/edge-case
// AggregatingMergeTree states, or from divide-by-zero in
// downstream expressions. encoding/json rejects NaN+Inf per
// RFC 8259, so the operations bundle's JSON marshal step
// 500'd silently — frontend saw "operations: null" → "No
// operations" empty state even though the MV path served rows.
// Apply this everywhere we Scan float64 pointers out of CH.
func safeF(p *float64) float64 {
	if p == nil {
		return 0
	}
	if math.IsNaN(*p) || math.IsInf(*p, 0) {
		return 0
	}
	return *p
}

// ── Batch inserts ─────────────────────────────────────────────────────────────

// asyncInsertCtx wraps the caller's context with ClickHouse
// async_insert settings. async_insert lets the server coalesce
// concurrent INSERTs from our parallel flusher pool into single
// disk writes, reducing per-insert overhead at high throughput.
// wait_for_async_insert=1 keeps client-side semantics synchronous
// (the call doesn't return until the server has buffered the rows
// for durability), so we still detect insert errors properly.
//
// v0.5.346 — tuned for high-TPS ingestion:
//   • async_insert_max_data_size = 10MB  (up from 1MB default)
//     — bigger coalescence per server-side flush, fewer disk
//     writes per row at burst peaks.
//   • async_insert_busy_timeout_ms = 1000  (up from 200ms)
//     — wait up to 1s collecting concurrent inserts before
//     flushing. Trade a tiny visibility lag for far fewer
//     write operations at sustained load.
//   • async_insert_stale_timeout_ms = 1000
//     — flush a partial buffer 1s after the last insert so
//     low-traffic services (test envs, off-hours) don't sit
//     on rows indefinitely.
func asyncInsertCtx(ctx context.Context) context.Context {
	return clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{
		"async_insert":                      1,
		"wait_for_async_insert":             1,
		"async_insert_max_data_size":        10_485_760,
		"async_insert_busy_timeout_ms":      1000,
		"async_insert_stale_timeout_ms":     1000,
	}))
}

// spansInsertColumns is the EXPLICIT, physically-ordered column list for the
// spans INSERT EXCEPT the trailing op_group column (which is conditional —
// see InsertSpans) and the materialized `cluster` column (which is NEVER in
// an INSERT). The order MUST match the spans CREATE TABLE in store.go
// (trace_id … scope_name) AND the value order built in spanAppendArgs.
// v0.8.186 — moved from a positional `INSERT INTO spans` to a named column
// list (the metric_points pattern) so op_group can be dropped per
// s.hasOpGroupCol without misaligning every other column. A named INSERT also
// fails loudly on a stale schema instead of silently writing into the wrong
// column.
var spansInsertColumns = []string{
	"trace_id", "span_id", "parent_id", "name", "kind",
	"service_name", "host_name", "deploy_env", "status_code", "status_msg",
	"time", "duration",
	"db_system", "db_statement", "http_method", "http_route", "http_status",
	"rpc_system", "rpc_method", "peer_service", "msg_system",
	"attr_keys", "attr_values", "res_keys", "res_values",
	"events", "scope_name",
}

// spanAppendArgs builds the positional Append() arguments for one span in the
// EXACT order of spansInsertColumns, optionally appending op_group LAST when
// withOpGroup is true. Keeping the column list and the value list in one place
// (this function + spansInsertColumns) is the single guarantee against
// positional drift: op_group is appended to both, or to neither, in lockstep.
func spanAppendArgs(sp *Span, withOpGroup bool) []any {
	args := []any{
		sp.TraceID, sp.SpanID, sp.ParentID, sp.Name, sp.Kind,
		sp.ServiceName, sp.HostName, sp.DeployEnv, sp.StatusCode, sp.StatusMsg,
		sp.Time, sp.Duration,
		sp.DBSystem, sp.DBStatement, sp.HTTPMethod, sp.HTTPRoute, sp.HTTPStatus,
		sp.RPCSystem, sp.RPCMethod, sp.PeerService, sp.MsgSystem,
		sp.AttrKeys, sp.AttrValues, sp.ResKeys, sp.ResValues,
		sp.Events, sp.ScopeName,
	}
	if withOpGroup {
		args = append(args, sp.OpGroup)
	}
	return args
}

// spansInsertColumnNames returns the ordered column list the INSERT will use,
// appending op_group as the LAST column iff withOpGroup. Pure helper shared by
// spansInsertSQL and the alignment regression test (v0.8.186).
func spansInsertColumnNames(withOpGroup bool) []string {
	if !withOpGroup {
		return spansInsertColumns
	}
	return append(append([]string{}, spansInsertColumns...), "op_group")
}

// spansInsertSQL builds the `INSERT INTO spans (col, col, …)` statement.
func spansInsertSQL(withOpGroup bool) string {
	return "INSERT INTO spans (" + strings.Join(spansInsertColumnNames(withOpGroup), ", ") + ")"
}

func (s *Store) InsertSpans(ctx context.Context, spans []*Span) error {
	ctx = asyncInsertCtx(ctx)
	// op_group is bound ONLY when the column is actually present on the table
	// the write fans out to (s.hasOpGroupCol, probed once at boot). On an
	// external Distributed install where the ALTER never reached spans_local
	// (v0.8.186), hasOpGroupCol is false: the column list AND the per-row
	// value list both drop op_group, so the INSERT matches the real schema
	// and ingest survives. The monolithic / cluster-name-set path keeps
	// hasOpGroupCol=true → byte-identical column+value layout to pre-v0.8.186.
	withOpGroup := s.hasOpGroupCol
	batch, err := s.conn.PrepareBatch(ctx, spansInsertSQL(withOpGroup))
	if err != nil {
		return fmt.Errorf("prepare spans: %w", err)
	}
	for _, sp := range spans {
		if err := batch.Append(spanAppendArgs(sp, withOpGroup)...); err != nil {
			return fmt.Errorf("append span: %w", err)
		}
	}
	return batch.Send()
}

func (s *Store) InsertLogs(ctx context.Context, logs []*Log) error {
	ctx = asyncInsertCtx(ctx)
	batch, err := s.conn.PrepareBatch(ctx, "INSERT INTO logs")
	if err != nil {
		return fmt.Errorf("prepare logs: %w", err)
	}
	for _, l := range logs {
		if err := batch.Append(
			l.TraceID, l.SpanID, l.Time, l.SeverityNum, l.SeverityText,
			l.Body, l.ServiceName, l.HostName,
			l.AttrKeys, l.AttrValues, l.ResKeys, l.ResValues, l.ScopeName,
		); err != nil {
			return fmt.Errorf("append log: %w", err)
		}
	}
	return batch.Send()
}

// metricsInsertSQL builds the named `INSERT INTO metric_points (…)`
// statement. v0.5.358 introduced the explicit column list (clear error on a
// stale schema instead of silent positional drift); v0.8.328 makes
// series_fingerprint CONDITIONAL on s.hasSeriesFpCol — on an external
// Distributed install where the ALTER never reached metric_points_local,
// binding it would kill every metric batch with code 16 (the op_group /
// v0.8.186 failure shape). Pure so the column/value alignment is unit-tested.
func metricsInsertSQL(withSeriesFp bool) string {
	sql := `INSERT INTO metric_points
		(metric, instrument, description, unit,
		 service_name, host_name, time, start_time,
		 value, count, sum_value, min_value, max_value,
		 attr_keys, attr_values, res_keys, res_values,
		 bucket_bounds, bucket_counts, temporality`
	if withSeriesFp {
		sql += ", series_fingerprint"
	}
	return sql + ")"
}

// metricAppendArgs builds the positional Append() arguments for one metric
// point in the EXACT order of metricsInsertSQL, appending series_fingerprint
// LAST iff withSeriesFp — the same lockstep guarantee spanAppendArgs gives
// the spans INSERT (v0.8.186 discipline).
func metricAppendArgs(p *MetricPoint, withSeriesFp bool) []any {
	// Histogram-only payloads come through with arrays
	// populated; everything else uses empty slices so the
	// CH default-array behaviour kicks in.
	bounds := p.BucketBounds
	counts := p.BucketCounts
	if bounds == nil {
		bounds = []float64{}
	}
	if counts == nil {
		counts = []uint64{}
	}
	args := []any{
		p.Metric, p.Instrument, p.Description, p.Unit,
		p.ServiceName, p.HostName, p.Time, p.StartTime,
		p.Value, p.Count, p.SumValue, p.MinValue, p.MaxValue,
		p.AttrKeys, p.AttrValues, p.ResKeys, p.ResValues,
		bounds, counts, p.Temporality,
	}
	if withSeriesFp {
		args = append(args, p.SeriesFingerprint)
	}
	return args
}

func (s *Store) InsertMetrics(ctx context.Context, pts []*MetricPoint) error {
	ctx = asyncInsertCtx(ctx)
	// series_fingerprint is bound ONLY when the column is actually present
	// on the table the write fans out to (s.hasSeriesFpCol, probed once at
	// boot) — see metricsInsertSQL for the failure mode this prevents.
	withSeriesFp := s.hasSeriesFpCol
	batch, err := s.conn.PrepareBatch(ctx, metricsInsertSQL(withSeriesFp))
	if err != nil {
		return fmt.Errorf("prepare metrics: %w", err)
	}
	for _, p := range pts {
		if err := batch.Append(metricAppendArgs(p, withSeriesFp)...); err != nil {
			return fmt.Errorf("append metric: %w", err)
		}
	}
	return batch.Send()
}

// ── Service queries ───────────────────────────────────────────────────────────

// GetServices returns aggregate stats per service for the requested window.
// Pass `since` for a relative window (now-since … now), or non-zero `from`/`to`
// for an absolute window (overrides since).
func (s *Store) GetServices(ctx context.Context, since time.Duration, from, to time.Time) ([]ServiceSummary, error) {
	return s.GetServicesFilteredIn(ctx, since, from, to, "", nil, "", "", 0, 0, "", "")
}

// GetServicesFiltered keeps the prior surface intact (no
// service-name allowlist). The newer GetServicesFilteredIn
// is the variant the API uses when the operator filtered by
// owner / SRE team.
func (s *Store) GetServicesFiltered(ctx context.Context, since time.Duration, from, to time.Time, nameMatch, sort, dir string, limit, offset int) ([]ServiceSummary, error) {
	return s.GetServicesFilteredIn(ctx, since, from, to, nameMatch, nil, sort, dir, limit, offset, "", "")
}

// servicesSortExpr maps a UI-side sort key to a CH ORDER BY
// fragment for the raw-scan GetServicesFiltered path. The
// whitelist is the only sanitisation — never interpolate the
// raw key into SQL. `dir` is normalised to ASC / DESC; unknown
// values fall through to DESC.
func servicesSortExpr(sort, dir string) string {
	col := "span_count"
	switch sort {
	case "name":
		col = "service_name"
	case "spans", "span_count", "spanCount":
		col = "span_count"
	case "errorCount", "errors", "error_count":
		col = "error_count"
	case "errorRate", "error_rate":
		// Avoid div-by-zero ordering surprises by using
		// nullIf inside the expression — services with zero
		// spans land at the bottom on either direction.
		col = "(error_count / nullIf(span_count, 0))"
	case "avg", "avg_ms":
		col = "avg_ms"
	case "p99", "p99_ms":
		col = "p99_ms"
	case "apdex":
		col = "apdex"
	}
	d := "DESC"
	if dir == "asc" || dir == "ASC" {
		d = "ASC"
	}
	return col + " " + d + " NULLS LAST"
}

// clusterDeriveExpr is the canonical "which cluster did this
// span come from" expression. Banks running OTel SDKs with
// different resource-attribute conventions emit the cluster
// name under any of three keys, sometimes as a resource attr
// (cluster-level, set at SDK init) and sometimes as a span
// attr (per-operation overrides). We coalesce across all six
// permutations, resource-first since that's the stable case.
//
// Returns '' when no signal is present — callers comparing
// against an empty string skip the row, which is the right
// behaviour (no-cluster rows belong only in the "All
// clusters" view).
const clusterDeriveExpr = `coalesce(
	nullIf(res_values[indexOf(res_keys, 'k8s.cluster.name')], ''),
	nullIf(res_values[indexOf(res_keys, 'openshift.cluster.name')], ''),
	nullIf(res_values[indexOf(res_keys, 'cluster')], ''),
	nullIf(attr_values[indexOf(attr_keys, 'k8s.cluster.name')], ''),
	nullIf(attr_values[indexOf(attr_keys, 'openshift.cluster.name')], ''),
	nullIf(attr_values[indexOf(attr_keys, 'cluster')], ''),
	''
)`

// clusterColExpr reads the promoted `cluster` MATERIALIZED column
// (v0.8.x) when present — new parts compute+store it at insert — and
// falls back to the live clusterDeriveExpr array scan for old parts
// that predate the column add (those read '' for the materialized
// col). CH short-circuits coalesce, so new parts never pay the
// indexOf() scan over res_values/attr_values; old parts pay it once,
// until they TTL out of the retention window, after which every part
// carries the column. The SAME clusterDeriveExpr const is embedded
// here as the column's MATERIALIZED expression (store.go) so new and
// old parts always derive identical cluster names — no drift.
const clusterColExpr = `coalesce(nullIf(cluster, ''), ` + clusterDeriveExpr + `)`

// clusterExpr returns the SQL expression that yields a span's cluster name.
// When the materialized `cluster` column is resolvable on the read path
// (s.hasClusterCol, probed once at boot) it uses the COMPLETE clusterColExpr:
// the cheap column read with the derive as a coalesce fallback for
// pre-v0.8.132 parts. When the column is absent — an external Distributed
// cluster where chstore never added it to spans_local — it uses the pure
// res/attr derive so the query never references a column the per-shard
// table lacks (which fails with code 47). Every cluster query path goes
// through this so the two modes can't drift. (v0.8.162.)
func (s *Store) clusterExpr() string {
	if s.hasClusterCol {
		return clusterColExpr
	}
	return clusterDeriveExpr
}

// GetServiceClusterMap returns one entry per service with the
// distinct cluster names it ran in during the last `since`
// window. Used to enrich Problems / Anomalies / Incidents at
// read time so the operator sees which cluster(s) the firing
// service spans — same service can run across 3+ clusters
// simultaneously (eu-west / eu-central / us-east) and a
// problem on one might not affect the others.
//
// Single batched query — N+1-free regardless of problem count.
// Capped at 1000 services × 50 clusters as a defensive bound;
// well above any realistic bank-scale deployment.
//
// Cached 60s per `since` (v0.8.359, perf P2-C): this raw-spans
// GROUP BY measured 120-220ms and re-ran on every problems /
// inbox / incidents / anomalies recompute. Cluster membership is
// infrastructure-stable, so a minute of staleness is invisible.
// Single-entry cache keyed by since — the enrichment callers all
// pass time.Hour, so a variable-window caller (service map)
// simply misses without thrashing them. The cached map is
// returned SHARED: callers must treat it as read-only (all
// current callers only index into it).
func (s *Store) GetServiceClusterMap(ctx context.Context, since time.Duration) (map[string][]string, error) {
	if since == 0 {
		since = 1 * time.Hour
	}
	s.clusterMapMu.RLock()
	if s.clusterMapVal != nil && s.clusterMapFor == since &&
		time.Since(s.clusterMapAt) < clusterMapCacheTTL {
		v := s.clusterMapVal
		s.clusterMapMu.RUnlock()
		return v, nil
	}
	s.clusterMapMu.RUnlock()
	from := time.Now().Add(-since)
	rows, err := s.conn.Query(ctx, `
		SELECT service_name, `+s.clusterExpr()+` AS cluster
		FROM spans
		WHERE time >= ? AND service_name != ''
		GROUP BY service_name, cluster
		HAVING cluster != ''
		ORDER BY service_name, cluster
		LIMIT 50000
		SETTINGS max_execution_time = 8`+heavyScanSpill, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var svc, cl string
		if err := rows.Scan(&svc, &cl); err != nil {
			continue
		}
		out[svc] = append(out[svc], cl)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	// Replace, never mutate — a reader holding the old snapshot
	// stays consistent (same discipline as the alertRules cache).
	s.clusterMapMu.Lock()
	s.clusterMapAt = time.Now()
	s.clusterMapFor = since
	s.clusterMapVal = out
	s.clusterMapMu.Unlock()
	return out, nil
}

// clusterScanWindow bounds how far back a cluster-enumeration scan
// reaches. The k8s/openshift cluster set is infrastructure-stable —
// a cluster that runs observable services emits spans every few
// minutes — so enumerating "which clusters exist" never needs to
// scan the operator's full selected range. On an external Distributed
// `spans` with cluster_name unset (no materialized `cluster` column),
// the only available expression is the per-row res/attr indexOf derive
// (clusterDeriveExpr); running it across a 24h window of a billion-
// span/day cluster blows max_execution_time = 8 → code 159
// TIMEOUT_EXCEEDED (v0.8.188 — operator-reported: the `clusters` cache
// warmer timed out every tick). Capping the scan at the most recent
// hour keeps the derive within budget. The filter itself is unaffected
// — clusterExpr() resolves any cluster name over any window on the
// read path, so a cluster the operator already knows stays selectable.
const clusterScanWindow = time.Hour

// clampClusterFrom returns the earliest scan timestamp for cluster
// enumeration: never more than clusterScanWindow before `to`. Pure so
// the budget-clamp is regression-testable without a live CH.
func clampClusterFrom(from, to time.Time) time.Time {
	if earliest := to.Add(-clusterScanWindow); from.Before(earliest) {
		return earliest
	}
	return from
}

// ListClusters returns the distinct cluster names observed in
// the window, sourced from the same resource/attr coalesce
// chain the filter uses. Drives the cluster-filter dropdown on
// /services and /service?name=. Capped at 200 — beyond that
// the dropdown is unusable anyway, and the operator can type
// the cluster directly into the filter URL.
func (s *Store) ListClusters(ctx context.Context, from, to time.Time) ([]string, error) {
	if from.IsZero() {
		from = time.Now().Add(-1 * time.Hour)
	}
	if to.IsZero() {
		to = time.Now()
	}
	// Bound the scan to a recent window — the cluster set is infra-stable,
	// so a wider range only burns CH read bandwidth (and on an external
	// Distributed cluster, blows the 8s budget on the derive). See
	// clusterScanWindow.
	from = clampClusterFrom(from, to)
	// Primary: the COMPLETE clusterColExpr (column for new parts, derive for old).
	// CH short-circuits the coalesce, so post-migration — once every part carries
	// the materialized `cluster` column (v0.8.132) — the derive (res_values/
	// attr_values indexOf) is never evaluated and this is just the cheap column
	// read. During the migration window it additionally derives clusters that
	// live ONLY in pre-column parts, so the list is never missing a real cluster.
	// On err==nil we return even an empty result (genuinely no clusters).
	if names, err := s.scanClusters(ctx, `
		SELECT `+s.clusterExpr()+` AS cluster
		FROM spans
		WHERE time >= ? AND time <= ?
		GROUP BY cluster
		HAVING cluster != ''
		ORDER BY cluster
		LIMIT 200
		SETTINGS max_execution_time = 8`, from, to); err == nil {
		return names, nil
	} else if !s.hasClusterCol {
		// No `cluster` column on the read path (external Distributed cluster):
		// the primary WAS the pure derive, so there's no faster column DISTINCT
		// to fall back to. Surface the derive's error rather than running a
		// DISTINCT cluster query that would itself fail with code 47.
		return nil, err
	}
	// Fallback ONLY on error: at billion-span scale during the transition the
	// derive can blow the 8s budget (operator-reported blank dropdown — a derive
	// error used to cache an empty list). Fall back to a sub-second DISTINCT over
	// the LowCardinality `cluster` column so the dropdown shows the live/active
	// clusters instead of blanking. This can omit a cluster active ONLY in
	// pre-v0.8.132 parts, but it self-heals as old parts age out, and the filter
	// itself uses clusterColExpr so any cluster stays selectable.
	return s.scanClusters(ctx, `
		SELECT DISTINCT cluster
		FROM spans
		WHERE time >= ? AND time <= ? AND cluster != ''
		ORDER BY cluster
		LIMIT 200
		SETTINGS max_execution_time = 8`, from, to)
}

// ListEnvironments returns the distinct deployment environments
// (spans.deploy_env) observed in the window — the source for the
// global Topbar env picker (v0.8.383, env-separation Phase 1).
// Mirrors ListClusters' budget shape: the env set is deploy-stable
// (an env with observable services emits spans every few minutes),
// so enumeration never scans more than clusterScanWindow before
// `to` — but unlike the cluster derive, deploy_env is a typed
// LowCardinality column, so this is a cheap dict GROUP BY even at
// billion-span scale. Empty deploy_env is excluded: "no env" is
// the picker's default state, not a value.
// envEnumWindow resolves the enumeration scan window (v0.8.389,
// operator-reported: "release" env missing from the picker). The
// UNSEARCHED list keeps the cheap 1h clamp (busiest envs — feature
// branches made the set unbounded); an explicit SEARCH widens to 24h
// so a quiet-but-real env (a release branch that deployed this
// morning) is findable by name. Still time-bounded + exec-capped;
// deploy_env is a typed LC column, no derive. Pure — table-tested.
func envEnumWindow(from, to time.Time, q string) time.Time {
	clamp := time.Hour
	if strings.TrimSpace(q) != "" {
		clamp = 24 * time.Hour
	}
	if floor := to.Add(-clamp); from.Before(floor) {
		return floor
	}
	return from
}

// ListEnvironments enumerates deploy_env values for the picker
// (v0.8.383; reworked v0.8.389 — operator-reported: LIMIT 50 +
// ALPHABETICAL order starved later names once feature-branch envs
// (int-feature-*) exploded the set: "release" sorted past the cap
// and never appeared. Now count-ordered (busiest first, ties by
// name), optional case-insensitive substring q, and a total so the
// picker can say "+N more — type to refine" instead of implying
// completeness).
func (s *Store) ListEnvironments(ctx context.Context, from, to time.Time, q string, limit int) (envs []string, total uint64, err error) {
	if to.IsZero() {
		to = time.Now()
	}
	if from.IsZero() {
		from = to.Add(-1 * time.Hour)
	}
	from = envEnumWindow(from, to, q)
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	where := "time >= ? AND time <= ? AND deploy_env != ''"
	args := []any{from, to}
	if strings.TrimSpace(q) != "" {
		where += " AND positionCaseInsensitive(deploy_env, ?) > 0"
		args = append(args, strings.TrimSpace(q))
	}
	names, err := s.scanClusters(ctx, `
		SELECT deploy_env
		FROM spans
		WHERE `+where+`
		GROUP BY deploy_env
		ORDER BY count() DESC, deploy_env ASC
		LIMIT `+strconv.Itoa(limit)+`
		SETTINGS max_execution_time = 8`, args...)
	if err != nil {
		return nil, 0, err
	}
	// Same-scan-shape total (LC dict pass) so the picker can label
	// truncation honestly. Soft-fails to len(names).
	total = uint64(len(names))
	row := s.conn.QueryRow(ctx, `
		SELECT uniqExact(deploy_env)
		FROM spans
		WHERE `+where+`
		SETTINGS max_execution_time = 8`, args...)
	var t uint64
	if err := row.Scan(&t); err == nil && t > total {
		total = t
	}
	return names, total, nil
}

// GetServiceEnvironments returns the distinct environments ONE
// service emitted spans from in the window — drives the Envs chip
// group on the Service detail header (v0.8.383, env-separation
// Phase 0c; the operator's "same mobile-bff in int/uat/prep" case).
// service_name leads the WHERE so the (service_name, time) primary
// key prunes the scan; deploy_env is LowCardinality so the GROUP BY
// is a dict pass. Empty env excluded — single-env installs simply
// render no chip group.
func (s *Store) GetServiceEnvironments(ctx context.Context, service string, from, to time.Time) ([]string, error) {
	if to.IsZero() {
		to = time.Now()
	}
	if from.IsZero() {
		from = to.Add(-1 * time.Hour)
	}
	return s.scanClusters(ctx, `
		SELECT deploy_env
		FROM spans
		WHERE service_name = ? AND time >= ? AND time <= ? AND deploy_env != ''
		GROUP BY deploy_env
		ORDER BY deploy_env
		LIMIT 10
		SETTINGS max_execution_time = 8`, service, from, to)
}

// scanClusters runs a single-string-column cluster query and collects the
// distinct names. Shared by the fast column path and the derive fallback.
func (s *Store) scanClusters(ctx context.Context, sql string, args ...any) ([]string, error) {
	rows, err := s.conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err == nil {
			out = append(out, c)
		}
	}
	return out, rows.Err()
}

// ServiceClusterStat is one row of the per-cluster breakdown
// rendered on the Service detail page when traffic comes from
// more than one k8s/openshift cluster. Same numeric set the
// services-list row carries (span count, error rate, p99) plus
// the cluster identifier so an SRE can spot "same service is
// slow only on cluster-eu-prod" at a glance.
type ServiceClusterStat struct {
	Cluster    string  `json:"cluster"`
	SpanCount  uint64  `json:"spanCount"`
	ErrorCount uint64  `json:"errorCount"`
	ErrorRate  float64 `json:"errorRate"`
	AvgMs      float64 `json:"avgDurationMs"`
	P99Ms      float64 `json:"p99DurationMs"`
}

// GetServiceClusterBreakdown returns RED stats per cluster for
// one service in the window. The aggregation is over raw spans
// because the service MV doesn't carry the cluster dim; the
// filter on service_name is selective enough that this stays
// fast (one service's slice of spans, not the whole table).
//
// Returns an empty slice when the service has zero traffic in
// the window — the SPA renders "no cluster breakdown" in that
// case rather than blanking the panel.
func (s *Store) GetServiceClusterBreakdown(
	ctx context.Context, service string, from, to time.Time,
) ([]ServiceClusterStat, error) {
	if service == "" {
		return nil, nil
	}
	if from.IsZero() {
		from = time.Now().Add(-1 * time.Hour)
	}
	if to.IsZero() {
		to = time.Now()
	}
	rows, err := s.conn.Query(ctx, `
		SELECT `+s.clusterExpr()+` AS cluster,
		       count()                          AS span_count,
		       countIf(status_code = 'error')   AS error_count,
		       avg(duration) / 1e6              AS avg_ms,
		       quantile(0.99)(duration) / 1e6   AS p99_ms
		FROM spans
		WHERE time >= ? AND time <= ? AND service_name = ?
		GROUP BY cluster
		HAVING cluster != ''
		ORDER BY span_count DESC
		LIMIT 50
		SETTINGS max_execution_time = 10`,
		from, to, service)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ServiceClusterStat{}
	for rows.Next() {
		var r ServiceClusterStat
		if err := rows.Scan(&r.Cluster, &r.SpanCount, &r.ErrorCount, &r.AvgMs, &r.P99Ms); err != nil {
			continue
		}
		if r.SpanCount > 0 {
			r.ErrorRate = float64(r.ErrorCount) / float64(r.SpanCount) * 100
		}
		out = append(out, r)
	}
	return out, nil
}

// GetServicesFiltered narrows the result to services whose name
// matches `nameMatch` (case-insensitive substring). Used by the
// services-page picker so a service outside the top-N still appears
// when the user types its name. Empty `nameMatch` disables the
// filter (legacy behaviour). limit/offset drive page-based
// pagination — pass limit=0 to disable the cap.
// GetServicesFilteredIn — same as GetServicesFiltered plus
// an optional `serviceIn` allowlist. Used by the API to
// pre-narrow the universe of services to those whose catalog
// row matches an owner-team / SRE-team filter; the spans
// query then groups across just the allowed names. nil /
// empty list = no constraint.
// servicesListWhere builds the WHERE for the raw-spans services
// listing (GetServicesFilteredIn). Factored pure (no conn) so the
// v0.8.385 SQL-shape tests can pin the cluster + env conjuncts
// without a live ClickHouse — the env_filter_test.go pattern.

// heavyScanSpill is appended to the bounded raw-spans GROUP BYs that
// the cluster/env fallback paths run at prod scale (v0.8.392,
// operator-reported: /api/services 500'd ~1.3s on busy clusters with
// the cluster filter — ClickHouse's memory guard (code 241) kills a
// ballooning GROUP BY hash table long before max_execution_time).
// External aggregation SPILLS the hash table to disk instead of
// dying: the spill path is slower, but the page renders. 2 GiB spill
// threshold + the 8 GiB per-query ceiling the topology writer has
// run in prod since v0.5.x. Hardcoded on purpose (operator call):
// tolerate high service counts and many clusters everywhere, no
// tuning knob. Pinned by TestHeavyScanSpill.
const heavyScanSpill = ",\n" +
	"\t\t         max_bytes_before_external_group_by = 2000000000,\n" +
	"\t\t         max_memory_usage = 8000000000"

// clusterMemberServices resolves a cluster name to the services that
// ran in it, from the 60s-cached 1h-clamped service→cluster map
// (v0.8.386). Sorted for deterministic SQL; empty on lookup failure
// or an unknown name — callers MUST treat empty as "don't narrow",
// never as "no services". Bounded: the map itself caps at 1000
// services × 50 clusters.
func (s *Store) clusterMemberServices(ctx context.Context, cluster string) []string {
	// Conn-less Stores (pure SQL-shape tests) may still carry a
	// SEEDED map cache; only a real cache miss needs the conn.
	s.clusterMapMu.RLock()
	fresh := s.clusterMapVal != nil && s.clusterMapFor == time.Hour &&
		time.Since(s.clusterMapAt) < clusterMapCacheTTL
	s.clusterMapMu.RUnlock()
	if !fresh && s.conn == nil {
		return nil
	}
	m, err := s.GetServiceClusterMap(ctx, time.Hour)
	if err != nil {
		return nil
	}
	var out []string
	for svc, clusters := range m {
		for _, c := range clusters {
			if c == cluster {
				out = append(out, svc)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

func (s *Store) servicesListWhere(ctx context.Context, since time.Duration, from, to time.Time, nameMatch string, serviceIn []string, cluster, env string) whereClause {
	var wc whereClause
	if !from.IsZero() {
		wc.add("time >= ?", from)
		if !to.IsZero() {
			wc.add("time <= ?", to)
		}
	} else {
		wc.add("time >= ?", time.Now().Add(-since))
	}
	if nameMatch != "" {
		wc.add("positionCaseInsensitive(service_name, ?) > 0", nameMatch)
	}
	if len(serviceIn) > 0 {
		// Build a `service_name IN (?, ?, …)` clause. ClickHouse
		// driver expects each `?` placeholder to map to a
		// single arg, so we splat the slice.
		holders := make([]string, len(serviceIn))
		args := make([]any, len(serviceIn))
		for i, n := range serviceIn {
			holders[i] = "?"
			args[i] = n
		}
		wc.add("service_name IN ("+strings.Join(holders, ",")+")", args...)
	}
	if cluster != "" {
		// v0.8.386 (operator-reported: /api/services 500 on SOME of
		// prod's 18 clusters) — on an external Distributed spans with
		// no promoted cluster column, this conjunct is the per-row
		// res/attr DERIVE over the whole window; at prod volume that
		// blows max_execution_time/memory, and cache warmth made it
		// look cluster-specific. Narrow by the 60s-cached cluster→
		// services map FIRST: service_name is the PK prefix, so the
		// derive then runs only over the member services' granules.
		// Membership is the map's 1h-clamped view (infra-stable, same
		// tolerance the picker uses); the retained conjunct keeps the
		// NUMBERS exact per cluster. Lookup failure or an unknown
		// cluster name falls back to the old full-window behaviour —
		// never an empty page from a cold map.
		if len(serviceIn) == 0 {
			if members := s.clusterMemberServices(ctx, cluster); len(members) > 0 {
				holders := make([]string, len(members))
				args := make([]any, len(members))
				for i, n := range members {
					holders[i] = "?"
					args[i] = n
				}
				wc.add("service_name IN ("+strings.Join(holders, ",")+")", args...)
			}
		}
		// Match against the promoted cluster column with a derive
		// fallback for pre-column parts (clusterColExpr). New parts
		// hit the indexed LowCardinality col; old parts fall back to
		// the indexOf scan until they age out.
		wc.add(s.clusterExpr()+" = ?", cluster)
	}
	if env != "" {
		// v0.8.385 (env-separation Phase 2) — global ?env= filter.
		// deploy_env is a typed LowCardinality column, so unlike the
		// cluster derive this conjunct is a cheap direct comparison
		// (same precedent as TraceFilter.Env, v0.8.383).
		wc.add("deploy_env = ?", env)
	}
	return wc
}

// cluster (when non-empty) narrows results to spans whose
// derived k8s/openshift cluster name matches exactly. The
// match is on the resolved string returned by
// clusterDeriveExpr — operators pass the cluster name they
// see in the /api/clusters dropdown.
// env (when non-empty) narrows to spans.deploy_env — the global
// Topbar env picker (v0.8.385, env-separation Phase 2). Same
// raw-fallback semantics as cluster, but CHEAPER: deploy_env is a
// typed LowCardinality column, no indexOf derive needed.
func (s *Store) GetServicesFilteredIn(ctx context.Context, since time.Duration, from, to time.Time, nameMatch string, serviceIn []string, sort, dir string, limit, offset int, cluster, env string) ([]ServiceSummary, error) {
	wc := s.servicesListWhere(ctx, since, from, to, nameMatch, serviceIn, cluster, env)
	// scale-audit v0.8.201 — defensive cap on the limit<=0 wrapper path
	// (GetServices passes limit=0) so this raw-spans GROUP BY can't return an
	// unbounded result at billion-span scale.
	limitClause := " LIMIT 5000"
	if limit > 0 {
		limitClause = fmt.Sprintf(" LIMIT %d OFFSET %d", limit, offset)
	}
	// Apdex threshold (T) — 200 ms is a common default. Frustrated boundary
	// is 4T. Computed per-service in the same pass to avoid an extra query.
	const apdexT = 200.0
	rows, err := s.conn.Query(ctx, `
		SELECT service_name,
		       count()                                  AS span_count,
		       countIf(status_code = 'error')           AS error_count,
		       avg(duration) / 1e6                      AS avg_ms,
		       quantile(0.99)(duration) / 1e6           AS p99_ms,
		       (countIf(duration <= ?* 1e6) + countIf(duration > ? * 1e6 AND duration <= ? * 1e6) / 2)
		         / nullIf(count(), 0)                   AS apdex
		FROM spans `+wc.sql()+`
		GROUP BY service_name
		ORDER BY `+servicesSortExpr(sort, dir)+limitClause+`
		SETTINGS max_execution_time = 20`+heavyScanSpill,
		append([]any{apdexT, apdexT, apdexT * 4}, wc.args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ServiceSummary
	for rows.Next() {
		var sv ServiceSummary
		var avgMs, p99Ms, apdex *float64
		if err := rows.Scan(&sv.Name, &sv.SpanCount, &sv.ErrorCount, &avgMs, &p99Ms, &apdex); err != nil {
			return nil, err
		}
		// v0.5.301 — NaN/Inf scrub before JSON marshal.
		sv.AvgMs = safeF(avgMs)
		sv.P99Ms = safeF(p99Ms)
		sv.Apdex = safeF(apdex)
		if sv.SpanCount > 0 {
			sv.ErrorRate = float64(sv.ErrorCount) / float64(sv.SpanCount) * 100
		}
		sv.ApdexThresholdMs = apdexT
		out = append(out, sv)
	}
	return out, rows.Err()
}

// SparklineBuckets is the fixed bucket count for the inline call-rate
// sparkline on each operations-table row. 30 strikes the balance between
// detail (a 30-min window with 1-min resolution shows minute-level
// micro-spikes) and SVG width budget (~80px / 30 ≈ 2.7px/bucket — wide
// enough to be readable, narrow enough to fit beside the numeric cols).
const SparklineBuckets = 30

// queryOperationsFromMV reads the operation_summary_5m MV (added
// in v0.4.99) instead of scanning raw spans for per-operation
// aggregates over a wide window. The MV is an
// AggregatingMergeTree so reads use *Merge() to combine the
// state columns server-side — typical 1h window touches ~12
// rows per (service, operation) instead of millions of spans.
// Sparkline buckets fall out naturally since the MV is already
// 5-min-bucketed; we densify into SparklineBuckets slots in Go.
//
// Returns (rows, nil) on success including the empty case. The
// caller's "fall back to raw spans" path activates on error so a
// fresh install whose MV hasn't been backfilled yet still serves
// /services/{name}/operations.
// queryOperationsFromMV reads the per-operation rollup MV. When
// normalized is true it reads operation_group_summary_5m keyed by the
// normalized op_group shape (group_id rel B) instead of
// operation_summary_5m keyed by the raw operation name; op_group is
// aliased back to the OperationSummary.Name field so the read path,
// scanner, and frontend render identically. The ungrouped '' bucket
// (old/pre-Release-A spans) is excluded so the normalized list stays
// clean. When normalized is false the behaviour is byte-for-byte the
// pre-rel-B path.
func (s *Store) queryOperationsFromMV(ctx context.Context, service string, winStart, winEnd time.Time, normalized bool) ([]OperationSummary, error) {
	if service == "" {
		return nil, fmt.Errorf("queryOperationsFromMV: service required")
	}
	// Pick the MV + grouping column. op_group is aliased AS name so the
	// rest of this function (scan into r.Name, sparkline idxByName) is
	// shared verbatim across both modes. opFilter excludes the ungrouped
	// '' bucket in normalized mode only.
	mvTable, nameCol, opFilter := "operation_summary_5m", "name", ""
	if normalized {
		mvTable, nameCol, opFilter = "operation_group_summary_5m", "op_group", " AND op_group != ''"
	}
	// v0.5.299 — Operator-reported: "No operations" on services
	// that DO have traffic. Root cause: time_bucket holds the
	// bucket START (toStartOfInterval(time, 5 MINUTE)). When the
	// window's winStart isn't aligned to a 5-min boundary, the
	// `time_bucket >= winStart` predicate excludes the bucket
	// that CONTAINS winStart — even though that bucket holds
	// spans inside the operator's window. Example: service
	// deploys at 10:03, all spans land in bucket 10:00;
	// operator opens detail at 10:18 with 15m default →
	// winStart=10:03 → bucket 10:00 excluded → empty table.
	// Fix: align winStart down to the bucket boundary in the
	// WHERE so any bucket whose 5-min slot OVERLAPS the window
	// is included.
	bucketStart := winStart.Truncate(5 * time.Minute)
	// First pass: aggregate rollup per name across the window.
	// nameCol/mvTable/opFilter are server-side constants (never user
	// input) so the interpolation is injection-safe; the window bounds
	// stay parameterised.
	const apdexT = 200.0
	rows, err := s.conn.Query(ctx, `
		SELECT `+nameCol+` AS name,
		       countMerge(span_count_state)                            AS span_count,
		       countMerge(error_count_state)                           AS error_count,
		       sumMerge(duration_sum_state) / 1e6
		         / nullIf(countMerge(span_count_state), 0)             AS avg_ms,
		       arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 1) / 1e6 AS p50_ms,
		       arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 2) / 1e6 AS p95_ms,
		       arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 3) / 1e6 AS p99_ms,
		       (countMerge(apdex_satisfied_state)
		         + countMerge(apdex_tolerating_state) / 2)
		         / nullIf(countMerge(span_count_state), 0)             AS apdex
		FROM `+mvTable+`
		WHERE service_name = ? AND time_bucket >= ? AND time_bucket <= ?`+opFilter+`
		GROUP BY `+nameCol+`
		ORDER BY span_count DESC
		LIMIT 500
		SETTINGS max_execution_time = 30,
		         optimize_read_in_order = 1,
		         optimize_aggregation_in_order = 1,
		         `+s.shardSkipSetting()+`, `+mvQuantileMemSettings,
		service, bucketStart, winEnd)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OperationSummary{}
	idxByName := map[string]int{}
	for rows.Next() {
		var r OperationSummary
		var avgMs, p50, p95, p99 *float64
		var apdex *float64
		if err := rows.Scan(&r.Name, &r.SpanCount, &r.ErrorCount,
			&avgMs, &p50, &p95, &p99, &apdex); err != nil {
			return nil, err
		}
		r.AvgMs = safeF(avgMs)
		r.P50Ms = safeF(p50)
		r.P95Ms = safeF(p95)
		r.P99Ms = safeF(p99)
		r.Apdex = safeF(apdex)
		if r.SpanCount > 0 {
			r.ErrorRate = float64(r.ErrorCount) / float64(r.SpanCount) * 100
		}
		out = append(out, r)
		idxByName[r.Name] = len(out) - 1
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}

	// Second pass: per-bucket counts for the inline sparkline.
	// The MV bucket is 5 min so we densify into SparklineBuckets
	// slots; for a 1h window that's 12 source buckets → 30
	// sparkline slots (each MV bucket fills 2-3 slots).
	winSec := int64(winEnd.Sub(winStart).Seconds())
	if winSec <= 0 {
		return out, nil
	}
	bucketSec := (winSec + int64(SparklineBuckets) - 1) / int64(SparklineBuckets)
	if bucketSec < 1 {
		bucketSec = 1
	}
	bucketRows, err := s.conn.Query(ctx, `
		SELECT `+nameCol+` AS name,
		       intDiv(toUInt32(time_bucket) - toUInt32(?), ?) AS bidx,
		       countMerge(span_count_state)                   AS c,
		       countMerge(error_count_state)                  AS e,
		       arrayElement(quantilesTDigestMerge(0.99)(duration_q_state), 1) / 1e6 AS p99
		FROM `+mvTable+`
		WHERE service_name = ? AND time_bucket >= ? AND time_bucket <= ?`+opFilter+`
		GROUP BY `+nameCol+`, bidx
		SETTINGS max_execution_time = 30,
		         `+s.shardSkipSetting()+`, `+mvQuantileMemSettings,
		bucketStart, bucketSec, service, bucketStart, winEnd)
	if err != nil {
		// Sparkline failure non-fatal — return aggregates without.
		return out, nil
	}
	defer bucketRows.Close()
	for bucketRows.Next() {
		var name string
		var bidx int64
		var c, e uint64
		var p99 *float64
		if err := bucketRows.Scan(&name, &bidx, &c, &e, &p99); err != nil {
			continue
		}
		i, ok := idxByName[name]
		if !ok {
			continue
		}
		if out[i].Sparkline == nil {
			out[i].Sparkline = make([]uint64, SparklineBuckets)
			out[i].ErrorsSparkline = make([]uint64, SparklineBuckets)
			out[i].P99Sparkline = make([]float64, SparklineBuckets)
		}
		if bidx >= 0 && int(bidx) < SparklineBuckets {
			out[i].Sparkline[bidx] += c
			out[i].ErrorsSparkline[bidx] += e
			// Per-bucket p99 — pick the max across coalesced MV
			// buckets that map to the same sparkline slot, same
			// idiom as the topology MV merges. Conservative read.
			if v := safeF(p99); v > out[i].P99Sparkline[bidx] {
				out[i].P99Sparkline[bidx] = v
			}
		}
	}
	_ = apdexT // referenced by the raw-spans path; kept here so a future move keeps the constants together.
	return out, nil
}

// GetOperationSummary returns per-operation aggregates for a single
// service: count, error rate, p50/p95/p99 latency, apdex, plus a
// fixed-length call-rate sparkline over the same window. Drives the
// "Operations" table on the service detail page. Rows ordered by span
// count desc so the heaviest operations surface first; the front-end
// applies its own sort if the user clicks a column header.
//
// Pass `since` for a relative window OR a non-zero from/to for an
// absolute one (matches GetServices semantics). Service name is required;
// passing "" returns all operations across all services, which is rarely
// useful but mirrors the existing GetOperations behaviour.
//
// Sparkline data comes from a second query that GROUPs BY (name,
// bucket_idx) so the worst case is `numNames × SparklineBuckets` rows
// rather than one-row-per-span — safe at billion-span scale. The two
// queries run sequentially (not parallel) because the cache key is
// shared and the second one is small/fast enough that the round-trip
// cost dominates over its execution time.
// normalized=true groups the operations by op_group (the normalized
// operation-shape column; group_id rel B) instead of the raw operation
// name — both the MV path (operation_group_summary_5m) and the raw-spans
// fallback group by op_group and exclude the ungrouped '' bucket. The
// OperationSummary.Name field carries the op_group value in that mode, so
// the scanner, sparkline, and frontend are unchanged. normalized=false is
// byte-for-byte the pre-rel-B behaviour.
func (s *Store) GetOperationSummary(ctx context.Context, service string, since time.Duration, from, to time.Time, normalized bool) ([]OperationSummary, error) {
	// v0.8.186 — when op_group isn't on the spans table (external Distributed
	// install where the ALTER couldn't reach spans_local), BOTH normalized
	// branches reference op_group: the MV path reads the now-absent
	// operation_group_summary_5m, and the raw-spans fallback selects / filters
	// / groups by `op_group` — which HARD-ERRORS with code 16. Force
	// normalized=false so the read soft-degrades to raw operation-name
	// grouping instead of failing the whole Operations table. The healthy path
	// (hasOpGroupCol=true) is unaffected.
	if normalized && !s.hasOpGroupCol {
		normalized = false
	}
	// Resolve the absolute [winStart, winEnd] up front so the sparkline
	// bucketing query uses the exact same window as the aggregate.
	// Using time.Now() twice in the two query branches would skew the
	// bucket alignment by a few milliseconds and put empty buckets at
	// the right edge.
	var winStart, winEnd time.Time
	if !from.IsZero() {
		winStart = from
		if !to.IsZero() {
			winEnd = to
		} else {
			winEnd = time.Now()
		}
	} else {
		winEnd = time.Now()
		winStart = winEnd.Add(-since)
	}

	// Read path: MV when the window is at least one full bucket
	// (5 min) wide. v0.5.300 — Operator-reported (test env):
	// MV returning 0 rows for services that DO have traffic.
	// At scale this can happen for several real reasons: MV
	// sync delay on fresh OTLP batches, AggregatingMergeTree
	// parts not yet merged, sharded CH `optimize_skip_unused_shards`
	// pruning the wrong shard, clock skew between Coremetry pod
	// and CH cluster. Instead of trusting the empty result, fall
	// through to the raw-spans path (bounded by max_execution_time
	// + LIMIT below) which sees the freshly-inserted rows
	// directly. The cache TTL on the calling endpoint then
	// stores the rescued result so the same operator click
	// within 60s hits Redis. Logged so the operator can see
	// which path is serving each request.
	useMV := winEnd.Sub(winStart) >= 5*time.Minute
	if useMV {
		out, err := s.queryOperationsFromMV(ctx, service, winStart, winEnd, normalized)
		if err != nil {
			log.Printf("[ops] service=%s MV path errored: %v — falling through to raw spans",
				service, err)
		} else if len(out) > 0 {
			log.Printf("[ops] service=%s MV path served %d operations (window=%s)",
				service, len(out), winEnd.Sub(winStart))
			return out, nil
		} else {
			log.Printf("[ops] service=%s MV path empty (window=%s) — falling through to raw spans (likely MV sync delay / shard prune / clock skew)",
				service, winEnd.Sub(winStart))
		}
	}

	// Raw-spans fallback. In normalized mode group by the op_group shape
	// and exclude the ungrouped '' bucket so the list matches the MV
	// path; otherwise group by the raw name. nameCol is a server-side
	// constant, injection-safe to interpolate; bounds stay parameterised
	// (LIMIT + max_execution_time + time-bounded WHERE preserved either
	// way — CLAUDE.md raw-spans constraint).
	rawNameCol := "name"
	var wc whereClause
	wc.add("time >= ?", winStart)
	wc.add("time <= ?", winEnd)
	if service != "" {
		wc.add("service_name = ?", service)
	}
	if normalized {
		rawNameCol = "op_group"
		wc.add("op_group != ''") // no placeholder — adds zero args
	}
	const apdexT = 200.0
	rows, err := s.conn.Query(ctx, `
		SELECT `+rawNameCol+` AS name,
		       count()                                       AS span_count,
		       countIf(status_code = 'error')                AS error_count,
		       avg(duration) / 1e6                           AS avg_ms,
		       quantile(0.50)(duration) / 1e6                AS p50_ms,
		       quantile(0.95)(duration) / 1e6                AS p95_ms,
		       quantile(0.99)(duration) / 1e6                AS p99_ms,
		       (countIf(duration <= ? * 1e6)
		         + countIf(duration > ? * 1e6 AND duration <= ? * 1e6) / 2)
		         / nullIf(count(), 0)                        AS apdex
		FROM spans `+wc.sql()+`
		GROUP BY `+rawNameCol+`
		ORDER BY span_count DESC
		LIMIT 500
		SETTINGS max_execution_time = 20,
		         optimize_skip_unused_shards = 0`+heavyScanSpill,
		append([]any{apdexT, apdexT, apdexT * 4}, wc.args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OperationSummary{}
	for rows.Next() {
		var r OperationSummary
		// v0.5.301 — scan floats into nullable pointers + safeF
		// so any NaN/Inf out of quantile()/avg() on edge inputs
		// is sanitised before JSON marshal (RFC 8259 — NaN+Inf
		// aren't valid JSON; encoding/json hard-errors).
		var avgMs, p50, p95, p99, apdex *float64
		if err := rows.Scan(&r.Name, &r.SpanCount, &r.ErrorCount,
			&avgMs, &p50, &p95, &p99, &apdex); err != nil {
			return nil, err
		}
		r.AvgMs = safeF(avgMs)
		r.P50Ms = safeF(p50)
		r.P95Ms = safeF(p95)
		r.P99Ms = safeF(p99)
		r.Apdex = safeF(apdex)
		if r.SpanCount > 0 {
			r.ErrorRate = float64(r.ErrorCount) / float64(r.SpanCount) * 100
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Second query: bucketed call-rate per operation. We restrict to the
	// names already returned above so we don't waste cycles bucketing
	// the long tail beyond the LIMIT 500. Using IN with a constructed
	// list (rather than a subquery) keeps the query plan simple and
	// avoids a second pass over the spans table for tail operations.
	if len(out) == 0 {
		return out, nil
	}
	winSec := int64(winEnd.Sub(winStart).Seconds())
	if winSec <= 0 {
		return out, nil
	}
	// Bucket size: enough seconds so that ⌈winSec / bucketSec⌉ ≤ SparklineBuckets.
	// `(winSec + N - 1) / N` is integer-ceil; +1 floor guards against the
	// degenerate case where winSec < SparklineBuckets (each row gets its
	// own bucket and any extras are empty trailing slots).
	bucketSec := (winSec + int64(SparklineBuckets) - 1) / int64(SparklineBuckets)
	if bucketSec < 1 {
		bucketSec = 1
	}

	names := make([]string, 0, len(out))
	idxByName := make(map[string]int, len(out))
	for i, r := range out {
		names = append(names, r.Name)
		idxByName[r.Name] = i
	}
	holders := make([]string, len(names))
	for i := range names {
		holders[i] = "?"
	}
	// Argument order must match the placeholders left-to-right:
	// intDiv(?, ?) projects first (winStart, bucketSec), then the
	// wc.sql() WHERE-clause args, then the IN-list names. Build it
	// once so we don't risk a mismatch between SQL and bindings.
	args := make([]any, 0, 2+len(wc.args)+len(names))
	args = append(args, winStart, bucketSec)
	args = append(args, wc.args...)
	for _, n := range names {
		args = append(args, n)
	}

	// Same rawNameCol switch as the aggregate query above: in normalized
	// mode select/filter/group by op_group (aliased back AS name so the
	// scanner + idxByName lookup are shared). wc already carries the
	// op_group != '' predicate appended above.
	sparkRows, err := s.conn.Query(ctx, `
		SELECT `+rawNameCol+` AS name,
		       intDiv(toUInt32(time) - toUInt32(?), ?) AS bidx,
		       count()                                 AS c,
		       countIf(status_code = 'error')          AS e,
		       quantile(0.99)(duration) / 1e6          AS p99
		FROM spans `+wc.sql()+`
		  AND `+rawNameCol+` IN (`+strings.Join(holders, ",")+`)
		GROUP BY `+rawNameCol+`, bidx
		SETTINGS max_execution_time = 15`,
		args...)
	if err != nil {
		// Sparkline failure is non-fatal — return aggregates without
		// trend column populated. Avoids breaking the whole table on a
		// transient ClickHouse hiccup.
		return out, nil
	}
	defer sparkRows.Close()
	for sparkRows.Next() {
		var name string
		var bidx int64
		var c, e uint64
		var p99 *float64
		if err := sparkRows.Scan(&name, &bidx, &c, &e, &p99); err != nil {
			continue
		}
		i, ok := idxByName[name]
		if !ok {
			continue
		}
		if out[i].Sparkline == nil {
			out[i].Sparkline = make([]uint64, SparklineBuckets)
			out[i].ErrorsSparkline = make([]uint64, SparklineBuckets)
			out[i].P99Sparkline = make([]float64, SparklineBuckets)
		}
		if bidx >= 0 && int(bidx) < SparklineBuckets {
			out[i].Sparkline[bidx] += c
			out[i].ErrorsSparkline[bidx] += e
			if v := safeF(p99); v > out[i].P99Sparkline[bidx] {
				out[i].P99Sparkline[bidx] = v
			}
		}
	}
	return out, nil
}


// GetOperations returns the distinct span names ("operations") seen in the
// given window, optionally filtered by service. Ordered by call count desc,
// so the most common operations appear first in the autocomplete list.
func (s *Store) GetOperations(ctx context.Context, service string, since time.Duration, from, to time.Time) ([]string, error) {
	var wc whereClause
	if !from.IsZero() {
		wc.add("time >= ?", from)
		if !to.IsZero() {
			wc.add("time <= ?", to)
		}
	} else {
		wc.add("time >= ?", time.Now().Add(-since))
	}
	if service != "" {
		wc.add("service_name = ?", service)
	}
	rows, err := s.conn.Query(ctx,
		`SELECT name, count() AS c
		 FROM spans `+wc.sql()+`
		 GROUP BY name
		 ORDER BY c DESC
		 LIMIT 500`, wc.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		var c uint64
		if err := rows.Scan(&name, &c); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// GetServiceGraph returns the directed call graph between services.
// If `service` is non-empty, only edges where it appears as source OR
// target are returned (the neighborhood of that service).
//
// Two-source derivation, UNION'd then re-aggregated:
//
//  1. parent→child self-join across different service_names. This is
//     the strong signal — both sides emit OTel spans, so the edge
//     reflects a real cross-service call.
//
//  2. Outbound (client / producer) spans where the downstream identity
//     is inferred from the first non-empty among:
//       a. peer.service                (OTel SDK hint)
//       b. rpc.service                 (gRPC contract — the same string
//                                       Grafana Tempo's traces-drilldown
//                                       uses to bucket child gRPC calls)
//       c. server.address / http.host  (HTTP downstream)
//       d. db.system                   (DB engine)
//       e. messaging.system            (queue / topic broker)
//     This catches edges to non-instrumented downstreams (managed DBs,
//     third-party APIs, brokers) AND covers environments where the
//     OTel SDK isn't populating peer.service — common in older Java
//     auto-instrumentation and hand-rolled gRPC clients.
//
// We deliberately DO NOT derive edges from net.peer.ip / pod names —
// they're network-layer identifiers that change on every restart and
// would create spurious nodes for sidecars / proxies / load balancers.
// service_name + the application-layer attributes above are stable.
// GetServiceGraph signature kept narrow for callers; the topN cap is
// passed via GetServiceGraphTopN below. The original entry point
// keeps the legacy "no cap" behaviour for non-UI consumers (tests,
// SLO eval, etc.) — at scale the HTTP handler should always go
// through the capped variant.
func (s *Store) GetServiceGraph(ctx context.Context, service string, since time.Duration, from, to time.Time) ([]ServiceEdge, error) {
	return s.GetServiceGraphTopN(ctx, service, since, from, to, 0)
}

// GetServiceGraphTopN returns at most `topN` highest-traffic edges
// (by call count). topN <= 0 disables the cap. Without a cap, a
// large fleet (>500 services) regularly produces 5k+ edges, which
// the SPA can't lay out in real time.
func (s *Store) GetServiceGraphTopN(ctx context.Context, service string, since time.Duration, from, to time.Time, topN int) ([]ServiceEdge, error) {
	var startTime, endTime time.Time
	if !from.IsZero() {
		startTime = from
		if !to.IsZero() {
			endTime = to
		}
	} else {
		startTime = time.Now().Add(-since)
	}
	if endTime.IsZero() {
		endTime = time.Now()
	}
	// v0.5.367 — read from topology_edges_5m. Pre-fix did a raw
	// `spans JOIN spans` per request which is unviable at the
	// 1B+ spans/day target. The MV is updated every 5min by
	// WriteTopologyBucket; per-edge errors landed in v0.5.367
	// alongside calls + sum_duration_ns so this read needs no
	// additional aggregation pass beyond a window roll-up.
	//
	// Bucket-align the lower bound (same idiom as the other
	// *_5m readers) so a 13:03 winStart catches the 13:00
	// bucket that contains 13:00-13:05 of source spans.
	bucketStart := startTime.Truncate(5 * time.Minute)

	svcWhere := ""
	args := []any{bucketStart, endTime}
	if service != "" {
		svcWhere = " AND (parent_service = ? OR child_node = ?)"
		args = append(args, service, service)
	}

	// FINAL on the ReplacingMergeTree so the SAME (time_bucket,
	// parent_service, child_node, node_kind, protocol) tuple
	// doesn't double-count across version revisions. The
	// version field deduplicates within bucket; aggregation
	// across buckets (sum / sumIf / sum) yields window totals.
	sql := `
		SELECT parent_service                         AS source,
		       child_node                             AS target,
		       sum(calls)                             AS call_count,
		       sum(errors) * 100.0 / nullIf(sum(calls), 0) AS error_rate,
		       sum(sum_duration_ns) / nullIf(sum(calls), 0) / 1e6 AS avg_ms
		FROM topology_edges_5m FINAL
		WHERE time_bucket >= ? AND time_bucket <= ?` + svcWhere + `
		  AND parent_service != child_node
		GROUP BY parent_service, child_node
		ORDER BY call_count DESC
		SETTINGS max_execution_time = 10`
	if topN > 0 {
		sql += fmt.Sprintf("\n\t\tLIMIT %d", topN)
	}

	rows, err := s.conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ServiceEdge{}
	for rows.Next() {
		var e ServiceEdge
		var errRate, avgMs *float64
		if err := rows.Scan(&e.Source, &e.Target, &e.CallCount, &errRate, &avgMs); err != nil {
			return nil, err
		}
		e.ErrorRate = safeF(errRate)
		e.AvgMs = safeF(avgMs)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ── Trace queries ─────────────────────────────────────────────────────────────

// WellKnownTraceCol maps OTel semantic-convention attribute keys to
// their dedicated columns on the spans table. When the /traces page
// asks for one of these as an extra column we pull from the indexed
// LowCardinality column instead of scanning the attr_keys/attr_values
// arrays — same value, much cheaper plan.
var WellKnownTraceCol = map[string]string{
	"http.method":      "http_method",
	"http.route":       "http_route",
	"http.status_code": "toString(http_status)",
	"db.system":        "db_system",
	"db.statement":     "db_statement",
	"rpc.system":       "rpc_system",
	"rpc.method":       "rpc_method",
	"peer.service":     "peer_service",
	"messaging.system": "msg_system",
	"service.name":     "service_name",
	"deployment.environment": "deploy_env",
	// Current semconv spelling (≥1.27) — same typed column (v0.8.379).
	"deployment.environment.name": "deploy_env",
	"host.name":        "host_name",
	"kind":             "kind",
}

type TraceFilter struct {
	Service  string
	Search   string
	TraceID  string // exact match or prefix (16+ hex chars)
	From, To time.Time
	HasError bool
	// RootOnly hides traces where the root span ((parent_id = '' OR parent_id = '0000000000000000')) was
	// never ingested — typically partial / fragmented traces where
	// only sub-spans landed in storage. The list view exposes this
	// as a "Root traces" checkbox alongside "Errors only".
	RootOnly bool
	// RequireServices restricts the result to traces that contain
	// spans from EVERY listed service — a trace-topology AND across
	// service involvement. The single Service filter, in contrast,
	// is a span-level WHERE that narrows to one service. When both
	// are set, RequireServices takes precedence (Service is dropped)
	// because the WHERE-narrowing approach can't co-exist with the
	// HAVING-based fan-in check. Used by the backtrace 'Traces'
	// drill-in to surface only traces where caller × callee
	// actually co-occur.
	RequireServices []string
	// TraceIDs restricts the result to this explicit set of trace IDs
	// (rendered as `trace_id IN (…)`). Used by the relations view
	// (relations.go): the bounded self-join resolves a page of trace
	// IDs, then GetTraces re-fetches their summary rows for the list
	// render. The `trace_id IN (…)` clause rides the idx_trace bloom
	// skip index so the re-fetch is bounded by the page size, not the
	// window. Caller caps the slice length (≤ page size). When set, the
	// trace_summary MV fast-path is disqualified (raw access required).
	TraceIDs []string
	MinMs    float64
	MaxMs    float64
	AttrKey  string
	AttrVal  string
	// ExtraAttrs is the user-selected list of attribute keys whose
	// values should be projected into TraceRow.Extras. Each key picks
	// up the first non-empty value among span-attributes and resource-
	// attributes for that trace. Sanitised by the HTTP layer to strict
	// dot/underscore/dash naming so the value can flow safely into the
	// SELECT clause.
	ExtraAttrs []string
	// Env narrows to spans emitted from ONE deployment environment
	// (spans.deploy_env — v0.8.383, env-separation Phase 1, the global
	// Topbar picker's ?env=). Deliberately a first-class field rather
	// than an injected FilterExpr: FilterRoot SUPERSEDES Filters, so an
	// appended env leaf would silently vanish whenever the operator has
	// a grouped OR/nested filter active — and wrapping the group one
	// level deeper would hit the depth cap (nested groups' own Groups
	// are ignored). As its own always-AND conjunct it composes with
	// every filter mode. Non-empty Env disqualifies the trace_summary
	// MV fast-path (the MV has no env dimension — cluster-style
	// raw-fallback, operator-approved, NO MV changes).
	Env        string
	Filters    []FilterExpr // advanced filter chips (AND-joined)
	// FilterRoot is the optional grouped AND/OR builder (v0.8.x trace-query
	// gap-2). When non-nil it SUPERSEDES Filters: buildGetTracesWhere calls
	// ApplyFilterGroup instead of ApplyFilters. A flat-AND FilterRoot emits
	// byte-identical SQL to the legacy Filters path (pinned by
	// filtergroup_test.go), so existing callers that leave it nil are wholly
	// unaffected. An OR / nested group disqualifies the trace_summary MV
	// fast-path exactly like Search / custom-attr does (see GetTraces gate).
	FilterRoot *FilterGroup
	Sort     string       // "time" | "duration"
	Order    string       // "asc" | "desc"
	Limit    int
	Offset   int
	// RankedWithin, when non-nil, is an OUT param: the MV fast-path
	// writes traceRecencySliceN into it when a non-time sort was
	// ranked within the newest-N recency slice (v0.8.369,
	// Dynatrace-style). The HTTP layer forwards it so the UI can
	// show the "ranked within newest N" hint honestly — it stays 0
	// when the raw path (or an unsliced sort) served the request.
	RankedWithin *int
	// CountMode controls the cost/accuracy of the total-rows badge:
	//
	//	"skip"    no DISTINCT count at all — the cheapest path. The caller
	//	          gets `hasMore=true` when the result is full, so the UI
	//	          can paginate without ever paying the count.
	//	"approx"  count only the first Limit+1 trace_ids (LIMIT-bounded
	//	          subquery). Caps the scan so the worst case is bounded
	//	          even at scale.
	//	"exact"   full count(DISTINCT trace_id) — the legacy behaviour;
	//	          can be 10s+ on multi-billion-span tables.
	//
	// Empty string is treated as "exact" by GetTraces for back-compat
	// with internal callers; the HTTP layer overrides the default to
	// "skip".
	CountMode string
}

// buildGetTracesWhere assembles the WHERE clause for GetTraces from
// a TraceFilter. Pure function (no Store / no ctx), extracted in
// v0.5.450 so the regression test for the v0.5.440 fix can assert
// on the generated SQL without standing up a ClickHouse instance.
//
// Order of conditions matches the historical inline build:
// time bounds → service narrowing → trace ID → error flag →
// duration → free-form FilterExpr[].
func buildGetTracesWhere(f TraceFilter) whereClause {
	var wc whereClause
	if !f.From.IsZero() {
		// v0.5.356 — operator-reported: clicking an operation on
		// Service detail jumped to /traces but returned 0
		// results despite the op row showing non-zero counts.
		// Root cause: operation_summary_5m reads
		// `time_bucket >= winStart.Truncate(5min)` (v0.5.299 fix
		// so ops with traffic in the bucket-overlap region
		// surface); this raw-spans path used a strict
		// `time >= winStart` so a trace whose only matching
		// span landed in the bucket-overlap region was hidden.
		// Match the alignment when the window is wide enough
		// (>5min) that the up-to-5min widening doesn't
		// dominate the operator's perceived window.
		fromAligned := f.From
		if !f.To.IsZero() && f.To.Sub(f.From) > 5*time.Minute {
			fromAligned = f.From.Truncate(5 * time.Minute)
		}
		wc.add("time >= ?", fromAligned)
	}
	if !f.To.IsZero() {
		wc.add("time <= ?", f.To)
	}
	// Service narrowing — critical for the (service_name, time)
	// primary key. At 40M traces/day we cannot afford to skip the
	// service-level prefix.
	//   - Single Service set:        service_name = ?
	//   - RequireServices set:       service_name IN (?, ?, …) so we
	//     only scan spans from the required participants. The HAVING
	//     fan-in check works correctly off this narrowed set because
	//     we only ever ask "does service X have ≥1 span in this
	//     trace" — restricting to {RequireServices} doesn't drop any
	//     row that contributes to the answer.
	//   - Neither: full scan (existing behaviour).
	switch {
	case len(f.RequireServices) > 0:
		holders := make([]string, len(f.RequireServices))
		args := make([]any, len(f.RequireServices))
		for i, s := range f.RequireServices {
			holders[i] = "?"
			args[i] = s
		}
		wc.add("service_name IN ("+strings.Join(holders, ",")+")", args...)
	case f.Service != "":
		// v0.5.440 — narrow service-scope restored. v0.5.371
		// previously widened this to a `trace_id IN (subquery)`
		// shape so a trace where service X called a route owned
		// by service Y would still match when the operator
		// searched for "POST /payment" (X's span name was the
		// verb only, Y's name had the route). That worked but
		// pulled in every trace where ANY other service called
		// X — operator-reported (v0.5.440): "I filtered by
		// service=X and the list shows traces of services that
		// CALLED X; Tempo's semantic is just X's traces."
		//
		// v0.5.372 in the meantime broadened the search HAVING
		// to include `arrayExists` over `attr_values`, which
		// catches the route in url.path / http.target / similar
		// attrs that X's client span carries. So the v0.5.371
		// cross-service widening is no longer needed to satisfy
		// the "X called Y's route" case — X's own attrs supply
		// the match. Going back to plain WHERE service_name=X
		// fixes the operator's complaint without regressing
		// v0.5.371's use case.
		wc.add("service_name = ?", f.Service)
	}
	if f.TraceID != "" {
		// Exact match for full 32-char trace ID, prefix match for shorter.
		// Bloom filter index on trace_id makes this efficient.
		if len(f.TraceID) == 32 {
			wc.add("trace_id = ?", f.TraceID)
		} else {
			wc.add("startsWith(trace_id, ?)", f.TraceID)
		}
	}
	if len(f.TraceIDs) > 0 {
		// Explicit trace-id set (relations view). `trace_id IN (…)` rides
		// the idx_trace bloom skip index so even a wide window scans only
		// the page's spans. Caller caps the slice length.
		holders := make([]string, len(f.TraceIDs))
		args := make([]any, len(f.TraceIDs))
		for i, id := range f.TraceIDs {
			holders[i] = "?"
			args[i] = id
		}
		wc.add("trace_id IN ("+strings.Join(holders, ",")+")", args...)
	}
	if f.HasError {
		wc.add("status_code = 'error'")
	}
	if f.MinMs > 0 {
		wc.add("duration >= ?", int64(f.MinMs*1e6))
	}
	if f.MaxMs > 0 {
		wc.add("duration <= ?", int64(f.MaxMs*1e6))
	}
	// Environment narrowing (v0.8.383) — always ANDed, independent of
	// the Filters/FilterRoot supersede rule below (see TraceFilter.Env).
	// deploy_env is a typed LowCardinality column, so this is a cheap
	// dict comparison on the raw path.
	if f.Env != "" {
		wc.add("deploy_env = ?", f.Env)
	}
	// Grouped AND/OR builder supersedes the flat Filters when present
	// (v0.8.x gap-2). A flat-AND FilterRoot routes straight through
	// ApplyFilters inside ApplyFilterGroup, so the legacy path stays
	// byte-identical; an OR / nested group emits a single parenthesised
	// conjunct.
	if f.FilterRoot != nil {
		ApplyFilterGroup(&wc, *f.FilterRoot)
	} else {
		ApplyFilters(&wc, f.Filters)
	}
	return wc
}

// tracesSpillSettings spills the raw trace-list GROUP BY trace_id + ORDER BY to
// disk instead of OOM-ing the node. Operator-reported (v0.8.70): a
// /api/traces?range=12h&search=…&sort=duration query over a large spans table
// failed with CH code 241 ("Memory limit exceeded … AggregatingTransform") —
// the search filter disqualifies the trace_summary MV, so it fell back to the
// raw GROUP BY trace_id and built a multi-GB aggregation state. Spilling at
// 512 MiB keeps the query completing (trades disk + time for memory) on
// memory-constrained nodes; nodes with ample RAM rarely reach the threshold.
const tracesSpillSettings = "max_bytes_before_external_group_by = 536870912, max_bytes_before_external_sort = 536870912"

// searchHaystack is the per-span text the free-text trace search scans:
// operation name + HTTP method + route + every span-attr value, space-joined
// into one string so the operator's words match whichever field carries them.
// arrayStringConcat on an empty attr_values returns '' (concat is NULL-safe).
const searchHaystack = `concat(name, ' ', http_method, ' ', http_route, ' ', arrayStringConcat(attr_values, ' '))`

// searchPredicate builds the per-span free-text match for a query string plus
// its CH args. The input is whitespace-split into tokens; a span matches only
// when EVERY token appears (case-insensitive, UTF8) somewhere in searchHaystack
// — ALL-tokens semantics (v0.8.x, operator choice). multiSearchAllPositions
// returns each token's position in a single pass over the haystack; arrayAll
// asserts none is zero (all found). A single token reduces to the prior
// single-needle behaviour (verified byte-identical on live spans). Returns
// ("", nil) for a blank / all-whitespace query so the caller adds no predicate.
func searchPredicate(search string) (string, []any) {
	tokens := strings.Fields(search)
	if len(tokens) == 0 {
		return "", nil
	}
	ph := make([]string, len(tokens))
	args := make([]any, len(tokens))
	for i, t := range tokens {
		ph[i] = "?"
		args[i] = t
	}
	sql := "arrayAll(p -> p > 0, multiSearchAllPositionsCaseInsensitiveUTF8(" +
		searchHaystack + ", [" + strings.Join(ph, ", ") + "]))"
	return sql, args
}

func (s *Store) GetTraces(ctx context.Context, f TraceFilter) ([]TraceRow, uint64, bool, error) {
	// MV fast-path. Activates when:
	//   • Window is ≥ 5 minutes (MVs are 5-min bucketed)
	//   • No per-span filters that require raw access:
	//       search (LIKE on name), traceId prefix, extra attr
	//       columns, DSL/Filters, RequireServices.
	//   • Sort is by time/duration/spans/status (everything
	//     except service-by-service navigation we can satisfy
	//     from the summary's aggregate states).
	//   • CountMode is skip (the MV path returns hasMore but
	//     doesn't compute exact totals — same trade-off the
	//     /api/traces UI already accepts).
	//
	// Branches by service filter:
	//   • f.Service != "" → two-stage path: scan
	//     trace_service_index_5m for matching trace_ids, then
	//     pull their summaries from trace_summary_5m.
	//   • f.Service == "" → single-stage path: scan
	//     trace_summary_5m directly (PK on (time_bucket,
	//     trace_id) handles the partition prune; GROUP BY
	//     collapses the bucketed rows into one row per
	//     trace_id within the window). Drops the open-page
	//     /traces?last=15m wait from "scan tens of millions
	//     of raw spans" to sub-second.
	//
	// When all conditions hold we bypass the GROUP BY trace_id
	// over raw spans (~10-100M rows on a 7-day window) and
	// read pre-aggregated state instead.
	// v0.8.77 — extra attribute columns NO LONGER disqualify the MV fast path.
	// Operator-reported: adding a "+ Column" on a huge spans window was very
	// slow because requesting any ExtraAttrs forced the raw GROUP BY trace_id
	// scan over the whole window. Instead we keep the fast MV list and fill the
	// requested columns with a SECOND, bounded query that fetches the attrs for
	// ONLY the page's ~50 trace_ids via the idx_trace bloom skip index — so a
	// column add stays sub-second regardless of total span volume.
	if !f.From.IsZero() && !f.To.IsZero() &&
		f.To.Sub(f.From) >= 5*time.Minute &&
		f.Search == "" && f.TraceID == "" &&
		// v0.8.383 — an env filter disqualifies the MV fast-path:
		// trace_summary_5m has no env dimension (cluster precedent —
		// bounded raw fallback, NO MV changes).
		f.Env == "" &&
		len(f.Filters) == 0 &&
		!f.FilterRoot.hasPredicate() &&
		len(f.RequireServices) == 0 &&
		len(f.TraceIDs) == 0 &&
		(f.CountMode == "skip" || f.CountMode == "") {
		out, total, hasMore, err := s.getTracesFromMV(ctx, f)
		if err == nil {
			if len(f.ExtraAttrs) > 0 {
				if ferr := s.fillTraceExtras(ctx, out, f.ExtraAttrs); ferr != nil {
					// Non-fatal: fall through to the raw path (which projects the
					// attrs inline) rather than return rows missing their columns.
					log.Printf("[chstore] trace extras fill failed, falling back to raw: %v", ferr)
				} else {
					return out, total, hasMore, nil
				}
			} else {
				return out, total, hasMore, nil
			}
		} else {
			// On error fall through to raw path — log it so a regression in the
			// MV pipeline doesn't silently leave us on the slow path forever.
			log.Printf("[chstore] trace_summary fast path failed, falling back to raw: %v", err)
		}
	}

	wc := buildGetTracesWhere(f)
	if f.Limit == 0 {
		f.Limit = 50
	}

	// HAVING fragments applied after GROUP BY trace_id:
	//   - RootOnly: requires the root span ((parent_id = '' OR parent_id = '0000000000000000')) landed
	//   - RequireServices: each listed service must have ≥1 span in
	//     the trace, so a (caller, callee) pair must literally
	//     co-occur — what the backtrace 'Traces' drill-in needs.
	havingParts := []string{}
	havingArgs := []any{}
	if f.RootOnly {
		// "Root traces only" — strict: a real root span must
		// be present AND complete. A complete root has:
		//   • parent_id empty (handles both wire formats)
		//   • a non-empty name
		//   • a non-empty service_name
		// This excludes Tempo-style "root not available" partial
		// traces where some orphan child span happens to have an
		// empty parent_id but isn't actually the trace's root.
		havingParts = append(havingParts,
			"countIf((parent_id = '' OR parent_id = '0000000000000000') AND name != '' AND service_name != '') > 0")
	}
	for _, svc := range f.RequireServices {
		if svc == "" {
			continue
		}
		havingParts = append(havingParts, "countIf(service_name = ?) > 0")
		havingArgs = append(havingArgs, svc)
	}
	// v0.5.369 — search at trace-level via HAVING so a trace
	// matches as long as ANY of its spans hits the substring,
	// not "one span satisfies WHERE + search together".
	// v0.5.370 — broaden the search target. Operator-reported:
	// /traces?service=frontend&search=POST /login returned 1
	// trace out of 22K matching spans because the SDK
	// emitted "POST" (verb only) as name with /login living
	// in http_route. Match across name AND http_route AND the
	// common HTTP path attrs so the operator's "POST /login"
	// query lands regardless of how the SDK split the parts.
	// Combined into one countIf to keep the HAVING args
	// addressable; CH treats it as a single trace-level
	// predicate.
	if pred, pargs := searchPredicate(f.Search); pred != "" {
		// v0.8.x — ALL-tokens free-text match over the combined per-span
		// haystack (name + method + route + attr_values). countIf(...) > 0
		// keeps it a trace-level predicate (any span in the trace matching
		// all the words surfaces the trace). Replaces the prior 4-leg
		// single-needle OR-chain; single-word queries behave identically.
		havingParts = append(havingParts, "countIf("+pred+") > 0")
		havingArgs = append(havingArgs, pargs...)
	}
	havingSQL := ""
	if len(havingParts) > 0 {
		havingSQL = " HAVING " + strings.Join(havingParts, " AND ")
	}

	// Total-count cost is bounded by the user's choice. Default for
	// internal callers (and back-compat) is "exact"; the HTTP layer flips
	// to "skip" so the UI never blocks on a multi-billion-row DISTINCT.
	var total uint64
	switch f.CountMode {
	case "skip":
		// no-op; caller infers fullness from len(rows) vs Limit
	case "approx":
		// Cap the count at 10× page size — bounded scan, surfaces "lots
		// of results" without paying a full DISTINCT.
		cap := f.Limit * 10
		if cap < 100 {
			cap = 100
		}
		approxSQL := fmt.Sprintf(
			"SELECT count() FROM (SELECT trace_id FROM spans %s GROUP BY trace_id%s LIMIT %d) SETTINGS max_execution_time = 30",
			wc.sql(), havingSQL, cap,
		)
		countArgs := append([]any{}, wc.args...)
		countArgs = append(countArgs, havingArgs...)
		if err := s.conn.QueryRow(ctx, approxSQL, countArgs...).Scan(&total); err != nil {
			return nil, 0, false, err
		}
	default: // "exact" (and "" for back-compat)
		// HAVING-bearing filters (RootOnly / RequireServices) force a
		// GROUP BY + HAVING wrapper so the count reflects only traces
		// surviving those checks; without them count(DISTINCT trace_id)
		// would include rows the row-level query later hides.
		//
		// v0.5.378 — auto-HLL on wide windows. count(DISTINCT) /
		// uniqExact is O(N) in trace_ids and starts to chew real CPU
		// past ~24h. Switch the implementation to HyperLogLog for
		// windows >24h so "Show total" returns sub-second on a 30d
		// query at billion-span scale. Error margin ~1% — visible
		// as "~12K" vs "exactly 12,547". The 24h cutoff keeps short-
		// window operator-facing counts exactly precise where the
		// extra cost is negligible.
		var settings string
		if !f.From.IsZero() && !f.To.IsZero() && f.To.Sub(f.From) > 24*time.Hour {
			settings = "SETTINGS max_execution_time = 30, count_distinct_implementation = 'uniq', " + tracesSpillSettings
		} else {
			settings = "SETTINGS max_execution_time = 30, " + tracesSpillSettings
		}
		var countSQL string
		var countArgs []any
		if havingSQL != "" {
			countSQL = "SELECT count() FROM (SELECT trace_id FROM spans " + wc.sql() +
				" GROUP BY trace_id" + havingSQL + ") " + settings
			countArgs = append(countArgs, wc.args...)
			countArgs = append(countArgs, havingArgs...)
		} else {
			countSQL = "SELECT count(DISTINCT trace_id) FROM spans " + wc.sql() +
				" " + settings
			countArgs = wc.args
		}
		if err := s.conn.QueryRow(ctx, countSQL, countArgs...).Scan(&total); err != nil {
			return nil, 0, false, err
		}
	}

	// SAFE: sort key is whitelisted, never user-supplied SQL.
	sortMap := map[string]string{
		"time":      "trace_start",
		"duration":  "dur_ms",
		"spans":     "span_count",
		"service":   "root_svc",
		"operation": "root_name",
		"status":    "has_error",
	}
	sortCol, ok := sortMap[f.Sort]
	if !ok {
		sortCol = "trace_start"
	}
	order := "DESC"
	if f.Order == "asc" {
		order = "ASC"
	}

	// Pull Limit+1 rows so we know whether there's another page without
	// having to count. Cheap — same query plan, one extra row's worth of
	// bytes. This is what powers the "Page N · 50+" badge when CountMode
	// is "skip".
	pageLimit := f.Limit + 1

	// Build optional projections for user-requested attribute columns.
	//
	// Two paths depending on the key:
	//   - well-known semconv key with a dedicated structured column
	//     (http.method → http_method, db.system → db_system, etc.):
	//     use the indexed LowCardinality column. Cheap.
	//   - everything else: array lookup against attr_values via
	//     attr_values[indexOf(attr_keys, ?)], with a fallback to
	//     res_values[indexOf(res_keys, ?)] so resource-level attrs
	//     (service.namespace, k8s.pod.name, etc.) also work.
	//
	// Keys flow as `?` parameters; HTTP-layer sanitisation already
	// rejected anything outside [a-zA-Z0-9._-] so the SELECT can't be
	// poisoned even if a clickhouse-go quirk changed.
	extraSelect := ""
	extraArgs := []any{}
	for i, key := range f.ExtraAttrs {
		if col, ok := WellKnownTraceCol[key]; ok {
			extraSelect += fmt.Sprintf(", any(%s) AS extra_%d", col, i)
			continue
		}
		extraSelect += fmt.Sprintf(
			", anyIf(coalesce("+
				"nullIf(attr_values[indexOf(attr_keys, ?)], ''),"+
				"nullIf(res_values[indexOf(res_keys, ?)], '')"+
				"), has(attr_keys, ?) OR has(res_keys, ?)) AS extra_%d",
			i,
		)
		extraArgs = append(extraArgs, key, key, key, key)
	}

	// Note: use if() not ternary ? : — ClickHouse treats ? as a param placeholder
	// v0.5.351 — root_name/root_svc fallback. When the WHERE
	// filter (service / name search) excludes the real root
	// span (because it lives in a different service or has a
	// different name), anyIf(parent_id='') returns empty and
	// the trace row renders blank. Fall back to ANY span's
	// name/service so the operator at least sees a label — the
	// trace detail view still shows the full trace on click.
	querySQL := `
		SELECT trace_id,
		       coalesce(
		         nullIf(anyIf(name, (parent_id = '' OR parent_id = '0000000000000000')), ''),
		         any(name)
		       )                                       AS root_name,
		       coalesce(
		         nullIf(anyIf(service_name, (parent_id = '' OR parent_id = '0000000000000000')), ''),
		         any(service_name)
		       )                                       AS root_svc,
		       min(time)                               AS trace_start,
		       (max(toUnixTimestamp64Nano(time) + duration) -
		        toUnixTimestamp64Nano(min(time))) / 1e6 AS dur_ms,
		       count()                                 AS span_count,
		       max(if(status_code = 'error', 1, 0))    AS has_error` +
		extraSelect + `
		FROM spans ` + wc.sql() + `
		GROUP BY trace_id` + havingSQL + `
		ORDER BY ` + sortCol + ` ` + order + `
		LIMIT ? OFFSET ?
		SETTINGS
		  max_execution_time = 60,
		  optimize_read_in_order = 1,
		  optimize_aggregation_in_order = 1,
		  distributed_product_mode = 'global',
		  ` + tracesSpillSettings

	// Argument order matches placeholder order in the SQL:
	//   1. SELECT projections (extra attribute columns)
	//   2. WHERE  predicates (time / service / DSL filters)
	//   3. HAVING predicates (RequireServices fan-in)
	//   4. LIMIT / OFFSET
	args := append([]any{}, extraArgs...)
	args = append(args, wc.args...)
	args = append(args, havingArgs...)
	args = append(args, pageLimit, f.Offset)
	rows, err := s.conn.Query(ctx, querySQL, args...)
	if err != nil {
		return nil, 0, false, err
	}
	defer rows.Close()

	out := []TraceRow{}
	for rows.Next() {
		var t TraceRow
		var hasErr uint8
		var ts time.Time
		// Scan the fixed columns first, then peel off one extra string
		// per requested attribute. The driver expects every column to
		// have a destination, so the extras slice is sized exactly.
		extras := make([]string, len(f.ExtraAttrs))
		dest := []any{&t.TraceID, &t.RootName, &t.ServiceName, &ts, &t.DurationMs, &t.SpanCount, &hasErr}
		for i := range extras {
			dest = append(dest, &extras[i])
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, 0, false, err
		}
		t.StartTime = ts.UnixNano()
		t.HasError = hasErr == 1
		if len(extras) > 0 {
			t.Extras = make(map[string]string, len(extras))
			for i, k := range f.ExtraAttrs {
				t.Extras[k] = extras[i]
			}
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, false, err
	}
	hasMore := len(out) > f.Limit
	if hasMore {
		out = out[:f.Limit]
	}
	return out, total, hasMore, nil
}

// getTracesFromMV implements the two-stage fast path:
//
//  Stage 1: scan trace_service_index_5m, find the top
//  (Limit+1) trace_ids that match the service in the
//  window, ordered by last activity. Uses the
//  (service_name, time_bucket) primary key prefix so the
//  scan is partition-pruned + sorted-access.
//
//  Stage 2: pull those trace_ids' full summaries from
//  trace_summary_5m (state merge). Apply RootOnly /
//  HasError / MinMs / MaxMs as HAVING filters here so the
//  page sort sees only matching traces. Final ORDER BY +
//  LIMIT runs on the page-bounded result.
//
// Sort: index supports time-desc; for duration / span_count
// / has_error we still produce the right page because stage
// 1 over-selects (10× LIMIT) and stage 2 re-sorts. This
// trades a small amount of recall for a 1000× speedup at
// scale; if an operator needs an exact ordering across the
// full window they can drop the service filter or narrow
// the time range.
// fillTraceExtras fetches the user-selected attribute columns for a page of
// trace rows and writes them into each row's Extras map. Bounded by the page
// size — WHERE trace_id IN (page ids) rides the idx_trace bloom skip index — so
// it stays cheap regardless of total span volume. Attribute resolution mirrors
// the raw path's extraSelect (well-known semconv key → structured column;
// otherwise attr_values/res_values array lookup). (v0.8.77)
func (s *Store) fillTraceExtras(ctx context.Context, rows []TraceRow, attrs []string) error {
	if len(rows) == 0 || len(attrs) == 0 {
		return nil
	}
	sel := ""
	projArgs := []any{}
	for i, key := range attrs {
		if col, ok := WellKnownTraceCol[key]; ok {
			sel += fmt.Sprintf(", any(%s) AS extra_%d", col, i)
			continue
		}
		sel += fmt.Sprintf(
			", anyIf(coalesce("+
				"nullIf(attr_values[indexOf(attr_keys, ?)], ''),"+
				"nullIf(res_values[indexOf(res_keys, ?)], '')"+
				"), has(attr_keys, ?) OR has(res_keys, ?)) AS extra_%d", i)
		projArgs = append(projArgs, key, key, key, key)
	}
	holders := make([]string, len(rows))
	idArgs := make([]any, len(rows))
	for i, r := range rows {
		holders[i] = "?"
		idArgs[i] = r.TraceID
	}
	sql := "SELECT trace_id" + sel +
		" FROM spans WHERE trace_id IN (" + strings.Join(holders, ",") + ")" +
		" GROUP BY trace_id SETTINGS max_execution_time = 15"
	args := append(append([]any{}, projArgs...), idArgs...)

	qrows, err := s.conn.Query(ctx, sql, args...)
	if err != nil {
		return err
	}
	defer qrows.Close()
	byID := make(map[string][]string, len(rows))
	for qrows.Next() {
		var id string
		extras := make([]string, len(attrs))
		dest := make([]any, 0, len(attrs)+1)
		dest = append(dest, &id)
		for i := range extras {
			dest = append(dest, &extras[i])
		}
		if err := qrows.Scan(dest...); err != nil {
			return err
		}
		byID[id] = extras
	}
	if err := qrows.Err(); err != nil {
		return err
	}
	for i := range rows {
		ex, ok := byID[rows[i].TraceID]
		if !ok {
			continue
		}
		if rows[i].Extras == nil {
			rows[i].Extras = make(map[string]string, len(attrs))
		}
		for j, key := range attrs {
			if j < len(ex) {
				rows[i].Extras[key] = ex[j]
			}
		}
	}
	return nil
}

// traceStage1LightSQL builds the no-service Stage-1 id-select
// (v0.8.357): ONE aggregate for the sort key + only the states the
// active HAVING filters need — instead of every merge state for every
// trace in the window. ok=false for service/operation sorts, whose sort
// key IS the heavy argMax state (nothing to lighten; caller falls back
// to the single-stage scan). Pure so the shape is table-tested.
func traceStage1LightSQL(f TraceFilter, having []string) (string, bool) {
	var sortExpr string
	switch f.Sort {
	case "", "time":
		// Plain column max — no merge state at all. 5-min granularity
		// ties are fine: Stage 1 over-selects and Stage 2 re-sorts the
		// kept ids on the exact trace_start state.
		sortExpr = "max(time_bucket)"
	case "duration":
		sortExpr = "(maxMerge(trace_end_state) - toUnixTimestamp64Nano(minMerge(trace_start_state)))"
	case "spans":
		sortExpr = "countMerge(span_count_state)"
	case "status":
		sortExpr = "toUInt8(countMerge(error_count_state) > 0)"
	default:
		return "", false
	}
	order := "DESC"
	if f.Order == "asc" {
		order = "ASC"
	}
	havingSQL := ""
	if len(having) > 0 {
		havingSQL = " HAVING " + strings.Join(having, " AND ")
	}
	return `
		SELECT trace_id
		FROM trace_summary_5m
		WHERE time_bucket >= ? AND time_bucket <= ?
		GROUP BY trace_id` + havingSQL + `
		ORDER BY ` + sortExpr + ` ` + order + `
		LIMIT ?
		SETTINGS max_execution_time = 15,
		         optimize_read_in_order = 1,
		         optimize_aggregation_in_order = 1`, true
}

// traceRecencySliceN is the Dynatrace-style ranking slice (v0.8.369,
// operator decision): on the no-service path, non-time sorts rank
// within the NEWEST N traces instead of aggregating every trace in
// the window. Cost becomes constant at any range (the global
// duration sort was O(window) — at 1B spans/day a wide window
// pushed the 15s cap); the trade-off, surfaced in the UI as a
// "ranked within newest N" hint, is that a slower-but-older trace
// outside the slice no longer tops the list. Must stay ≤
// traceStage2MaxIDs (the ids ride Stage 2's IN list).
const traceRecencySliceN = 5000

// traceSortRecencySliced says whether a sort key ranks within the
// recency slice (everything except time itself). Pure — table-tested.
func traceSortRecencySliced(sort string) bool {
	return sort != "" && sort != "time"
}

// traceStage2MaxIDs bounds how many trace ids Stage 1 may hand to
// Stage 2's IN list. clickhouse-go interpolates bind args client-side,
// so each id costs ~35 bytes ('<32 hex>' + comma) of query text and
// the server parses at most max_query_size (default 256 KiB = 262144).
// 6000 ids ≈ 210 KiB leaves headroom for the rest of the statement.
// v0.8.363 — found via self-telemetry (coremetry-monolithic error
// span): Stage 2 died with code 62 "Syntax error at position 262126"
// — the inlined id list crossed the parser budget exactly there.
const traceStage2MaxIDs = 6000

// traceStage1Budget resolves the light Stage-1 id budget for a page:
// grows with offset (page N needs offset+page ids — both stages sort
// by the same key and apply the same HAVING, so the first
// offset+pageLimit ids are exactly the page candidates; 2× is dup /
// drift slack), floors at stage1Limit, and refuses (ok=false → the
// caller keeps the single-stage full-window scan, slower but correct)
// when even the un-slacked need would blow the Stage-2 IN-list byte
// budget. Pure — table-tested (v0.8.363).
func traceStage1Budget(offset, pageLimit, stage1Limit int) (int, bool) {
	need := 2 * (offset + pageLimit)
	budget := stage1Limit
	if need > budget {
		budget = need
	}
	if budget > traceStage2MaxIDs {
		if offset+pageLimit > traceStage2MaxIDs {
			return 0, false
		}
		budget = traceStage2MaxIDs
	}
	return budget, true
}

// tracePostAggFiltered reports whether the filter carries a
// post-aggregate predicate (one that only Stage 2's HAVING can
// evaluate). The service path must NOT bound Stage 1 by recency when
// one is active — see the v0.8.365 comment at the subquery switch.
// (RequireServices / Search / attr filters disqualify the MV path
// upstream, so they never reach here.) Pure — table-tested.
func tracePostAggFiltered(f TraceFilter) bool {
	return f.HasError || f.RootOnly || f.MinMs > 0 || f.MaxMs > 0
}

func (s *Store) getTracesFromMV(ctx context.Context, f TraceFilter) ([]TraceRow, uint64, bool, error) {
	if f.Limit == 0 {
		f.Limit = 50
	}
	pageLimit := f.Limit + 1
	// Stage 1 over-selects so a Stage 2 sort by non-time
	// columns still surfaces a reasonable page from the
	// service's recent slice. Clamped to the Stage-2 IN-list
	// byte budget (v0.8.363).
	stage1Limit := pageLimit * 10
	if stage1Limit < 200 {
		stage1Limit = 200
	}
	if stage1Limit > traceStage2MaxIDs {
		stage1Limit = traceStage2MaxIDs
	}

	// v0.8.357 (operator-reported: /traces 30-60m çok yavaş) — the
	// no-service path used to skip Stage 1 entirely and let Stage 2
	// compute EVERY merge state (6× argMax/min/max/count) for EVERY
	// trace in the window just to keep 50. At prod volume (millions of
	// trace_summary rows per 30m) that is the whole page-load. The
	// no-service path now gets its own LIGHT Stage 1: one aggregate for
	// the sort key + only the states active filters need, so the heavy
	// state merges run for ~stage1Limit ids only. service/operation
	// sorts still take the single-stage path (their sort key IS the
	// heavy argMax state — nothing to lighten).
	var traceIDs []any
	holders := ""
	serviceSubquery := false
	if f.Service != "" && tracePostAggFiltered(f) {
		// v0.8.365 — self-telemetry follow-up: the recency-bounded
		// Stage 1 below is BLIND to the post-aggregate filters
		// (hasError / minMs / maxMs / rootOnly). On a busy service
		// errors are rare, so "the stage1Limit most recent trace ids"
		// often contains zero matches and the page comes back empty
		// while matching traces exist in the window (ground truth: 18
		// error traces, API 0). With any post-aggregate filter active,
		// narrow Stage 2 by an IN-subquery over the service index
		// instead — every trace the service participated in inside the
		// window stays eligible, the HAVING then filters them. Costs a
		// wider Stage-2 merge, bounded by the window + LIMIT +
		// max_execution_time; without filters the fast recency path
		// below is unchanged.
		serviceSubquery = true
	} else if f.Service != "" {
		rows1, err := s.conn.Query(ctx, `
			SELECT trace_id
			FROM trace_service_index_5m
			WHERE service_name = ? AND time_bucket >= ? AND time_bucket <= ?
			GROUP BY trace_id
			ORDER BY maxMerge(last_seen_state) DESC
			LIMIT ?
			SETTINGS
			  max_execution_time = 15,
			  optimize_read_in_order = 1,
			  optimize_aggregation_in_order = 1,
			  `+s.shardSkipSetting(),
			f.Service, f.From, f.To, stage1Limit)
		if err != nil {
			return nil, 0, false, fmt.Errorf("stage1: %w", err)
		}
		defer rows1.Close()
		traceIDs = make([]any, 0, stage1Limit)
		for rows1.Next() {
			var id string
			if err := rows1.Scan(&id); err != nil {
				return nil, 0, false, err
			}
			traceIDs = append(traceIDs, id)
		}
		if err := rows1.Err(); err != nil {
			return nil, 0, false, err
		}
		if len(traceIDs) == 0 {
			return []TraceRow{}, 0, false, nil
		}

		holders = strings.Repeat("?,", len(traceIDs))
		holders = holders[:len(holders)-1]
	}

	// Push-down HAVING built from the post-aggregate filters.
	having := []string{}
	if f.RootOnly {
		// Root span exists with non-empty service_name (same
		// strict check the raw path uses).
		having = append(having, "argMaxIfMerge(root_service_state) != ''")
	}
	if f.HasError {
		having = append(having, "countMerge(error_count_state) > 0")
	}
	if f.MinMs > 0 {
		having = append(having, fmt.Sprintf(
			"((maxMerge(trace_end_state) - toUnixTimestamp64Nano(minMerge(trace_start_state))) / 1e6) >= %v",
			f.MinMs))
	}
	if f.MaxMs > 0 {
		having = append(having, fmt.Sprintf(
			"((maxMerge(trace_end_state) - toUnixTimestamp64Nano(minMerge(trace_start_state))) / 1e6) <= %v",
			f.MaxMs))
	}
	// Light Stage 1 for the no-service path (v0.8.357) — see
	// traceStage1LightSQL. Filters ride the same HAVING so the top-N ids
	// are correct; Stage 2 then merges the full states for ~stage1Limit
	// ids instead of the whole window. Deep offsets grow the id budget.
	if f.Service == "" && holders == "" {
		s1f := f
		ranked := traceSortRecencySliced(f.Sort)
		budget, budgetOK := traceStage1Budget(f.Offset, pageLimit, stage1Limit)
		if ranked {
			// Dynatrace-style (v0.8.369): Stage 1 picks the NEWEST
			// traceRecencySliceN ids (always time DESC — f.Order
			// belongs to the requested key, applied by Stage 2 within
			// the slice). This also gives service/operation sorts a
			// two-stage path — they used to single-stage over the
			// whole window. Pages past the slice come back empty by
			// design; the response carries the slice size so the UI
			// can say so.
			s1f.Sort, s1f.Order = "time", "desc"
			budget, budgetOK = traceRecencySliceN, true
		}
		if s1, ok := traceStage1LightSQL(s1f, having); ok && budgetOK {
			rows1, err := s.conn.Query(ctx, s1, f.From, f.To, budget)
			if err != nil {
				return nil, 0, false, fmt.Errorf("stage1-light: %w", err)
			}
			ids := make([]any, 0, budget)
			for rows1.Next() {
				var id string
				if err := rows1.Scan(&id); err != nil {
					rows1.Close()
					return nil, 0, false, err
				}
				ids = append(ids, id)
			}
			rows1.Close()
			if err := rows1.Err(); err != nil {
				return nil, 0, false, err
			}
			if len(ids) == 0 {
				return []TraceRow{}, 0, false, nil
			}
			traceIDs = ids
			holders = strings.Repeat("?,", len(ids))
			holders = holders[:len(holders)-1]
			if ranked && f.RankedWithin != nil {
				*f.RankedWithin = traceRecencySliceN
			}
		}
	}

	havingSQL := ""
	if len(having) > 0 {
		havingSQL = " HAVING " + strings.Join(having, " AND ")
	}

	// Whitelist sort col → CH expression. Defaults to time DESC.
	sortExpr := "trace_start"
	switch f.Sort {
	case "duration":
		sortExpr = "dur_ms"
	case "spans":
		sortExpr = "span_count"
	case "status":
		sortExpr = "has_error"
	case "service":
		sortExpr = "root_svc"
	case "operation":
		sortExpr = "root_name"
	}
	order := "DESC"
	if f.Order == "asc" {
		order = "ASC"
	}

	// Whether stage 2 narrows by trace_id ids (service fast path),
	// by an index subquery (service + post-aggregate filters,
	// v0.8.365), or scans the bucket window (no-service path) —
	// same SELECT, different WHERE.
	traceIDClause := ""
	var idArgs []any
	if serviceSubquery {
		traceIDClause = `trace_id IN (
			SELECT trace_id FROM trace_service_index_5m
			WHERE service_name = ? AND time_bucket >= ? AND time_bucket <= ?
			GROUP BY trace_id
		) AND `
		idArgs = []any{f.Service, f.From, f.To}
	} else if holders != "" {
		traceIDClause = "trace_id IN (" + holders + ") AND "
		idArgs = traceIDs
	}

	stage2 := `
		SELECT trace_id,
		       argMaxIfMerge(root_name_state)                              AS root_name,
		       argMaxIfMerge(root_service_state)                           AS root_svc,
		       minMerge(trace_start_state)                                 AS trace_start,
		       (maxMerge(trace_end_state) -
		        toUnixTimestamp64Nano(minMerge(trace_start_state))) / 1e6  AS dur_ms,
		       countMerge(span_count_state)                                AS span_count,
		       toUInt8(countMerge(error_count_state) > 0)                  AS has_error
		FROM trace_summary_5m
		WHERE ` + traceIDClause + `time_bucket >= ? AND time_bucket <= ?
		GROUP BY trace_id` + havingSQL + `
		ORDER BY ` + sortExpr + ` ` + order + `
		LIMIT ? OFFSET ?
		SETTINGS max_execution_time = 15,
		         optimize_read_in_order = 1,
		         optimize_aggregation_in_order = 1`

	args := append([]any{}, idArgs...)
	args = append(args, f.From, f.To, pageLimit, f.Offset)
	rows2, err := s.conn.Query(ctx, stage2, args...)
	if err != nil {
		return nil, 0, false, fmt.Errorf("stage2: %w", err)
	}
	defer rows2.Close()
	out := []TraceRow{}
	for rows2.Next() {
		var t TraceRow
		var hasErr uint8
		var ts time.Time
		if err := rows2.Scan(&t.TraceID, &t.RootName, &t.ServiceName, &ts, &t.DurationMs, &t.SpanCount, &hasErr); err != nil {
			return nil, 0, false, err
		}
		t.StartTime = ts.UnixNano()
		t.HasError = hasErr != 0
		out = append(out, t)
	}
	hasMore := len(out) > f.Limit
	if hasMore {
		out = out[:f.Limit]
	}
	return out, 0, hasMore, nil
}

// GetTraceAggregate buckets traces by an attribute (operation/service) and
// returns RED-style stats per bucket. Each bucket = traces with the same
// root operation (or service). Filters mirror GetTraces, but sorting/limit
// applies to bucket aggregates, not individual traces.
type AggregateFilter struct {
	// GroupBy picks the group dimension. Valid: "operation",
	// "service", "kind", "status", "http_method", "http_route",
	// "http_status", "host", "deploy_env", "scope". Anything else
	// (or empty) → "operation".
	GroupBy string
	// GroupAttr lets the operator group by a custom attribute key
	// (e.g. "user.id", "tenant", "order.id"). Sanitised by the
	// HTTP layer to dot/dash/underscore characters only. When set,
	// it overrides GroupBy.
	GroupAttr string
	Service   string
	Search    string
	From, To  time.Time
	HasError  bool
	MinMs     float64
	MaxMs     float64
	// Env — spans.deploy_env narrowing (v0.8.383, ?env=). First-class
	// for the same reason as TraceFilter.Env: it must survive the
	// FilterRoot-supersedes-Filters rule and the FilterGroup depth cap.
	// Non-empty disqualifies the trace_summary MV fast-path below.
	Env       string
	Filters   []FilterExpr
	// FilterRoot — grouped AND/OR builder (v0.8.x gap-2). Supersedes Filters
	// when non-nil; flat-AND is byte-identical to the legacy path, OR /
	// nested disqualifies the trace_summary MV fast-path.
	FilterRoot *FilterGroup
	Sort      string // "count"|"perMin"|"errorRate"|"avg"|"p50"|"p95"|"p99"|"max"|"name"
	Order     string // "asc"|"desc"
	Limit     int
}

func (s *Store) GetTraceAggregate(ctx context.Context, f AggregateFilter) ([]AggregateRow, error) {
	// MV fast-path: when grouping by service or operation and
	// no per-span filters / custom attributes / search are in
	// play, trace_summary_5m has every aggregate we need
	// already (countState / quantilesState / countIfState +
	// argMaxIfState(service / name)). Skips the inner
	// GROUP BY trace_id over raw spans, which dominates the
	// cold-cache cost on busy clusters (millions of rows on a
	// 15-min window for one popular service).
	//
	// Conditions mirror GetTraces' fast-path gate so the
	// trade-off is consistent: window ≥ 5min, no Search /
	// custom attribute / RequireServices / DSL filter, and the
	// grouping is one of the two the MV stores.
	if f.Limit == 0 {
		f.Limit = 100
	}
	if (f.GroupBy == "service" || f.GroupBy == "operation" || f.GroupBy == "") &&
		f.GroupAttr == "" && f.Search == "" &&
		// v0.8.383 — env filter forces the raw path (no env dim in the MV).
		f.Env == "" &&
		len(f.Filters) == 0 &&
		!f.FilterRoot.hasPredicate() &&
		!f.From.IsZero() && !f.To.IsZero() &&
		f.To.Sub(f.From) >= 5*time.Minute {
		if rows, err := s.getTraceAggregateFromMV(ctx, f); err == nil {
			return rows, nil
		}
		// Fall through on MV error so a regression doesn't
		// blank the page.
	}

	// Per-trace stats first (subquery), then group across traces.
	var wc whereClause
	if !f.From.IsZero() {
		// v0.5.356 — same bucket alignment as GetTraces above:
		// keep the aggregate-view's window in sync with the
		// operations table that surfaces operation-by-operation
		// counts from the 5-min bucketed MV. Without this,
		// click-from-operation → aggregate-view also missed the
		// bucket-overlap traces.
		fromAligned := f.From
		if !f.To.IsZero() && f.To.Sub(f.From) > 5*time.Minute {
			fromAligned = f.From.Truncate(5 * time.Minute)
		}
		wc.add("time >= ?", fromAligned)
	}
	if !f.To.IsZero() {
		wc.add("time <= ?", f.To)
	}
	if f.Service != "" {
		wc.add("service_name = ?", f.Service)
	}
	// v0.5.369 — search moved to inner HAVING below (cross-
	// service trace-level match). See GetTraces commentary for
	// the root-cause rationale.
	if f.HasError {
		wc.add("status_code = 'error'")
	}
	// v0.8.383 — env narrowing, always ANDed (see AggregateFilter.Env).
	if f.Env != "" {
		wc.add("deploy_env = ?", f.Env)
	}
	if f.FilterRoot != nil {
		ApplyFilterGroup(&wc, *f.FilterRoot)
	} else {
		ApplyFilters(&wc, f.Filters)
	}

	// Pick the grouping expression. Every form uses anyIf to grab
	// the root span's value ((parent_id = '' OR parent_id = '0000000000000000')), matching Uptrace-
	// style "group traces by root attributes".
	//
	// group_extra surfaces the service alongside the bucket name so
	// the UI can render '<svc> · <op>' rows for non-service groupings;
	// when grouping by service it stays empty.
	groupBuiltin := map[string]string{
		"operation":   "name",
		"service":     "service_name",
		"kind":        "kind",
		"status":      "status_code",
		"http_method": "http_method",
		"http_route":  "http_route",
		"http_status": "toString(http_status)",
		"host":        "host_name",
		"deploy_env":  "deploy_env",
		"scope":       "scope_name",
	}
	var groupExpr, extraExpr string
	groupArgs := []any{}
	switch {
	case f.GroupAttr != "":
		// Sanitisation happened on the HTTP layer — the key is
		// safe to flow through as a `?` parameter. We try span
		// attrs first then resource attrs.
		groupExpr = "anyIf(coalesce(" +
			"nullIf(attr_values[indexOf(attr_keys, ?)], ''), " +
			"nullIf(res_values[indexOf(res_keys, ?)], '')" +
			"), (parent_id = '' OR parent_id = '0000000000000000'))"
		extraExpr = "anyIf(service_name, (parent_id = '' OR parent_id = '0000000000000000'))"
		groupArgs = append(groupArgs, f.GroupAttr, f.GroupAttr)
	case f.GroupBy == "service":
		groupExpr = "anyIf(service_name, (parent_id = '' OR parent_id = '0000000000000000'))"
		extraExpr = "''"
	default:
		col, ok := groupBuiltin[f.GroupBy]
		if !ok {
			col = "name" // default = operation
		}
		groupExpr = "anyIf(" + col + ", (parent_id = '' OR parent_id = '0000000000000000'))"
		extraExpr = "anyIf(service_name, (parent_id = '' OR parent_id = '0000000000000000'))"
	}

	// Whitelist sort key.
	sortMap := map[string]string{
		"count":     "trace_count",
		"perMin":    "per_min",
		"errorRate": "error_rate",
		"avg":       "avg_ms",
		"p50":       "p50_ms",
		"p95":       "p95_ms",
		"p99":       "p99_ms",
		"max":       "max_ms",
		"name":      "group_key",
	}
	sortCol, ok := sortMap[f.Sort]
	if !ok {
		sortCol = "trace_count"
	}
	order := "DESC"
	if f.Order == "asc" {
		order = "ASC"
	}

	// Window minutes for perMin (count / minutes). When the caller
	// supplies an explicit from/to we use that; otherwise fall back
	// to a reasonable default — division by zero is guarded by
	// nullIf in the SQL itself.
	windowMin := 1.0
	if !f.From.IsZero() && !f.To.IsZero() && f.To.After(f.From) {
		windowMin = f.To.Sub(f.From).Minutes()
		if windowMin < 1 {
			windowMin = 1
		}
	}

	// 1) Inner: per-trace summary, bounded to the 200k most
	//    recent traces in the window. At billion-span scale a
	//    naive GROUP BY trace_id would scan every trace_id in
	//    the partition and aggregate all of them — for a 24h
	//    window that's hundreds of millions of unique IDs.
	//    The cap returns a representative top-N by recency
	//    (the operator usually cares about "what's slow right
	//    now" not "the worst trace last December") and keeps
	//    the outer GROUP BY bounded too.
	// 2) Outer: aggregate across the bounded trace set per
	//    group bucket.
	// SETTINGS max_execution_time = 30 protects the UI thread —
	// a misconfigured filter that fans out across a giant
	// window terminates with an error rather than wedging a
	// page-load forever.
	// v0.8.x — ALL-tokens free-text match (see searchPredicate). Computed once
	// so the inner-HAVING SQL and its args stay in lockstep on token count.
	searchSQL, searchArgs := searchPredicate(f.Search)
	searchHaving := ""
	if searchSQL != "" {
		searchHaving = " AND countIf(" + searchSQL + ") > 0"
	}
	sql := `
		SELECT group_key, group_extra,
		       count()                                   AS trace_count,
		       count() / ?                                AS per_min,
		       countIf(has_error = 1)                    AS error_count,
		       countIf(has_error = 1) / count() * 100    AS error_rate,
		       avg(dur_ms)                                AS avg_ms,
		       quantile(0.50)(dur_ms)                     AS p50_ms,
		       quantile(0.95)(dur_ms)                     AS p95_ms,
		       quantile(0.99)(dur_ms)                     AS p99_ms,
		       max(dur_ms)                                AS max_ms,
		       toUnixTimestamp64Nano(max(trace_start))    AS last_seen_ns
		FROM (
		    SELECT trace_id,
		           ` + groupExpr + ` AS group_key,
		           ` + extraExpr + ` AS group_extra,
		           min(time) AS trace_start,
		           (max(toUnixTimestamp64Nano(time) + duration) -
		            toUnixTimestamp64Nano(min(time))) / 1e6 AS dur_ms,
		           max(if(status_code = 'error', 1, 0)) AS has_error
		    FROM spans ` + wc.sql() + `
		    GROUP BY trace_id
		    HAVING group_key != ''` + searchHaving + `
		    ORDER BY trace_start DESC
		    LIMIT 200000
		)`

	// Argument order matches placeholder order in the SQL:
	//   1. Outer SELECT: per_min divisor (windowMin)
	//   2. Inner SELECT: group expr args (custom attribute lookup)
	//   3. Inner WHERE:  filters / time / service
	//   4. Outer HAVING: post filters (minMs / maxMs)
	//   5. Outer LIMIT
	args := []any{windowMin}
	args = append(args, groupArgs...)
	args = append(args, wc.args...)
	// v0.8.x — one bind per search token in the inner HAVING (ALL-tokens).
	args = append(args, searchArgs...)
	postFilter := ""
	if f.MinMs > 0 {
		postFilter += " AND avg_ms >= ?"
		args = append(args, f.MinMs)
	}
	if f.MaxMs > 0 {
		postFilter += " AND avg_ms <= ?"
		args = append(args, f.MaxMs)
	}

	sql += `
		GROUP BY group_key, group_extra`
	if postFilter != "" {
		sql += `
		HAVING 1=1` + postFilter
	}
	sql += `
		ORDER BY ` + sortCol + ` ` + order + `
		LIMIT ?
		SETTINGS max_execution_time = 30, max_threads = 8`
	args = append(args, f.Limit)

	rows, err := s.conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AggregateRow
	for rows.Next() {
		var a AggregateRow
		if err := rows.Scan(
			&a.GroupKey, &a.GroupExtra, &a.TraceCount, &a.PerMin,
			&a.ErrorCount, &a.ErrorRate,
			&a.AvgMs, &a.P50Ms, &a.P95Ms, &a.P99Ms, &a.MaxMs, &a.LastSeen,
		); err != nil {
			return nil, err
		}
		// v0.6.39 — this path reads from raw `spans`, so every
		// counted trace has raw data by construction. Pinning
		// WithRawAvailable == TraceCount lets the UI render a
		// uniform shape across both code paths.
		a.WithRawAvailable = a.TraceCount
		out = append(out, a)
	}
	return out, rows.Err()
}

// getTraceAggregateFromMV reads trace_summary_5m directly when
// the AggregateFilter is service- or operation-grouped and
// carries no per-span filters / custom attributes. The MV
// already stores everything we need:
//
//   • argMaxIfState(root_service / root_name) → group key
//   • countState                              → trace_count
//   • quantilesState                          → p50 / p95 / p99
//   • minMerge(trace_start_state)             → trace_start
//   • countIfState(error_count_state)         → error_count
//
// One pass over the MV's bucket range; no inner GROUP BY
// trace_id over the spans table. On 7d windows the wall-time
// drops from 20-40s to 200-500ms for the same row set.
func (s *Store) getTraceAggregateFromMV(ctx context.Context, f AggregateFilter) ([]AggregateRow, error) {
	// Group key + extra mapping. The MV doesn't store kind /
	// http_* etc, so this path is gated to the two it does
	// store; "" defaults to operation.
	var keyExpr, extraExpr string
	switch f.GroupBy {
	case "service":
		keyExpr = "argMaxIfMerge(root_service_state)"
		extraExpr = "''"
	default: // "" or "operation"
		keyExpr = "argMaxIfMerge(root_name_state)"
		extraExpr = "argMaxIfMerge(root_service_state)"
	}

	sortMap := map[string]string{
		"count":     "trace_count",
		"perMin":    "per_min",
		"errorRate": "error_rate",
		"avg":       "avg_ms",
		"p50":       "p50_ms",
		"p95":       "p95_ms",
		"p99":       "p99_ms",
		"max":       "max_ms",
		"name":      "group_key",
	}
	sortCol, ok := sortMap[f.Sort]
	if !ok {
		sortCol = "trace_count"
	}
	order := "DESC"
	if f.Order == "asc" {
		order = "ASC"
	}

	windowMin := f.To.Sub(f.From).Minutes()
	if windowMin < 1 {
		windowMin = 1
	}

	// Inner: one row per trace inside the bucket window. The
	// MV's PK on (time_bucket, trace_id) lets CH partition-prune
	// + read in order — sub-second even on 7d on a busy cluster.
	//
	// v0.6.43 — operator-reported: /traces?service=X aggregate
	// returned NO rows for any X that's not a root-emitting
	// service (most callees: payment-service, fraud-service,
	// account-ledger-service, ...). Root cause: the previous code
	// added `HAVING argMaxIfMerge(root_service_state) = ?` after
	// GROUP BY trace_id — this only matches traces where X IS the
	// root span's service. For services that are only called
	// (never root the trace), the HAVING removed every row even
	// though those traces had real X-emitted spans we should
	// aggregate.
	//
	// Fix mirrors getTracesFromMV's two-stage path: narrow the
	// trace_id set via trace_service_index_5m (a per-(service,
	// trace) MV that records every trace each service touched,
	// rooted or not), then GROUP BY on trace_summary_5m. The
	// resulting aggregate row groups by the *actual* root
	// operation, so "service=fraud-service" surfaces "POST
	// /checkout (frontend)" as the root that pulled fraud-service
	// in — exactly the call-pattern view the operator needs.
	innerWhere := "WHERE time_bucket >= ? AND time_bucket <= ?"
	innerArgs := []any{f.From, f.To}
	if f.Service != "" {
		innerWhere += ` AND trace_id IN (
		    SELECT DISTINCT trace_id FROM trace_service_index_5m
		    WHERE service_name = ? AND time_bucket >= ? AND time_bucket <= ?
		)`
		innerArgs = append(innerArgs, f.Service, f.From, f.To)
	}
	having := "HAVING group_key != ''"
	if f.HasError {
		having += " AND has_error = 1"
	}

	// v0.6.39 — `in_raw` per-trace flag fed into the outer
	// countIf gives the operator-visible "X traces aggregated · Y
	// still drillable" disparity. Uses GLOBAL IN against the raw
	// `spans` table on the SAME time window so the membership set
	// stays bounded by current ingest. Without GLOBAL IN this would
	// fan a subquery to every replica on a Distributed install;
	// GLOBAL marshals the set once on the coordinator and reuses
	// it across shards (CLAUDE.md cluster invariant). On single-
	// node CH the GLOBAL keyword is a no-op so it's safe in both
	// deployment modes.
	sql := `
		SELECT group_key, group_extra,
		       count() AS trace_count,
		       countIf(in_raw = 1) AS with_raw_available,
		       count() / ? AS per_min,
		       countIf(has_error = 1) AS error_count,
		       countIf(has_error = 1) / count() * 100 AS error_rate,
		       avg(dur_ms) AS avg_ms,
		       quantile(0.50)(dur_ms) AS p50_ms,
		       quantile(0.95)(dur_ms) AS p95_ms,
		       quantile(0.99)(dur_ms) AS p99_ms,
		       max(dur_ms) AS max_ms,
		       toUnixTimestamp64Nano(max(trace_start)) AS last_seen_ns
		FROM (
		    SELECT trace_id,
		           ` + keyExpr + ` AS group_key,
		           ` + extraExpr + ` AS group_extra,
		           argMaxIfMerge(root_service_state) AS root_svc,
		           minMerge(trace_start_state) AS trace_start,
		           (maxMerge(trace_end_state) -
		            toUnixTimestamp64Nano(minMerge(trace_start_state))) / 1e6 AS dur_ms,
		           toUInt8(countMerge(error_count_state) > 0) AS has_error,
		           toUInt8(trace_id GLOBAL IN (
		               SELECT DISTINCT trace_id FROM spans
		               WHERE time >= ? AND time <= ?
		           )) AS in_raw
		    FROM trace_summary_5m
		    ` + innerWhere + `
		    GROUP BY trace_id
		    ` + having + `
		    ORDER BY trace_start DESC
		    LIMIT 200000
		)
		GROUP BY group_key, group_extra`

	postFilter := ""
	postArgs := []any{}
	if f.MinMs > 0 {
		postFilter += " AND avg_ms >= ?"
		postArgs = append(postArgs, f.MinMs)
	}
	if f.MaxMs > 0 {
		postFilter += " AND avg_ms <= ?"
		postArgs = append(postArgs, f.MaxMs)
	}
	if postFilter != "" {
		sql += " HAVING 1=1" + postFilter
	}
	sql += `
		ORDER BY ` + sortCol + ` ` + order + `
		LIMIT ?
		SETTINGS max_execution_time = 15,
		         optimize_read_in_order = 1,
		         optimize_aggregation_in_order = 1`

	// Argument order:
	//   1. windowMin (outer per_min divisor)
	//   2. in_raw subquery: spans.time from, spans.time to (same window)
	//   3. innerArgs: time_bucket from, time_bucket to
	//      (+ when service set: service_name, time_bucket_from,
	//        time_bucket_to for the trace_service_index_5m
	//        narrowing — already in innerArgs)
	//   4. postArgs (MinMs / MaxMs)
	//   5. f.Limit
	args := []any{windowMin}
	args = append(args, f.From, f.To) // in_raw subquery bounds
	args = append(args, innerArgs...)
	args = append(args, postArgs...)
	args = append(args, f.Limit)

	rows, err := s.conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AggregateRow
	for rows.Next() {
		var a AggregateRow
		if err := rows.Scan(
			&a.GroupKey, &a.GroupExtra, &a.TraceCount, &a.WithRawAvailable, &a.PerMin,
			&a.ErrorCount, &a.ErrorRate,
			&a.AvgMs, &a.P50Ms, &a.P95Ms, &a.P99Ms, &a.MaxMs, &a.LastSeen,
		); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// traceTimeBound widens a trace's [start, end] window so the spans scan can be
// time-bounded. start = minMerge(trace_start_state) (a DateTime), endNanos =
// maxMerge(trace_end_state) (a UnixNano Int64) — see the trace_summary_5m MV at
// store.go:2132-2133. ok is false when the summary had no row for the trace
// (endNanos 0, e.g. fresh before MV sync) or the window is nonsensical; the
// caller then falls back to an unbounded scan so a trace is never un-fetchable.
// The 5-min margin absorbs clock skew / state approximation while still binding
// the scan to ~1-2 daily partitions instead of all of them. (v0.8.210)
func traceTimeBound(start time.Time, endNanos int64) (lo, hi time.Time, ok bool) {
	if endNanos <= 0 {
		return time.Time{}, time.Time{}, false
	}
	end := time.Unix(0, endNanos)
	if end.Before(start) {
		return time.Time{}, time.Time{}, false
	}
	const margin = 5 * time.Minute
	return start.Add(-margin), end.Add(margin), true
}

func (s *Store) GetTrace(ctx context.Context, traceID string) ([]SpanRow, error) {
	// v0.8.210 — derive the trace's time window from trace_summary_5m (the
	// aggregate, far smaller than raw spans) so the spans scan is time-bounded
	// to ~1-2 partitions instead of a 30-partition fan-out. Best-effort: any
	// lookup miss/error falls through to the original scan (correctness is never
	// traded for the speedup).
	//
	// v0.8.223 — trace_id is the TRAILING ORDER BY column of trace_summary_5m and
	// it's a combined MaterializedView (ADD INDEX unsupported, code 48), so this
	// pre-query can't granule-prune by trace_id → it's a full scan of the (small)
	// aggregate. Capped at max_execution_time = 3 (was 10) so the worst case — a
	// large MV where the scan times out — costs ≤3s before falling back to the
	// bloom-pruned spans path (spans.idx_trace), instead of up to +10s.
	where := "trace_id = ?"
	args := []any{traceID}
	var winStart time.Time
	var winEndNanos int64
	if err := s.conn.QueryRow(ctx, `
		SELECT minMerge(trace_start_state), toInt64(maxMerge(trace_end_state))
		FROM trace_summary_5m
		WHERE trace_id = ?
		SETTINGS max_execution_time = 3`, traceID).Scan(&winStart, &winEndNanos); err != nil {
		log.Printf("[trace] %s window lookup failed (%v) — unbounded spans scan", traceID, err)
	} else if lo, hi, ok := traceTimeBound(winStart, winEndNanos); ok {
		where += " AND time >= ? AND time <= ?"
		args = append(args, lo, hi)
	}

	rows, err := s.conn.Query(ctx, `
		SELECT trace_id, span_id, parent_id, name, kind, service_name, host_name,
		       time, duration, status_code, status_msg,
		       attr_keys, attr_values, res_keys, res_values,
		       events, scope_name,
		       db_system, db_statement, http_method, http_route, http_status, peer_service
		FROM spans
		WHERE `+where+`
		ORDER BY time ASC
		LIMIT 50000
		SETTINGS max_execution_time = 20`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SpanRow
	for rows.Next() {
		var sp SpanRow
		var t time.Time
		var dur int64
		var attrK, attrV, resK, resV []string
		var eventsJSON string
		if err := rows.Scan(
			&sp.TraceID, &sp.SpanID, &sp.ParentSpanID, &sp.Name, &sp.Kind, &sp.ServiceName, &sp.HostName,
			&t, &dur, &sp.StatusCode, &sp.StatusMessage,
			&attrK, &attrV, &resK, &resV,
			&eventsJSON, &sp.ScopeName,
			&sp.DBSystem, &sp.DBStatement, &sp.HTTPMethod, &sp.HTTPRoute, &sp.HTTPStatus, &sp.PeerService,
		); err != nil {
			return nil, err
		}
		sp.StartTime = t.UnixNano()
		sp.EndTime = t.UnixNano() + dur
		sp.DurationMs = float64(dur) / 1e6
		sp.Attributes = arraysToMap(attrK, attrV)
		sp.ResourceAttributes = arraysToMap(resK, resV)
		json.Unmarshal([]byte(eventsJSON), &sp.Events)
		out = append(out, sp)
	}
	return out, rows.Err()
}

// ── Log queries ───────────────────────────────────────────────────────────────

type LogFilter struct {
	Service     string
	// Env (v0.8.400 — env-separation Phase 4) — deployment-environment
	// filter, applied as the bounded res-array conjunct
	// logsEnvChainSQL. Empty = all environments.
	Env         string
	Search      string
	From, To    time.Time
	SeverityMin uint8
	TraceID     string
	SpanID      string // optional: only logs attached to this span
	Limit       int
	Offset      int
	// Cursor (v0.7.22, SAFE-CORE) — opaque CH keyset token from a
	// prior GetLogs NextCursor. When set, GetLogs pages AFTER the
	// encoded (time, rowKey) position with a STRICT keyset predicate
	// instead of OFFSET. Empty = first page; Offset still honoured.
	Cursor string
	// Ascending (v0.7.83) — flip the sort to oldest-first
	// (time ASC, rowKey ASC). Only honoured on a non-cursor read
	// (the keyset cursor is DESC-only). Used by the /logs Context
	// "after" window so LIMIT n returns the n rows immediately
	// AFTER the pivot, not the n newest in the forward window.
	Ascending bool
	// SinceNs (v0.8.x) — forward-tail mode for the live-tail SSE
	// stream. When > 0, GetLogs reads `time >= SinceNs` oldest-first,
	// bounded by Limit, and SKIPS the count() total + the keyset
	// cursor. The handler tracks the newest emitted timestamp and
	// passes it back; it dedups same-ns boundary rows by LogRow.ID
	// (the cityHash64 rowkey). See logstore.Filter.SinceNs.
	SinceNs int64
}

// logsMaxLimit caps the per-page row count on the logs table. A
// CLAUDE.md hard-constraint: every query on a billion-row table
// must carry a bounded LIMIT. The keyset cursor lets the caller
// page deeper without ever asking for >logsMaxLimit rows at once.
const logsMaxLimit = 1000

// logsEnvChainSQL is the logs-local deployment-environment derivation
// (v0.8.400 — env-separation Phase 4). The topoEnvChainSQL two-spelling
// rule (v0.8.380): the NEW semconv key deployment.environment.name
// first, then the legacy deployment.environment — but WITHOUT the
// deploy_env column leg (logs has no typed env column; adding one is a
// Dilim-5-class migration this raw-fallback slice deliberately avoids)
// and WITHOUT the namespace fallbacks (a logs row with neither key is
// honestly env-less, not namespace-approximated). indexOf() returns 0
// for a missing key and arr[0] is '' in CH, so absent keys coalesce
// cleanly. Pinned by TestLogsEnvChainSQL (repo_logs_env_test.go).
const logsEnvChainSQL = `coalesce(
			nullIf(res_values[indexOf(res_keys, 'deployment.environment.name')], ''),
			nullIf(res_values[indexOf(res_keys, 'deployment.environment')], ''),
			'')`

// logsRowKeyExpr is the ONE SQL expression that makes a log line
// distinguishable among rows sharing a DateTime64(9) timestamp. It is
// a deterministic 64-bit hash over the columns that together identify
// a log line: same row → same hash across every query, so it is a
// stable keyset tiebreak with no stored column / migration.
//
// v0.7.23 (SAFE-CORE) — the prior tiebreak was `span_id` (String
// DEFAULT ''). But (time, span_id) is NOT a total order on the logs
// table: most log lines are emitted OUTSIDE a span (span_id='') and
// DateTime64(9) timestamps collide at billions/day. A page boundary
// landing inside a run of (t0,'') rows dropped every remaining
// (t0,'') row on the next page, because `time = t0 AND span_id < ''`
// matches nothing. cityHash64 over the line's identifying columns is
// effectively unique among same-time rows (64-bit collision ≈ 2^-64),
// restoring a provable total order on (time, rowKey).
//
// host_name is in the hash (v0.7.80) so two pods emitting the SAME
// body/severity/trace_id/span_id at the SAME nanosecond — routine at
// scale — hash distinctly and don't collide at a page boundary. The
// only residual collision is a single pod re-emitting a byte-identical
// line at the same ns (a genuine duplicate); that is acceptable.
//
// MUST match byte-for-byte everywhere it appears (SELECT projection,
// ORDER BY, keyset WHERE) — drift would break the total-order proof.
const logsRowKeyExpr = "cityHash64(service_name, severity_num, severity_text, body, trace_id, span_id, host_name)"

// LogsCursor is the decoded form of a ClickHouse logs keyset token.
// Encoded wire format: base64("ch|"+TimeNs+"|"+RowKey). The "ch|"
// prefix tags the backend so an ES cursor fed to the CH path (or
// vice versa) fails to decode rather than silently mis-paging.
// RowKey is the unsigned cityHash64 of logsRowKeyExpr for the last
// row of the prior page.
type LogsCursor struct {
	TimeNs int64
	RowKey uint64
}

// EncodeLogsCursor renders a (timeNs, rowKey) position as the opaque
// base64 token the API layer round-trips. Kept as a pure function so
// the roundtrip is unit-testable (CLAUDE.md #11).
func EncodeLogsCursor(timeNs int64, rowKey uint64) string {
	raw := "ch|" + strconv.FormatInt(timeNs, 10) + "|" + strconv.FormatUint(rowKey, 10)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodeLogsCursor parses a token produced by EncodeLogsCursor.
// Returns ok=false for any malformed / wrong-backend token so the
// caller falls back to a first-page read rather than erroring the
// whole request.
func DecodeLogsCursor(tok string) (LogsCursor, bool) {
	if tok == "" {
		return LogsCursor{}, false
	}
	b, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil {
		return LogsCursor{}, false
	}
	s := string(b)
	// Expect "ch|<ns>|<rowkey>" — both tail fields are numeric so a
	// plain 3-way split is unambiguous.
	parts := strings.SplitN(s, "|", 3)
	if len(parts) != 3 || parts[0] != "ch" {
		return LogsCursor{}, false
	}
	ns, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return LogsCursor{}, false
	}
	rk, err := strconv.ParseUint(parts[2], 10, 64)
	if err != nil {
		return LogsCursor{}, false
	}
	return LogsCursor{TimeNs: ns, RowKey: rk}, true
}

// logsKeysetPredicate returns the SQL fragment + positional args for
// a STRICT keyset on the (time DESC, rowKey DESC) sort. For a DESC
// scan the next page is everything strictly "less than" the last row:
//
//	time < :t OR (time = :t AND <rowKeyExpr> < :h)
//
// Strict-less on BOTH legs over a TOTAL order (rowKey is effectively
// unique among same-time rows) means the boundary row itself is
// neither re-returned (dup) nor skipped (drop), and a run of same-time
// rows can no longer collapse to a single comparable value the way
// `span_id < ''` did before v0.7.23. Returns ("", nil) when the cursor
// is empty so the first page applies no keyset. Pure function for
// table-driven testing (CLAUDE.md #11).
func logsKeysetPredicate(c LogsCursor, hasCursor bool) (string, []interface{}) {
	if !hasCursor {
		return "", nil
	}
	// Compare the nanosecond instant as Int64 via toUnixTimestamp64Nano —
	// NOT a bare time.Time. clickhouse-go/v2 formats a positional
	// time.Time arg at SECONDS scale, so a bare `time = ?` against the
	// DateTime64(9) column would match nothing and `time < ?` would drop
	// every same-second row on the next page (silent page-boundary loss,
	// v0.7.80). The cursor already carries the exact ns (c.TimeNs ==
	// row.Timestamp == t.UnixNano()) and toUnixTimestamp64Nano(time)
	// yields the column's raw Int64 ns, so the comparison is exact and
	// matches the codebase's ns-precise convention. The From/To window
	// bounds stay on raw `time` so they still prune granules via the
	// time skip index — this extra predicate just refines within them.
	sql := "(toUnixTimestamp64Nano(time) < ? OR (toUnixTimestamp64Nano(time) = ? AND " + logsRowKeyExpr + " < ?))"
	return sql, []interface{}{c.TimeNs, c.TimeNs, c.RowKey}
}

// GetLogs reads a page of the logs table newest-first. v0.7.22
// (SAFE-CORE) hardened it for billion-row scale:
//
//   - Bounded LIMIT (capped at logsMaxLimit) + SETTINGS
//     max_execution_time = 30 — CLAUDE.md hard constraint that was
//     missing before (the count() + main SELECT could full-scan
//     unbounded).
//   - STABLE sort: ORDER BY time DESC, <rowKey> DESC, where rowKey is
//     a deterministic cityHash64 over the line's identifying columns
//     (logsRowKeyExpr). v0.7.23 (SAFE-CORE) replaced the span_id
//     tiebreak: span_id is String DEFAULT '' and most log lines are
//     emitted outside a span, so (time, span_id) was not a total
//     order — a page boundary inside a run of (t0,'') rows dropped
//     every remaining (t0,'') row on the next page. The hash makes
//     (time, rowKey) a provable total order, so no boundary
//     drop/dup.
//   - Keyset cursor paging: when f.Cursor decodes, page strictly
//     AFTER the encoded (time, rowKey) instead of OFFSET. Empty
//     cursor → first page; Offset still honoured for back-compat.
//
// Returns the rows, the (capped-cost) total match count for the UI,
// and a NextCursor — empty when fewer than the requested limit came
// back (last page).
func (s *Store) GetLogs(ctx context.Context, f LogFilter) ([]LogRow, uint64, string, error) {
	var wc whereClause
	if !f.From.IsZero() {
		wc.add("time >= ?", f.From)
	}
	if !f.To.IsZero() {
		wc.add("time <= ?", f.To)
	}
	if f.Service != "" {
		wc.add("service_name = ?", f.Service)
	}
	if f.Env != "" {
		// v0.8.400 — env conjunct. Built into wc BEFORE the SinceNs
		// branch below, so the count(), the main page read AND the
		// forward-tail read all carry it.
		wc.add(logsEnvChainSQL+" = ?", f.Env)
	}
	if f.Search != "" {
		wc.add("body LIKE ?", "%"+f.Search+"%")
	}
	if f.SeverityMin > 0 {
		wc.add("severity_num >= ?", f.SeverityMin)
	}
	if f.TraceID != "" {
		wc.add("trace_id = ?", f.TraceID)
	}
	if f.SpanID != "" {
		wc.add("span_id = ?", f.SpanID)
	}
	if f.Limit <= 0 {
		f.Limit = 100
	}
	if f.Limit > logsMaxLimit {
		f.Limit = logsMaxLimit
	}

	// v0.8.x — forward-tail mode (live-tail SSE). Read `time >= SinceNs`
	// oldest-first, bounded LIMIT, NO count() (the per-tick cost the tail
	// removes) and NO keyset cursor. Returns (rows, len, "", nil); the
	// handler advances SinceNs from the newest row + dedups the boundary
	// ns by LogRow.ID.
	if f.SinceNs > 0 {
		wc.add("time >= ?", time.Unix(0, f.SinceNs))
		rows, err := s.conn.Query(ctx, `
			SELECT time, severity_num, severity_text, body,
			       service_name, trace_id, span_id,
			       attr_keys, attr_values, res_keys, res_values,
			       `+logsRowKeyExpr+` AS _rowkey
			FROM logs `+wc.sql()+`
			ORDER BY time ASC, `+logsRowKeyExpr+` ASC
			LIMIT ?
			SETTINGS max_execution_time = 15`, append(wc.args, f.Limit)...)
		if err != nil {
			return nil, 0, "", err
		}
		defer rows.Close()
		var out []LogRow
		for rows.Next() {
			var lr LogRow
			var t time.Time
			var rowKey uint64
			var attrK, attrV, resK, resV []string
			if err := rows.Scan(
				&t, &lr.SeverityNumber, &lr.SeverityText, &lr.Body,
				&lr.ServiceName, &lr.TraceID, &lr.SpanID,
				&attrK, &attrV, &resK, &resV, &rowKey,
			); err != nil {
				return nil, 0, "", err
			}
			lr.Timestamp = t.UnixNano()
			lr.ID = rowKey
			lr.Attributes = arraysToMap(attrK, attrV)
			lr.ResourceAttributes = arraysToMap(resK, resV)
			out = append(out, lr)
		}
		if err := rows.Err(); err != nil {
			return nil, 0, "", err
		}
		return out, uint64(len(out)), "", nil
	}

	// Total covers the full match window (independent of the cursor)
	// so the UI's "N matches" stays stable while paging. Bounded by
	// max_execution_time so a heavy window can't stall the request.
	var total uint64
	if err := s.conn.QueryRow(ctx,
		"SELECT count() FROM logs "+wc.sql()+" SETTINGS max_execution_time = 30",
		wc.args...).Scan(&total); err != nil {
		return nil, 0, "", err
	}

	// Keyset: append the strict (time, span_id) predicate when a
	// cursor decodes. This is additive to the WHERE clause — the
	// first page (empty cursor) keeps OFFSET semantics for callers
	// that page by offset.
	cur, hasCursor := DecodeLogsCursor(f.Cursor)
	keysetSQL, keysetArgs := logsKeysetPredicate(cur, hasCursor)
	if keysetSQL != "" {
		wc.add(keysetSQL, keysetArgs...)
	}

	offset := f.Offset
	if hasCursor {
		// With a keyset cursor, OFFSET is meaningless (and would
		// re-skip rows the predicate already excluded). Page purely
		// off the keyset.
		offset = 0
	}

	// Direction: newest-first by default. Ascending (oldest-first) is
	// honoured only on a non-cursor read — the keyset cursor encodes a
	// strict DESC boundary, so ASC + cursor would be incoherent. Used by
	// the /logs Context "after" window (v0.7.83).
	ascending := f.Ascending && !hasCursor
	orderDir := "DESC"
	if ascending {
		orderDir = "ASC"
	}

	args := append(wc.args, f.Limit, offset)
	rows, err := s.conn.Query(ctx, `
		SELECT time, severity_num, severity_text, body,
		       service_name, trace_id, span_id,
		       attr_keys, attr_values, res_keys, res_values,
		       `+logsRowKeyExpr+` AS _rowkey
		FROM logs `+wc.sql()+`
		ORDER BY time `+orderDir+`, `+logsRowKeyExpr+` `+orderDir+`
		LIMIT ? OFFSET ?
		SETTINGS max_execution_time = 30`, args...)
	if err != nil {
		return nil, 0, "", err
	}
	defer rows.Close()

	var out []LogRow
	var lastTimeNs int64
	var lastRowKey uint64
	for rows.Next() {
		var lr LogRow
		var t time.Time
		var rowKey uint64
		var attrK, attrV, resK, resV []string
		if err := rows.Scan(
			&t, &lr.SeverityNumber, &lr.SeverityText, &lr.Body,
			&lr.ServiceName, &lr.TraceID, &lr.SpanID,
			&attrK, &attrV, &resK, &resV, &rowKey,
		); err != nil {
			return nil, 0, "", err
		}
		lr.Timestamp = t.UnixNano()
		// _rowkey (logsRowKeyExpr) is the deterministic keyset tiebreak
		// AND the per-row identity the frontend depends on: LogTable keys
		// React rows on l.id and tracks expand state in a Set<id>. v0.7.77
		// dropped the old rowNumberInAllBlocks() id and left lr.ID=0, which
		// collapsed every CH-backed row to key=0 → duplicate React keys +
		// expanding one row expanded ALL of them. Assign the hash
		// (effectively unique among same-time rows) so each row carries a
		// stable distinct id at zero extra query cost — same shape the ES
		// backend already provides via stringToInt64ID. (v0.7.80 fix)
		lr.ID = rowKey
		lr.Attributes = arraysToMap(attrK, attrV)
		lr.ResourceAttributes = arraysToMap(resK, resV)
		out = append(out, lr)
		lastTimeNs = lr.Timestamp
		lastRowKey = rowKey
	}
	if err := rows.Err(); err != nil {
		return nil, 0, "", err
	}

	// NextCursor only when the page came back full — a short page is
	// the last page, so no cursor (the UI stops paging). Never on an
	// ascending read: the cursor encoding + keyset are DESC-only, and
	// the only ascending caller (Context "after" window) doesn't page.
	next := ""
	if len(out) == f.Limit && !ascending {
		next = EncodeLogsCursor(lastTimeNs, lastRowKey)
	}
	return out, total, next, nil
}

// ── Metric queries ────────────────────────────────────────────────────────────

func (s *Store) GetMetricNames(ctx context.Context, service string) ([]MetricInfo, error) {
	out, _, err := s.ListMetricNames(ctx, service, "", 0, 0)
	return out, err
}

// metricNameLookback bounds the metric-name picker (and the /metrics
// catalogue load) to metrics that have reported at least once in this
// window. The old query read raw metric_points with an UNBOUNDED
// GROUP BY metric — it scanned all retained history and blew the 30s
// max_execution_time at prod scale (operator-reported v0.8.311:
// /api/metrics/names took ~30s and the list never loaded). A bounded
// lookback prunes to a handful of daily partitions (PARTITION BY
// toDate(time)) and matches how Datadog/Honeycomb surface "recently
// active" metrics — a metric silent for a week isn't chartable on the
// live page anyway. The proper long-term fix is a metric-name catalog
// MV (mirroring the summary MVs the service/operation pickers read);
// this time bound is the immediate, distributed-safe remedy.
const metricNameLookback = 7 * 24 * time.Hour

// buildMetricNamesWhere assembles the WHERE shared by ListMetricNames'
// count + select queries. The `time >= since` bound is ALWAYS present —
// pinned by a test so a future edit can't silently regress to the
// full-history scan that caused the timeout.
func buildMetricNamesWhere(service, pattern string, since time.Time) whereClause {
	var wc whereClause
	wc.add("time >= ?", since)
	if service != "" {
		wc.add("service_name = ?", service)
	}
	if pattern != "" {
		like := strings.NewReplacer(`*`, `%`, `?`, `_`).Replace(pattern)
		if !strings.ContainsAny(pattern, "*?") {
			like = "%" + like + "%"
		}
		wc.add("metric ILIKE ?", like)
	}
	return wc
}

// buildMetricCatalogWhere assembles the metric_catalog WHERE (dims
// only — freshness is a HAVING on the merged last_seen state, added
// by the caller). Pure — table-tested (v0.8.396).
func buildMetricCatalogWhere(service, pattern string) whereClause {
	var wc whereClause
	if service != "" {
		wc.add("service_name = ?", service)
	}
	if pattern != "" {
		like := strings.NewReplacer(`*`, `%`, `?`, `_`).Replace(pattern)
		if !strings.ContainsAny(pattern, "*?") {
			like = "%" + like + "%"
		}
		wc.add("metric ILIKE ?", like)
	}
	return wc
}

// listMetricNamesFromCatalog is the metric_catalog fast path
// (v0.8.396): a few thousand catalog rows instead of the raw
// metric_points GROUP BY that outgrew max_execution_time at prod
// volume. Freshness rides HAVING maxMerge(last_seen_state) so a
// long-silent metric ages out of the picker.
func (s *Store) listMetricNamesFromCatalog(ctx context.Context, service, pattern string, limit, offset int, defaultUnlimited bool) ([]MetricInfo, int, error) {
	wc := buildMetricCatalogWhere(service, pattern)
	since := time.Now().Add(-metricNameLookback)

	var total uint64
	if !defaultUnlimited {
		if err := s.conn.QueryRow(ctx,
			`SELECT count() FROM (
				SELECT metric FROM metric_catalog `+wc.sql()+`
				GROUP BY metric
				HAVING maxMerge(last_seen_state) >= ?
			) SETTINGS max_execution_time = 10`,
			append(append([]any{}, wc.args...), since)...).Scan(&total); err != nil {
			return nil, 0, err
		}
	}

	query := `SELECT metric, anyMerge(description_state), anyMerge(unit_state), anyMerge(instrument_state)
		 FROM metric_catalog ` + wc.sql() + `
		 GROUP BY metric
		 HAVING maxMerge(last_seen_state) >= ?
		 ORDER BY metric`
	args := append(append([]any{}, wc.args...), since)
	if !defaultUnlimited {
		query += " LIMIT ? OFFSET ?"
		args = append(args, limit, offset)
	}
	query += " SETTINGS max_execution_time = 10"
	rows, err := s.conn.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []MetricInfo{}
	for rows.Next() {
		var m MetricInfo
		if err := rows.Scan(&m.Name, &m.Description, &m.Unit, &m.Type); err != nil {
			return nil, 0, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return out, int(total), nil
}

// ListMetricNames — server-side searchable counterpart for
// MetricNamePicker (v0.5.181). Same wildcard semantics as
// ListServiceNames / ListOperationNames: bare query = substring,
// `*` and `?` honoured.
//
// v0.8.396 (operator-reported PROD bug): the v0.8.311 7-day bound
// stopped saving the raw GROUP BY at current prod volume — the scan
// alone blows max_execution_time. Reads now go CATALOG-FIRST
// (metric_catalog MV, instant at any scale) and fall back to the
// bounded raw scan only when the catalog is empty or unreadable —
// the first minutes after an upgrade (the MV populates forward
// only) and pathological installs. An empty SEARCH result on a
// non-empty catalog is authoritative (no fallback): the raw scan
// would only re-find metrics silent for 7+ days, which the picker
// hides by design.
func (s *Store) ListMetricNames(ctx context.Context, service, pattern string, limit, offset int) ([]MetricInfo, int, error) {
	defaultUnlimited := limit == 0 && offset == 0 && pattern == ""
	if limit <= 0 {
		limit = 200
	}

	if out, total, err := s.listMetricNamesFromCatalog(ctx, service, pattern, limit, offset, defaultUnlimited); err == nil {
		if len(out) > 0 {
			return out, total, nil
		}
		// Empty catalog result: authoritative for a SEARCH (the
		// catalog itself has rows), fallback-worthy when the whole
		// catalog is empty (fresh upgrade window).
		if pattern != "" || service != "" {
			var any uint8
			if err := s.conn.QueryRow(ctx,
				`SELECT count() > 0 FROM metric_catalog LIMIT 1 SETTINGS max_execution_time = 5`,
			).Scan(&any); err == nil && any == 1 {
				return out, total, nil
			}
		}
		log.Printf("[chstore] metric_catalog empty — falling back to the raw metric-name scan (fills within minutes of first ingest)")
	} else {
		log.Printf("[chstore] metric_catalog read failed (%v) — falling back to the raw metric-name scan", err)
	}

	wc := buildMetricNamesWhere(service, pattern, time.Now().Add(-metricNameLookback))

	var total uint64
	if !defaultUnlimited {
		if err := s.conn.QueryRow(ctx,
			"SELECT count(DISTINCT metric) FROM metric_points "+wc.sql()+
				" SETTINGS max_execution_time = 30",
			wc.args...).Scan(&total); err != nil {
			return nil, 0, err
		}
	}

	query := `SELECT metric, any(description), any(unit), any(instrument)
		 FROM metric_points ` + wc.sql() +
		` GROUP BY metric ORDER BY metric`
	args := append([]any{}, wc.args...)
	if !defaultUnlimited {
		query += " LIMIT ? OFFSET ?"
		args = append(args, limit, offset)
	}
	query += " SETTINGS max_execution_time = 30"
	rows, err := s.conn.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []MetricInfo
	for rows.Next() {
		var mi MetricInfo
		if err := rows.Scan(&mi.Name, &mi.Description, &mi.Unit, &mi.Type); err != nil {
			return nil, 0, err
		}
		out = append(out, mi)
	}
	if defaultUnlimited {
		total = uint64(len(out))
	}
	return out, int(total), rows.Err()
}

func (s *Store) GetMetricPoints(ctx context.Context, metric, service string, from, to time.Time, limit int) ([]MetricPointRow, error) {
	var wc whereClause
	wc.add("metric = ?", metric)
	if service != "" {
		wc.add("service_name = ?", service)
	}
	if !from.IsZero() {
		wc.add("time >= ?", from)
	}
	if !to.IsZero() {
		wc.add("time <= ?", to)
	}
	if limit == 0 {
		limit = 500
	}
	rows, err := s.conn.Query(ctx,
		`SELECT time, value, count, sum_value,
		        arrayStringConcat(arrayMap((k, v) -> concat(k, '=', v), attr_keys, attr_values), ',')
		 FROM metric_points `+wc.sql()+
			` ORDER BY time ASC LIMIT ?`,
		append(wc.args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MetricPointRow
	for rows.Next() {
		var p MetricPointRow
		var t time.Time
		// v0.8.325 — the ignored Scan error was the file's one outlier:
		// a per-row decode failure appended a ZERO-VALUE point and the
		// call still reported success (rows.Err() doesn't cover Scan),
		// silently corrupting chart data instead of failing loudly.
		if err := rows.Scan(&t, &p.Value, &p.Count, &p.Sum, &p.Attrs); err != nil {
			return nil, err
		}
		p.Time = t.UnixNano()
		out = append(out, p)
	}
	return out, rows.Err()
}

// ── WHERE clause builder ──────────────────────────────────────────────────────

type whereClause struct {
	conds []string
	args  []interface{}
}

func (w *whereClause) add(cond string, args ...interface{}) {
	w.conds = append(w.conds, cond)
	w.args = append(w.args, args...)
}

func (w *whereClause) sql() string {
	if len(w.conds) == 0 {
		return ""
	}
	return "WHERE " + strings.Join(w.conds, " AND ")
}

// BuildFilterWhere returns the SQL `WHERE ...` fragment + the
// positional args for a given FilterExpr[] (v0.5.261). Public
// adapter so packages outside chstore (currently internal/api's
// context-aware attribute-keys handler) can build filtered
// queries without depending on the internal whereClause type or
// re-implementing ApplyFilters' translation logic.
//
// Returns ("", nil) for an empty filter set — caller can safely
// concat the result onto a base SQL string without checking.
func BuildFilterWhere(filters []FilterExpr) (string, []any) {
	if len(filters) == 0 {
		return "", nil
	}
	var wc whereClause
	ApplyFilters(&wc, filters)
	return wc.sql(), wc.args
}

// BuildFilterGroupWhere is the grouped-AND/OR companion to BuildFilterWhere
// (v0.8.x gap-2). Returns the `WHERE …` fragment + args for a FilterGroup, or
// ("", nil) when the group contributes no compilable terms. A flat-AND group
// matches BuildFilterWhere's output (each leaf a separate ` AND ` conjunct)
// via the same ApplyFilterGroup→ApplyFilters delegation the repo layer uses.
func BuildFilterGroupWhere(g FilterGroup) (string, []any) {
	var wc whereClause
	ApplyFilterGroup(&wc, g)
	if len(wc.conds) == 0 {
		return "", nil
	}
	return wc.sql(), wc.args
}
