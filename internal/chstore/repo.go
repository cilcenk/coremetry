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
	"sync/atomic"
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
//   - async_insert_max_data_size = 10MB  (up from 1MB default)
//     — bigger coalescence per server-side flush, fewer disk
//     writes per row at burst peaks.
//   - async_insert_busy_timeout_ms = 1000  (up from 200ms)
//     — wait up to 1s collecting concurrent inserts before
//     flushing. Trade a tiny visibility lag for far fewer
//     write operations at sustained load.
//   - async_insert_stale_timeout_ms = 1000
//     — flush a partial buffer 1s after the last insert so
//     low-traffic services (test envs, off-hours) don't sit
//     on rows indefinitely.
//
// v0.10.240 (perf audit ING-1/ING-2/ING-3, docs/audit/performance-profile.md):
//   - async_insert_deduplicate = 1 — spans_local flush'ı 19 MV'yi tetikler;
//     kaskad ortasında zaman aşımı ("Timeout exceeded … while pushing to
//     view") kaynak part'ı commit edilmiş hâlde döner ve consumer aynı
//     batch'i 3 kez yeniden dener → span'ler ÇİFT, MV oranları çift
//     (yerelde 43 FlushError/24 s). Replicated tablolarda aynı bloğun
//     yeniden gönderimi hash'le düşer (wait_for_async_insert=1 şart, var);
//     Replicated olmayan tabloda ayar etkisiz — kural zararsız.
//   - parallel_view_processing = 1 — MV'ler sırayla değil paralel itilir;
//     flush gecikmesi satır hacminden değil kaskaddan geliyordu.
//   - async_insert_busy_timeout_min_ms = 500 — adaptif busy timeout tabanı
//     (varsayılan 50 ms): 8 flusher'ın ~aynı anda gönderdiği küçük
//     INSERT'ler tek flush'ta birleşsin; part churn (yerelde 305 part/saat/
//     node, 41 satır/part) düşsün. Tavan 1000 ms değişmedi.
func asyncInsertCtx(ctx context.Context) context.Context {
	return WithQuerySettings(ctx, asyncInsertSettings()) // v0.10.254 — tek kapı (log_comment ile birleşir)
}

// asyncInsertSettings — SAF; async_insert_settings_test.go pinler
// (CLAUDE.md: async_insert mekanizmasını bozma).
func asyncInsertSettings() clickhouse.Settings {
	return clickhouse.Settings{
		"async_insert":                     1,
		"wait_for_async_insert":            1,
		"async_insert_deduplicate":         1,
		"async_insert_max_data_size":       10_485_760,
		"async_insert_busy_timeout_min_ms": 500,
		"async_insert_busy_timeout_ms":     1000,
		"async_insert_stale_timeout_ms":    1000,
		"parallel_view_processing":         1,
	}
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
	batch, err := s.ingestWriteConn().PrepareBatch(ctx, spansInsertSQL(withOpGroup))
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
	batch, err := s.ingestWriteConn().PrepareBatch(ctx, "INSERT INTO logs")
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
func metricsInsertSQL(withSeriesFp, withIsMonotonic bool) string {
	sql := `INSERT INTO metric_points
		(metric, instrument, description, unit,
		 service_name, host_name, time, start_time,
		 value, count, sum_value, min_value, max_value,
		 attr_keys, attr_values, res_keys, res_values,
		 bucket_bounds, bucket_counts, temporality`
	if withSeriesFp {
		sql += ", series_fingerprint"
	}
	// v0.9.106 (F2) — is_monotonic conditional, series_fingerprint'ten SONRA
	// (append sırası metricAppendArgs ile lockstep; external-distributed'da
	// kolon yoksa her ikisi de atlanır).
	if withIsMonotonic {
		sql += ", is_monotonic"
	}
	return sql + ")"
}

// metricAppendArgs builds the positional Append() arguments for one metric
// point in the EXACT order of metricsInsertSQL, appending series_fingerprint
// LAST iff withSeriesFp — the same lockstep guarantee spanAppendArgs gives
// the spans INSERT (v0.8.186 discipline).
func metricAppendArgs(p *MetricPoint, withSeriesFp, withIsMonotonic bool) []any {
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
	// is_monotonic — series_fingerprint'ten SONRA (metricsInsertSQL kolon
	// sırasıyla lockstep).
	if withIsMonotonic {
		args = append(args, p.IsMonotonic)
	}
	return args
}

func (s *Store) InsertMetrics(ctx context.Context, pts []*MetricPoint) error {
	ctx = asyncInsertCtx(ctx)
	// series_fingerprint + is_monotonic are bound ONLY when the columns are
	// actually present on the table the write fans out to (probed once at boot)
	// — see metricsInsertSQL for the failure mode this prevents.
	withSeriesFp := s.hasSeriesFpCol
	withIsMonotonic := s.hasIsMonotonicCol
	batch, err := s.ingestWriteConn().PrepareBatch(ctx, metricsInsertSQL(withSeriesFp, withIsMonotonic))
	if err != nil {
		return fmt.Errorf("prepare metrics: %w", err)
	}
	for _, p := range pts {
		if err := batch.Append(metricAppendArgs(p, withSeriesFp, withIsMonotonic)...); err != nil {
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
// Returns ” when no signal is present — callers comparing
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

// clusterColExpr — promoted `cluster` MATERIALIZED kolonunu OKUR, başka
// bir şey yapmaz.
//
// v0.9.692 (perf taraması #3) — ESKİ HÂLİ `coalesce(nullIf(cluster,”),
// clusterDeriveExpr)` idi ve gerekçesi YANLIŞTI. Yorum şöyle diyordu:
// "CH short-circuits coalesce, so new parts never pay the indexOf()
// scan". Kısa devre SATIR YÜRÜTMESİNİ atlar, KOLON OKUMASINI değil:
// ifadede geçen res_values/attr_values dizileri her satır için diskten
// okunuyordu.
//
// ÖLÇÜLDÜ (chc-0, aynı 3 saatlik pencere, aynı çıktı):
//
//	coalesce fallback : 137.15 MiB / 247k satır = 566 B/satır · 81 ms
//	düz kolon         :   8.71 MiB / 1.015M satır = 8.6 B/satır · 11 ms
//	                                           → ~66× bayt, ~7× süre
//
// FALLBACK YAPISAL OLARAK GEREKSİZ, yalnız ampirik değil:
//  1. `cluster` kolonu store.go:852'de `MATERIALIZED clusterDeriveExpr`
//     — AYNI ifade. MATERIALIZED kolon, onu saklamayan eski parçalarda
//     OKUMA ANINDA hesaplanır; yani kolon zaten türetilmiş değeri
//     döndürüyor.
//  2. Kolon hiç yoksa (harici Distributed, ALTER uygulanmamış)
//     clusterExpr() zaten ham clusterDeriveExpr'e düşüyor — bu sabit
//     yalnız kolon VARKEN kullanılıyor.
//
// Yani fallback ikinci, gereksiz bir değerlendirmeydi.
//
// Ölçüm de bunu doğruladı: 6 saatte 20.383 boş-cluster satırın
// türetmeyle kurtarılanı SIFIR.
//
// hasClusterCol boot probu KALIYOR (clusterExpr'deki dal) — harici
// Distributed yolu ona bağlı ve v0.8.162'de operatör-bildirimli bir
// code 47 olayını o çözmüştü.
const clusterColExpr = `cluster`

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
	rows, err := s.telemetryReadConn().Query(ctx, `
		SELECT service_name, `+s.clusterExpr()+` AS cluster
		FROM spans
		WHERE time >= ? AND service_name != ''
		GROUP BY service_name, cluster
		HAVING cluster != ''
		ORDER BY service_name, cluster
		LIMIT 50000
		SETTINGS max_execution_time = 8`+s.heavyScanSpill(), from)
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
	row := s.telemetryReadConn().QueryRow(ctx, `
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
	rows, err := s.telemetryReadConn().Query(ctx, sql, args...)
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
	rows, err := s.telemetryReadConn().Query(ctx, `
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

// heavyScanSpill moved to query_memory.go as a Store method
// (v0.9.975): the 8 GiB ceiling it pinned was LARGER than the whole
// server budget on a 2.8 GiB node, so it never fired and CH shot a
// bystander instead. Same requested bytes, now held under a share of
// the server's own max_server_memory_usage.

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
// ServicesQuery is the /services read, as a struct.
//
// v0.9.345 — GetServicesFilteredIn had grown to twelve positional arguments
// and the three filters this release pushes into SQL would have made fifteen.
// The struct exists so the next filter is a named field rather than another
// unlabelled bool three commas deep. GetServicesFilteredIn stays as a thin
// wrapper: its six callers are unchanged.
type ServicesQuery struct {
	Since         time.Duration
	From, To      time.Time
	NameMatch     string
	ServiceIn     []string
	Sort, Dir     string
	Limit, Offset int
	Cluster, Env  string

	Display ServiceDisplayFilters
}

// ServiceDisplayFilters are the three /services controls that ran in the
// BROWSER until v0.9.345, over the 50 rows of the current page. At 1000s of
// services "Errors only" could empty page 1 while erroring services sat on
// page 7, and nothing on screen said so.
//
// They are plain aggregate predicates, so they belong in the HAVING where the
// LIMIT/OFFSET can respect them — the same correction the triage queue's
// filters got in v0.9.322/330/335/336 and /api/problems in v0.9.342.
type ServiceDisplayFilters struct {
	ErrorsOnly bool
	MinSpans   uint64
	MinP99Ms   float64
}

// Active reports whether any constraint is set — the caller uses it to decide
// whether the page's own "filtered on this page only" warning still applies.
func (f ServiceDisplayFilters) Active() bool {
	return f.ErrorsOnly || f.MinSpans > 0 || f.MinP99Ms > 0
}

// having renders the predicates against the column ALIASES of the calling
// query. The raw-spans path and the MV path name the same quantities
// differently (span_count/error_count vs spans/errs), so the aliases are
// parameters rather than literals: one predicate builder, two callers, no way
// for the two paths to drift into disagreeing about what "Errors only" means.
//
// Zero means "no constraint" — that is what an empty input box sends, and a
// service with genuinely zero spans is not something the operator asked to
// exclude by leaving the field blank.
func (f ServiceDisplayFilters) having(spansCol, errsCol, p99Col string) (string, []any) {
	var parts []string
	var args []any
	if f.ErrorsOnly {
		parts = append(parts, errsCol+" > 0")
	}
	if f.MinSpans > 0 {
		parts = append(parts, spansCol+" >= ?")
		args = append(args, f.MinSpans)
	}
	if f.MinP99Ms > 0 {
		parts = append(parts, p99Col+" >= ?")
		args = append(args, f.MinP99Ms)
	}
	if len(parts) == 0 {
		return "", nil
	}
	return " HAVING " + strings.Join(parts, " AND "), args
}

func (s *Store) GetServicesFilteredIn(ctx context.Context, since time.Duration, from, to time.Time, nameMatch string, serviceIn []string, sort, dir string, limit, offset int, cluster, env string) ([]ServiceSummary, error) {
	return s.GetServicesQuery(ctx, ServicesQuery{
		Since: since, From: from, To: to, NameMatch: nameMatch, ServiceIn: serviceIn,
		Sort: sort, Dir: dir, Limit: limit, Offset: offset, Cluster: cluster, Env: env,
	})
}

func (s *Store) GetServicesQuery(ctx context.Context, q ServicesQuery) ([]ServiceSummary, error) {
	since, from, to := q.Since, q.From, q.To
	nameMatch, serviceIn := q.NameMatch, q.ServiceIn
	sort, dir := q.Sort, q.Dir
	limit, offset := q.Limit, q.Offset
	cluster, env := q.Cluster, q.Env
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

	havingSQL, havingArgs := q.Display.having("span_count", "error_count", "p99_ms")

	rows, err := s.telemetryReadConn().Query(ctx, `
		SELECT service_name,
		       count()                                  AS span_count,
		       countIf(status_code = 'error')           AS error_count,
		       avg(duration) / 1e6                      AS avg_ms,
		       quantile(0.99)(duration) / 1e6           AS p99_ms,
		       (countIf(duration <= ?* 1e6) + countIf(duration > ? * 1e6 AND duration <= ? * 1e6) / 2)
		         / nullIf(count(), 0)                   AS apdex
		FROM spans `+wc.sql()+`
		GROUP BY service_name`+havingSQL+`
		ORDER BY `+servicesSortExpr(sort, dir)+limitClause+`
		SETTINGS max_execution_time = 20`+s.heavyScanSpill(),
		append(append([]any{apdexT, apdexT, apdexT * 4}, wc.args...), havingArgs...)...)
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

// SparklineBuckets is the MAXIMUM slot count for the inline sparklines
// (operations table, endpoints, infra metrics). Granular-sparklines
// sweep (M4, 2026-07-24 — operator: "sadece çizgisel gösteriyor, daha
// granüle olsun"): 30 → 120, so a 24h window renders 12-min slots
// instead of 48-min mush. This constant is the SINGLE source for every
// sparkline grid; the frontend derives the axis from array length, so
// variable-length arrays are safe (GetEndpointsMV precedent).
const SparklineBuckets = 120

// sparklineGrid computes the sparkline slot width + slot count for a
// winSec-second window over a source whose native resolution is
// grainSec (300 for the 5-min summary MVs, 60/10 for the spanmetrics
// tiers, 1 for raw spans). The slot width is always an INTEGER
// MULTIPLE of the grain (v0.9.207, review finding 6): MV rows sit on
// exact grainSec boundaries, so a non-multiple width (24h/120 = 720s
// over the 300s operations MV) aliases deterministically — intDiv
// packs 3 MV rows into some slots and 2 into others, and steady
// traffic renders as a periodic fake 1.5-2x spike on every sparkline.
// Rounding the ceil(win/N) width UP to the next grain multiple makes
// every full slot cover the same number of MV buckets; callers anchor
// the intDiv origin on a grain boundary so slot edges land on bucket
// edges. Short windows return FEWER, real slots (slot == MV bucket —
// the pre-sweep sub-grain comb, 1h @ 30 slots = 12 filled 18 zero,
// stays fixed) and long windows cap at SparklineBuckets. Fewer,
// honest slots beat more, sawtoothed slots: the frontend derives the
// axis from array length, so n < SparklineBuckets is safe. Callers
// size their arrays with n — never with the constant.
func sparklineGrid(winSec, grainSec int64) (bucketSec int64, n int) {
	if grainSec < 1 {
		grainSec = 1
	}
	if winSec < 1 {
		return grainSec, 1
	}
	bucketSec = (winSec + int64(SparklineBuckets) - 1) / int64(SparklineBuckets)
	// Round UP to a grain multiple — this also enforces the >= grain
	// floor the pre-v0.9.207 code had.
	bucketSec = (bucketSec + grainSec - 1) / grainSec * grainSec
	n = int((winSec + bucketSec - 1) / bucketSec)
	if n < 1 {
		n = 1
	}
	if n > SparklineBuckets {
		n = SparklineBuckets
	}
	return bucketSec, n
}

// latSparkCap — avg/p50/p95 latency serileri yalnız ilk N satıra
// (v0.9.64, review perf bulgusu): satırlar span_count desc sıralı,
// 150+ sırası fold altında; skaler değer + TrendDelta her satırda
// kalır. calls/errors/p99 serileri kapsam dışı (pre-v0.9.60 sözleşme).
const latSparkCap = 150

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
// scanner, and frontend render identically. The ungrouped ” bucket
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
	//
	// v0.9.1169 — ÜST sınır ise DIŞLAYICI (`< winEnd`), ve bu alt sınırın
	// simetriği değil: winStart'ı İÇEREN kova pencere verisi taşır, `winEnd`
	// ETİKETLİ kova [winEnd, +5dk) taşır — pencereden sıfır veri. Sınıfın
	// gerekçesi summary.go alignBucketStart doc bloğunda (v0.9.823 → 1156 →
	// 1167 → 1168).
	//
	// Burada İKİNCİ bir belirti daha vardı ve daha keskindi: fazla kova
	// birinci geçişin satır toplamına giriyor, ama sparkline'da
	// bidx = intDiv(winEnd-bucketStart, bucketSec) = nBuckets çıkıyor ve
	// aşağıdaki `int(bidx) < nBuckets` kapısına takılıp SESSİZCE düşüyordu.
	// Yani satırdaki "1.200 çağrı" ile sparkline'ın topladığı sayı
	// ayrışıyordu — aynı sorgu çiftinden iki farklı cevap.
	bucketStart := winStart.Truncate(5 * time.Minute)
	// First pass: aggregate rollup per name across the window.
	// nameCol/mvTable/opFilter are server-side constants (never user
	// input) so the interpolation is injection-safe; the window bounds
	// stay parameterised.
	const apdexT = 200.0
	rows, err := s.telemetryReadConn().Query(ctx, `
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
		WHERE service_name = ? AND time_bucket >= ? AND time_bucket < ?`+opFilter+`
		GROUP BY `+nameCol+`
		ORDER BY span_count DESC
		LIMIT 500
		SETTINGS max_execution_time = 25,
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

	// Second pass: per-bucket counts for the inline sparkline. The
	// grid is grain-multiple-aligned (sparklineGrid, v0.9.207): a 1h
	// window ships 12 REAL slots (slot == MV bucket), a 24h window
	// 96 × 15-min slots. Window measured from bucketStart — the
	// same aligned origin the bidx projection uses — so the bucket
	// containing a misaligned winStart isn't pushed past the last
	// slot and dropped.
	winSec := int64(winEnd.Sub(bucketStart).Seconds())
	if winSec <= 0 {
		return out, nil
	}
	bucketSec, nBuckets := sparklineGrid(winSec, 300)
	// v0.9.60 — quantile merge 0.99'dan (0.5,0.95,0.99)'a genişledi +
	// slot başına süre toplamı (avg serisi için): Elastic-parity latency
	// hücresinin percentile-seçicili sparkline'ı. Aynı tek scan.
	bucketRows, err := s.telemetryReadConn().Query(ctx, `
		SELECT `+nameCol+` AS name,
		       intDiv(toUInt32(time_bucket) - toUInt32(?), ?) AS bidx,
		       countMerge(span_count_state)                   AS c,
		       countMerge(error_count_state)                  AS e,
		       sumMerge(duration_sum_state) / 1e6             AS sum_ms,
		       arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 1) / 1e6 AS p50,
		       arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 2) / 1e6 AS p95,
		       arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 3) / 1e6 AS p99
		FROM `+mvTable+`
		WHERE service_name = ? AND time_bucket >= ? AND time_bucket < ?`+opFilter+`
		GROUP BY `+nameCol+`, bidx
		SETTINGS max_execution_time = 25,
		         `+s.shardSkipSetting()+`, `+mvQuantileMemSettings,
		bucketStart, bucketSec, service, bucketStart, winEnd)
	if err != nil {
		// Sparkline failure non-fatal — return aggregates without.
		return out, nil
	}
	defer bucketRows.Close()
	// Avg serisi için slot başına süre toplamı ayrı birikir; döngü
	// sonunda count'a bölünür (aynı slota birden çok MV bucket'ı
	// düşebildiğinden bölme en sonda yapılmalı).
	sumByIdx := map[int][]float64{}
	for bucketRows.Next() {
		var name string
		var bidx int64
		var c, e uint64
		var sumMs, p50, p95, p99 *float64
		if err := bucketRows.Scan(&name, &bidx, &c, &e, &sumMs, &p50, &p95, &p99); err != nil {
			continue
		}
		i, ok := idxByName[name]
		if !ok {
			continue
		}
		// v0.9.64 (review perf) — YENİ latency serileri (avg/p50/p95)
		// yalnız ilk latSparkCap satıra: satırlar span_count desc gelir,
		// 150+ sırası fold altında ve değer+delta zaten skalerden.
		// calls/errors/p99 TÜM satırlarda kalır (pre-v0.9.60 sözleşme —
		// detay modalı onları okur).
		wantLat := i < latSparkCap
		if out[i].Sparkline == nil {
			out[i].Sparkline = make([]uint64, nBuckets)
			out[i].ErrorsSparkline = make([]uint64, nBuckets)
			out[i].P99Sparkline = make([]float64, nBuckets)
			if wantLat {
				out[i].P95Sparkline = make([]float64, nBuckets)
				out[i].P50Sparkline = make([]float64, nBuckets)
				out[i].AvgSparkline = make([]float64, nBuckets)
				sumByIdx[i] = make([]float64, nBuckets)
			}
		}
		if bidx >= 0 && int(bidx) < nBuckets {
			out[i].Sparkline[bidx] += c
			out[i].ErrorsSparkline[bidx] += e
			// Per-bucket quantiles — pick the max across coalesced MV
			// buckets that map to the same sparkline slot, same
			// idiom as the topology MV merges. Conservative read.
			// round2: payload disiplini (v0.9.64).
			if v := round2(safeF(p99)); v > out[i].P99Sparkline[bidx] {
				out[i].P99Sparkline[bidx] = v
			}
			if wantLat {
				sumByIdx[i][bidx] += safeF(sumMs)
				if v := round2(safeF(p95)); v > out[i].P95Sparkline[bidx] {
					out[i].P95Sparkline[bidx] = v
				}
				if v := round2(safeF(p50)); v > out[i].P50Sparkline[bidx] {
					out[i].P50Sparkline[bidx] = v
				}
			}
		}
	}
	for i, sums := range sumByIdx {
		for b := 0; b < nBuckets; b++ {
			if out[i].Sparkline[b] > 0 {
				out[i].AvgSparkline[b] = round2(sums[b] / float64(out[i].Sparkline[b]))
			}
		}
	}
	_ = apdexT // referenced by the raw-spans path; kept here so a future move keeps the constants together.
	return out, nil
}

// GetOperationSummaryCompared — GetOperationSummary + bir-önceki
// eş-uzunluklu pencerenin skalerleri ve calls/errors gölge serileri,
// isimle merge edilmiş (v0.9.60, Endpoints ?compare=prior deseninin
// operations karşılığı). Prior pencere okuma hatası soft-düşer:
// current sonuç Prior'suz döner (karşılaştırma görünmez-düşer).
func (s *Store) GetOperationSummaryCompared(ctx context.Context, service string, since time.Duration, from, to time.Time, normalized bool, env string) ([]OperationSummary, error) {
	// Pencereyi BURADA mutlaklaştır ki current ve prior birebir aynı
	// uzunlukta olsun (GetOperationSummary'nin kendi now()'u iki çağrı
	// arasında kayardı).
	winStart, winEnd := from, to
	if winStart.IsZero() {
		winEnd = time.Now()
		if since <= 0 {
			since = 24 * time.Hour
		}
		winStart = winEnd.Add(-since)
	} else if winEnd.IsZero() {
		winEnd = time.Now()
	}
	cur, err := s.GetOperationSummary(ctx, service, 0, winStart, winEnd, normalized, env)
	if err != nil || len(cur) == 0 {
		return cur, err
	}
	dur := winEnd.Sub(winStart)
	// v0.9.64 (review MAJÖR) — prior pencerenin sonu, winStart'ı içeren
	// 5dk MV bucket'ını DIŞLAMALI: queryOperationsFromMV başlangıcı
	// aşağı yuvarlayıp sonu dahil ettiğinden floor5(winStart) bucket'ı
	// her iki pencereye TAM olarak giriyordu — deploy-compare'de
	// deploy-sonrası hatalar prior'a sızıp TrendDelta'yı sulandırıyordu
	// (15dk pencerede ~1/3'e kadar). floor5(ws)-1s: bucket-start
	// karşılaştırmasında önceki bucket'ta biter.
	priorEnd := winStart.Truncate(5 * time.Minute).Add(-time.Second)
	prior, perr := s.GetOperationSummary(ctx, service, 0, winStart.Add(-dur), priorEnd, normalized, env)
	if perr != nil {
		return cur, nil
	}
	byName := make(map[string]OperationSummary, len(prior))
	for _, p := range prior {
		byName[p.Name] = p
	}
	for i := range cur {
		p, ok := byName[cur[i].Name]
		if !ok {
			continue
		}
		cur[i].HasPrior = true
		cur[i].PriorSpanCount = p.SpanCount
		cur[i].PriorErrorCount = p.ErrorCount
		cur[i].PriorErrorRate = p.ErrorRate
		cur[i].PriorAvgMs = p.AvgMs
		cur[i].PriorP50Ms = p.P50Ms
		cur[i].PriorP95Ms = p.P95Ms
		cur[i].PriorP99Ms = p.P99Ms
		cur[i].PriorSparkline = p.Sparkline
		cur[i].PriorErrorsSparkline = p.ErrorsSparkline
	}
	return cur, nil
}

// operationsUseMV — /service Operations tablosunun MV hızlı-yol kapısı.
// operation_summary_5m 5 dakikada bir satır yazar (sub-5m pencere boş
// okur) ve NE cluster NE deploy_env boyutu taşır — env verildiğinde
// (cluster'ın v0.8.385'te /services'te yaptığı gibi) MV diskalifiye olur
// ve okuma ham-spans yoluna (deploy_env = ?) düşer. servicesUseMV
// (api.go) ile AYNI şekil; saf tutuldu ki operation_env_test.go çivilesin.
func operationsUseMV(window time.Duration, env string) bool {
	return window >= 5*time.Minute && env == ""
}

// GetOperationSummary returns per-operation aggregates for a single
// service: count, error rate, p50/p95/p99 latency, apdex, plus a
// fixed-length call-rate sparkline over the same window. Drives the
// "Operations" table on the service detail page.
//
// env (v0.9.1039) — global Topbar picker. Non-empty forces the raw-spans
// path (operationsUseMV) with a deploy_env conjunct, the same MV-lacks-env
// trade-off getDatabasesRaw / GetEndpoints (raw) / GetServicesQuery make. Rows ordered by span
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
// fallback group by op_group and exclude the ungrouped ” bucket. The
// OperationSummary.Name field carries the op_group value in that mode, so
// the scanner, sparkline, and frontend are unchanged. normalized=false is
// byte-for-byte the pre-rel-B behaviour.
func (s *Store) GetOperationSummary(ctx context.Context, service string, since time.Duration, from, to time.Time, normalized bool, env string) ([]OperationSummary, error) {
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
	useMV := operationsUseMV(winEnd.Sub(winStart), env)
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
	// v0.9.1039 — global env narrow. deploy_env is a settled spans column
	// (getDatabasesRaw / GetEndpoints raw use it verbatim); the same
	// conjunct rides both the aggregate query below AND the sparkline query
	// (both read wc.args), so raw + normalized modes narrow identically.
	if env != "" {
		wc.add("deploy_env = ?", env)
	}
	if normalized {
		rawNameCol = "op_group"
		wc.add("op_group != ''") // no placeholder — adds zero args
	}
	const apdexT = 200.0
	rows, err := s.telemetryReadConn().Query(ctx, `
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
		         optimize_skip_unused_shards = 0`+s.heavyScanSpill(),
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
	// Raw spans have second resolution, so the grid runs at grain=1:
	// even a sub-5-min window gets real per-slot data. n falls out of
	// sparklineGrid (≤ SparklineBuckets); arrays are sized with it.
	bucketSec, nBuckets := sparklineGrid(winSec, 1)

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
	// v0.9.64 (review m7) — raw yol da avg/p50/p95 serilerini üretir:
	// v0.9.61'in Latency kolonu p95 default'uyla sub-5dk pencerede
	// (raw yol) en görünür hücre '—' basıyordu. MV yolundaki genişletmenin
	// aynısı; quantileTDigest raw'da da ~%2 hata ile yeter (pitfall
	// kuralı: 1M+ satırda quantile() değil).
	sparkRows, err := s.telemetryReadConn().Query(ctx, `
		SELECT `+rawNameCol+` AS name,
		       intDiv(toUInt32(time) - toUInt32(?), ?) AS bidx,
		       count()                                 AS c,
		       countIf(status_code = 'error')          AS e,
		       sum(duration) / 1e6                     AS sum_ms,
		       quantileTDigest(0.5)(duration) / 1e6    AS p50,
		       quantileTDigest(0.95)(duration) / 1e6   AS p95,
		       quantileTDigest(0.99)(duration) / 1e6   AS p99
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
	rawSums := map[int][]float64{}
	for sparkRows.Next() {
		var name string
		var bidx int64
		var c, e uint64
		var sumMs, p50, p95, p99 *float64
		if err := sparkRows.Scan(&name, &bidx, &c, &e, &sumMs, &p50, &p95, &p99); err != nil {
			continue
		}
		i, ok := idxByName[name]
		if !ok {
			continue
		}
		if out[i].Sparkline == nil {
			out[i].Sparkline = make([]uint64, nBuckets)
			out[i].ErrorsSparkline = make([]uint64, nBuckets)
			out[i].P99Sparkline = make([]float64, nBuckets)
			out[i].P95Sparkline = make([]float64, nBuckets)
			out[i].P50Sparkline = make([]float64, nBuckets)
			out[i].AvgSparkline = make([]float64, nBuckets)
			rawSums[i] = make([]float64, nBuckets)
		}
		if bidx >= 0 && int(bidx) < nBuckets {
			out[i].Sparkline[bidx] += c
			out[i].ErrorsSparkline[bidx] += e
			rawSums[i][bidx] += safeF(sumMs)
			if v := round2(safeF(p99)); v > out[i].P99Sparkline[bidx] {
				out[i].P99Sparkline[bidx] = v
			}
			if v := round2(safeF(p95)); v > out[i].P95Sparkline[bidx] {
				out[i].P95Sparkline[bidx] = v
			}
			if v := round2(safeF(p50)); v > out[i].P50Sparkline[bidx] {
				out[i].P50Sparkline[bidx] = v
			}
		}
	}
	for i, sums := range rawSums {
		for b := 0; b < nBuckets; b++ {
			if out[i].Sparkline[b] > 0 {
				out[i].AvgSparkline[b] = round2(sums[b] / float64(out[i].Sparkline[b]))
			}
		}
	}
	return out, nil
}

// round2 — sparkline float'ları 2 ondalığa yuvarlanır (v0.9.64, review
// perf bulgusu): Go'nun shortest-round-trip marshal'ı tdigest/1e6
// bölümlerinden 17-19 karakterlik float'lar basıyordu; 500-op tavanında
// yanıt ~1.35MB'a şişmişti. 2 ondalık ms hassasiyeti sparkline için
// fazlasıyla yeter, gövdeyi ~%60 küçültür.
func round2(v float64) float64 {
	return math.Round(v*100) / 100
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
	rows, err := s.telemetryReadConn().Query(ctx,
		`SELECT name, count() AS c
		 FROM spans `+wc.sql()+`
		 GROUP BY name
		 ORDER BY c DESC
		 LIMIT 500
		 SETTINGS max_execution_time = 10`, wc.args...)
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
//     a. peer.service                (OTel SDK hint)
//     b. rpc.service                 (gRPC contract — the same string
//     Grafana Tempo's traces-drilldown
//     uses to bucket child gRPC calls)
//     c. server.address / http.host  (HTTP downstream)
//     d. db.system                   (DB engine)
//     e. messaging.system            (queue / topic broker)
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
	//
	// v0.9.1171 — üst sınır DIŞLAYICI, alt sınırın simetriği DEĞİL:
	// startTime'ı İÇEREN kova pencere verisi taşır (almamak kayıp),
	// endTime ETİKETLİ kova [endTime, +5dk) taşır — pencereden sıfır veri.
	// Kenar ağırlıkları grafiğin kalınlıklarını sürdüğü için fazla kova
	// topolojiyi sessizce yeniden ölçekliyordu. Gerekçe: summary.go
	// alignBucketStart doc bloğu.
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
		WHERE time_bucket >= ? AND time_bucket < ?` + svcWhere + `
		  AND ` + TopoCallEdgeFilterSQL + `
		  AND parent_service != child_node
		GROUP BY parent_service, child_node
		ORDER BY call_count DESC
		SETTINGS max_execution_time = 10`
	if topN > 0 {
		sql += fmt.Sprintf("\n\t\tLIMIT %d", topN)
	}

	rows, err := s.telemetryReadConn().Query(ctx, sql, args...)
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
	"http.method": "http_method",
	// v0.9.628 — GÜNCEL semconv yazımları; ingest ikisini de aynı kolona
	// yazıyor (otlp/semconv.go). Gösterim yolu da ikisini tanımalı.
	"http.request.method":       "http_method",
	"http.response.status_code": "toString(http_status)",
	"db.system.name":            "db_system",
	"db.query.text":             "db_statement",
	"http.route":                "http_route",
	"http.status_code":          "toString(http_status)",
	"db.system":                 "db_system",
	"db.statement":              "db_statement",
	"rpc.system":                "rpc_system",
	"rpc.method":                "rpc_method",
	"peer.service":              "peer_service",
	"messaging.system":          "msg_system",
	"service.name":              "service_name",
	"deployment.environment":    "deploy_env",
	// Current semconv spelling (≥1.27) — same typed column (v0.8.379).
	"deployment.environment.name": "deploy_env",
	"host.name":                   "host_name",
	"kind":                        "kind",
}

type TraceFilter struct {
	Service  string
	Search   string
	TraceID  string // exact 32-hex match only (prefix search removed v0.9.82)
	From, To time.Time
	HasError bool
	// MVGap (v0.10.124) — pencere trace_summary_5m'de BOŞLUK olan bir güne
	// değiyor (trace_mv_coverage.go); Store girişinde doldurulur ve
	// tracesMVEligible'ı düşürür → ham yol. Çağıran vermez.
	MVGap bool
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
	Env string
	// Cluster narrows to spans whose derived k8s / openshift cluster name
	// matches exactly (v0.9.943 — UX denetimi B3/Ö5). Same first-class
	// reasoning as Env: FilterRoot SUPERSEDES Filters, so an appended
	// cluster leaf would silently vanish under a grouped OR filter.
	//
	// The match rides clusterExpr() — the promoted `cluster` column when
	// the read path resolves it, the 6-key derive otherwise — so /traces
	// answers the same "which cluster" question /services, /endpoints and
	// /databases already answer, not a second one.
	//
	// H15 GATE: a non-empty Cluster disqualifies the trace_summary MV
	// fast-path (the MV carries no cluster dimension — the same bounded
	// raw fallback Env uses, NO MV changes). An UNCONDITIONAL conjunct
	// would have disqualified /api/traces' cheapest read on EVERY request,
	// which is why tracesMVEligible tests the empty case explicitly.
	Cluster string
	Filters []FilterExpr // advanced filter chips (AND-joined)
	// FilterRoot is the optional grouped AND/OR builder (v0.8.x trace-query
	// gap-2). When non-nil it SUPERSEDES Filters: buildGetTracesWhere calls
	// ApplyFilterGroup instead of ApplyFilters. A flat-AND FilterRoot emits
	// byte-identical SQL to the legacy Filters path (pinned by
	// filtergroup_test.go), so existing callers that leave it nil are wholly
	// unaffected. An OR / nested group disqualifies the trace_summary MV
	// fast-path exactly like Search / custom-attr does (see GetTraces gate).
	FilterRoot *FilterGroup
	Sort       string // "time" | "duration"
	Order      string // "asc" | "desc"
	Limit      int
	Offset     int
	// RankedWithin, when non-nil, is an OUT param: the MV fast-path
	// writes traceRecencySliceN into it when a non-time sort was
	// ranked within the newest-N recency slice (v0.8.369,
	// Dynatrace-style). The HTTP layer forwards it so the UI can
	// show the "ranked within newest N" hint honestly — it stays 0
	// when the raw path (or an unsliced sort) served the request.
	RankedWithin *int
	// NarrowedFrom (v0.9.297) — OUT param. Set when Stage 2 ran out of
	// resources and the window had to be halved to answer at all. The
	// caller MUST surface it: a top-N over half the requested window is
	// a DIFFERENT answer, not a slower one, and presenting it as the
	// operator's question silently answered is the failure mode this
	// codebase keeps paying for.
	NarrowedFrom *time.Time
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

// addClusterConjunct — cluster daraltmasının TEK yazım yeri (v0.9.943, B3).
//
// İki çağıranı var (liste + toplu görünüm) ve ikisi de AYNI şekli
// üretmek ZORUNDA: /traces'te sekme değiştirmek soruyu genişletemez.
// İkinci bir kopya, bu depoda tekrar eden "bir kural iki yerde, zamanla
// ayrışıyor" sınıfının davetiyesi olurdu.
//
// H15: boş cluster HİÇBİR ŞEY eklemez. Bu satır, /api/traces'in
// trace_summary_5m hızlı yolunu koruyan şeyin ta kendisi
// (tracesMVEligible boş cluster'da uygunluğu sürdürür).
func addClusterConjunct(wc *whereClause, clusterExpr, cluster string) {
	if cluster == "" {
		return
	}
	wc.add(clusterExpr+" = ?", cluster)
}

// buildGetTracesWhere assembles the WHERE clause for GetTraces from
// a TraceFilter. Pure function (no Store / no ctx), extracted in
// v0.5.450 so the regression test for the v0.5.440 fix can assert
// on the generated SQL without standing up a ClickHouse instance.
//
// Order of conditions matches the historical inline build:
// time bounds → service narrowing → trace ID → error flag →
// duration → free-form FilterExpr[].
//
// clusterExpr (v0.9.943 — B3): the SQL expression that yields a span's
// cluster name. Passed IN rather than derived here because the choice
// (promoted column vs 6-key derive) belongs to the Store's boot probe
// (hasClusterCol) and this function must stay pure + ClickHouse-free so
// the SQL-shape tests can run without a server. Callers pass
// s.clusterExpr(); tests pass either constant.
func buildGetTracesWhere(f TraceFilter, clusterExpr string) whereClause {
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
		// Exact match ONLY — prefix search removed (v0.9.82, operator-reported).
		// trace_id carries a bloom_filter skip index (idx_trace) that prunes
		// granules for `=`/`IN` but CANNOT serve startsWith(): a prefix
		// predicate defeated the index and full-scanned the spans table — and
		// the trace-id lookup drops the time bound in the UI, so it ran
		// unbounded across the whole retention window. A full 32-hex id is a
		// bloom point-lookup; a partial id matches nothing (still fast).
		wc.add("trace_id = ?", f.TraceID)
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
	// v0.10.258 — operator-reported: "root diye aratınca geliyor ama error
	// seçince no traces". Errors = TRACE'te hata var; search/root/attr
	// filtreleri HAVING'de trace-düzeyi (countIf > 0) çalışırken bu WHERE
	// span-düzeyiydi: aynı span hem hata hem aranan operasyon olmak
	// zorundaydı (hata çocuk span'deyse → 0 satır; MV yolu ise
	// countMerge(error_count_state) > 0 ile trace-düzeyi bakıyordu —
	// iki yol ayrışmıştı). Başka span-düzeyi yüklem yoksa WHERE kalır
	// (idx_status set index'i granül budar); varsa hata HAVING'e taşınır
	// (hasErrorSpanLocal, GetTraces HAVING bloğu).
	if f.HasError && hasErrorSpanLocal(f) {
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
	// Cluster narrowing (v0.9.943 — B3/Ö5). Same always-AND placement as
	// Env, same reason. H15: the conjunct is CONDITIONAL — an empty
	// Cluster must emit NOTHING, otherwise every /api/traces request
	// would carry a derive comparison and (via tracesMVEligible) lose the
	// trace_summary fast path.
	addClusterConjunct(&wc, clusterExpr, f.Cluster)
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

// traceCountSettings builds the SETTINGS clause for a trace-count query.
//
// v0.9.284 — every count branch now spills at the same threshold as the
// LIST query it is counting. The "approx" branch used to carry only
// max_execution_time, so it hit code 241 EARLIER than the list whose
// results it was counting: the operator saw a memory error for a page
// that would have rendered. useHLL swaps count(DISTINCT) for the
// HyperLogLog implementation on wide windows (~1% margin); it is
// meaningless for the LIMIT-wrapped approx shape, which has no DISTINCT.
// stage2NeedsSafetySlice reports whether Stage 2 is about to run with
// NO trace-id bound — the one shape in this file that can reach
// gigabytes, and the only surviving explanation for the operator's
// 4.15 GiB (v0.9.299).
//
// Named and exported to the test package on purpose: the guard used to
// be an inline condition with a test that MIRRORED it, which proves the
// mirror, not the code. Everything that decides whether the expensive
// statement can be built now goes through here.
func stage2NeedsSafetySlice(holders string) bool {
	return holders == ""
}

// stage2IsBounded is the invariant that must hold when the statement is
// handed to ClickHouse: one of the two bounds is present. False here
// means an unbounded `GROUP BY trace_id` over the whole window.
func stage2IsBounded(holders string) bool {
	return holders != ""
}

// countModeAllowsMV reports whether the requested count mode leaves the
// trace_summary_5m fast path open. Only the modes needing NO window-wide
// aggregation qualify: "skip" and the empty default. "approx" and
// "exact" both wrap a `GROUP BY trace_id` over raw spans, so serving the
// LIST from the MV would save nothing — the expensive query still runs
// beside it.
//
// v0.9.284 — /explore pinned "approx" on every traces search purely to
// render a "of ~M" footer label, and that one field held this gate shut.
// The same unfiltered query /traces answered from the MV, /explore
// answered from raw spans. Named + tested so the gate cannot close
// again as a side effect of a UI label.
// tracesMVEligible — bu filtre MV hızlı yolunda karşılanabilir mi?
//
// v0.9.638'de GetTraces'in gövdesinden ÇIKARILDI, davranış değişmeden.
// Sebep: tavanlı sayım (trace_count.go) listenin SAYDIĞI kümeyle AYNI
// evreni saymak zorunda. Koşulu aynalayan ikinci bir kopya, bu oturumun
// on yedi sürümünü doğuran hata sınıfının ta kendisi olurdu — sayım
// planlayıcısı bu fonksiyonu AYNALAMAZ, ÇAĞIRIR.
//
// countModeAllowsMV BİLİNÇLİ olarak DIŞARIDA: o, filtrenin MV'de
// karşılanabilirliğiyle değil, çağıranın istediği sayım kipiyle ilgili.
// İçeri alsaydık sayım planlayıcısı kendi kipini kendine sorardı.
//
// SAF — tablo testli.
func tracesMVEligible(f TraceFilter) bool {
	return !f.MVGap && // v0.10.124 — MV'de boş gün: ham yol
		!f.From.IsZero() && !f.To.IsZero() &&
		f.To.Sub(f.From) >= 5*time.Minute &&
		f.Search == "" && f.TraceID == "" &&
		// v0.8.383 — an env filter disqualifies the MV fast-path:
		// trace_summary_5m has no env dimension (cluster precedent —
		// bounded raw fallback, NO MV changes).
		f.Env == "" &&
		// v0.9.943 (B3/H15) — cluster filtresi de MV'yi diskalifiye eder:
		// trace_summary_5m'de cluster boyutu YOK. KOŞULLU olması işin
		// KENDİSİ — boş cluster (isteklerin ezici çoğunluğu) hızlı yolda
		// KALIR; koşulsuz bir conjunct /api/traces'in en pahalı okumasını
		// HER istekte ham spans'e düşürürdü.
		f.Cluster == "" &&
		len(f.Filters) == 0 &&
		!f.FilterRoot.hasPredicate() &&
		len(f.RequireServices) == 0 &&
		len(f.TraceIDs) == 0
}

func countModeAllowsMV(mode string) bool {
	return mode == "skip" || mode == ""
}

func traceCountSettings(useHLL bool) string {
	s := "SETTINGS max_execution_time = 25, "
	if useHLL {
		s += "count_distinct_implementation = 'uniq', "
	}
	return s + tracesSpillSettings
}

// searchHaystack is the per-span text the free-text trace search scans:
// operation name + HTTP method + route + every span-attr value, space-joined
// into one string so the operator's words match whichever field carries them.
// arrayStringConcat on an empty attr_values returns ” (concat is NULL-safe).
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

// traceDisplaySvcExpr — /traces listesinde GÖSTERİLEN servis (v0.10.97,
// operatör-raporlu "iframe trace'leri"). Mutlak kök (parent'sız span)
// mobil web/iframe telemetrisinden gelince servisi 'unknown' oluyor;
// giriş-span ilkesi gereği görüntü EN ERKEN server/consumer span'in
// servisine düşer. Probe false iken (kolon henüz okunamıyor — cluster
// upgrade'inin ilk boot'u) ifade bit-bit ESKİ zincirdir.
//
// Kök-VARLIĞI filtreleri (RootOnly HAVING'i) bilinçli olarak bu zinciri
// KULLANMAZ: onlar "gerçek kök var mı" sorusu, bu "ne gösterelim" sorusu.
func (s *Store) traceDisplaySvcExpr() string {
	const root = "argMaxIfMerge(root_service_state)"
	if !s.hasTraceEntrySvcCol {
		return root
	}
	return "if((" + root + " = '' OR " + root + " = 'unknown') AND argMinIfMerge(entry_service_state) != '', argMinIfMerge(entry_service_state), " + root + ")"
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
	// FAZ 2 (docs/audit/traces-attribute-columns.md §6A) — fillTraceExtras is
	// now the COMMON phase-2 of BOTH paths (raw list no longer inlines the
	// projection) and is time-bounded by the page rows' real min/max
	// timestamps, so partition pruning + the trace_id bloom compose.
	// v0.10.124 — MV boşluğu (sihirbaz henüz doldurmadıysa) ham yola düşürür.
	f.MVGap = s.TraceMVGap(ctx, f.From, f.To)
	if tracesMVEligible(f) && countModeAllowsMV(f.CountMode) {
		out, total, hasMore, err := s.getTracesFromMV(ctx, f)
		if err == nil {
			if len(f.ExtraAttrs) > 0 {
				// FAZ 2 — phase-2 failure is now a hard error: the raw path
				// no longer inlines the projection, so falling through would
				// only re-run the SAME bounded phase-2 query and fail again.
				if ferr := s.fillTraceExtras(ctx, out, f.ExtraAttrs); ferr != nil {
					return nil, 0, false, fmt.Errorf("trace extras: %w", ferr)
				}
			}
			return out, total, hasMore, nil
		} else if mvFallbackEligible(err) {
			// Fall through to raw path — log it so a regression in the MV
			// pipeline doesn't silently leave us on the slow path forever.
			log.Printf("[chstore] trace_summary fast path failed, falling back to raw: %v", err)
		} else {
			// v0.9.276 — do NOT retry a resource failure on the raw path.
			//
			// This unconditional fallback is what turned a slow query into the
			// operator's HTTP 500. When the MV read aborts on time or memory,
			// the raw path is asked to do THE SAME WORK over billions of raw
			// spans — GROUP BY trace_id, where `spans` is ORDER BY
			// (service_name, time) so trace_id misaligns there too. That query
			// carries max_execution_time = 25 but the client ReadTimeout is 30s
			// and clickhouse-go arms it ONCE per read phase without refreshing
			// on progress packets, so on a wide window it cannot finish either.
			//
			// The operator therefore waited out the MV guard, then waited out
			// the client, and got a transport "i/o timeout" instead of the
			// ClickHouse error that actually explains the failure. Two slow
			// queries instead of one, and the real cause hidden.
			return nil, 0, false, fmt.Errorf("traces: %w", err)
		}
	}

	wc := buildGetTracesWhere(f, s.clusterExpr())
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
	// v0.10.238 — rootOnly'nin servisli dalı trace_summary_5m'i TÜM pencere ×
	// TÜM servisler için GROUP BY trace_id + string argMax durumuyla tarayıp
	// GLOBAL IN geçici tablosu kurar: ölçüldü (lokal, 1 gün, aynı yüklem)
	// 50.5 MiB / 2150 ms vs kök yüklemi olmadan 0.8 MiB / 269 ms (62×).
	// Prod 12 saat = milyonlarca trace → 241. Hafif 1. aşama bu HAVING'i
	// TAŞIMAZ; kök kontrolü adayların (≤ traceStage2MaxIDs id) üstünde
	// sınırlı bir MV sorgusuyla SONRADAN yapılır (filterRootTraces) — anlam
	// aynı (kök herhangi bir serviste olabilir, v0.10.107), maliyet id
	// sayısıyla sınırlı.
	rootPostFilter := f.RootOnly && (f.Service != "" || len(f.RequireServices) > 0)
	// v0.10.245 — HAVING'in ilk iki argümanı kök alt sorgusunun (from, to)
	// unix sn sınırı; zaman sıralı probe bunları BASAMAK penceresiyle
	// bağlar (probeHavingArgs) — 1 saatlik basamak için 30 günlük alt
	// sorgu 159 veriyordu.
	rootSubInHaving := rootPostFilter
	lightHavingParts := []string{}
	lightHavingArgs := []any{}
	if f.HasError && !hasErrorSpanLocal(f) {
		// v0.10.258 — trace-düzeyi hata (bkz. buildGetTracesWhere).
		havingParts = append(havingParts, traceHasErrorHaving)
		lightHavingParts = append(lightHavingParts, traceHasErrorHaving)
	}
	if f.RootOnly {
		// "Root traces only" — strict: a real root span must
		// be present AND complete. A complete root has:
		//   • parent_id empty (handles both wire formats)
		//   • a non-empty name
		//   • a non-empty service_name
		// This excludes Tempo-style "root not available" partial
		// traces where some orphan child span happens to have an
		// empty parent_id but isn't actually the trace's root.
		//
		// v0.10.107 — SERVİS-DARALTILMIŞ ham şekilde bu countIf YANLIŞTI:
		// WHERE service_name=? span'leri önce süzüyor, HAVING kökü o
		// daraltılmış kümede arıyor — kökü BAŞKA serviste olan her trace
		// sessizce düşüyordu (MV yolu "kök herhangi bir serviste" diye
		// bakar; tooltip'in vaadi de o). Daraltılmış şekilde kök-varlığı
		// artık DARALTILMAMIŞ kaynaktan sorulur: trace_summary_5m üyeliği
		// (bucket-budanmış, GLOBAL — 288 sınıfı değil). Daraltmasız ham
		// taramada eski countIf doğru ve ucuz: kök zaten görünür.
		if f.Service != "" || len(f.RequireServices) > 0 {
			havingParts = append(havingParts, `trace_id GLOBAL IN (
			SELECT trace_id FROM trace_summary_5m
			WHERE time_bucket >= toStartOfFiveMinute(toDateTime(?, 'UTC'))
			  AND time_bucket < toDateTime(?, 'UTC')
			GROUP BY trace_id
			HAVING argMaxIfMerge(root_service_state) != '')`)
			havingArgs = append(havingArgs, f.From.Unix(), f.To.Unix())
		} else {
			havingParts = append(havingParts,
				"countIf((parent_id = '' OR parent_id = '0000000000000000') AND name != '' AND service_name != '') > 0")
			lightHavingParts = append(lightHavingParts,
				"countIf((parent_id = '' OR parent_id = '0000000000000000') AND name != '' AND service_name != '') > 0")
		}
	}
	for _, svc := range f.RequireServices {
		if svc == "" {
			continue
		}
		havingParts = append(havingParts, "countIf(service_name = ?) > 0")
		havingArgs = append(havingArgs, svc)
		lightHavingParts = append(lightHavingParts, "countIf(service_name = ?) > 0")
		lightHavingArgs = append(lightHavingArgs, svc)
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
		lightHavingParts = append(lightHavingParts, "countIf("+pred+") > 0")
		lightHavingArgs = append(lightHavingArgs, pargs...)
	}
	havingSQL := ""
	if len(havingParts) > 0 {
		havingSQL = " HAVING " + strings.Join(havingParts, " AND ")
	}
	lightHavingSQL := ""
	if len(lightHavingParts) > 0 {
		lightHavingSQL = " HAVING " + strings.Join(lightHavingParts, " AND ")
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
			"SELECT count() FROM (SELECT trace_id FROM spans %s GROUP BY trace_id%s LIMIT %d) %s",
			wc.sql(), havingSQL, cap, traceCountSettings(false),
		)
		countArgs := append([]any{}, wc.args...)
		countArgs = append(countArgs, havingArgs...)
		if err := s.telemetryReadConn().QueryRow(ctx, approxSQL, countArgs...).Scan(&total); err != nil {
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
		settings := traceCountSettings(
			!f.From.IsZero() && !f.To.IsZero() && f.To.Sub(f.From) > 24*time.Hour)
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
		if err := s.telemetryReadConn().QueryRow(ctx, countSQL, countArgs...).Scan(&total); err != nil {
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

	// FAZ 2 (docs/audit/traces-attribute-columns.md §6A) — the raw list is
	// NARROW by construction now: attribute extras are NEVER inlined into
	// this query. The old inline projection decompressed the four fat
	// attr/res array columns for EVERY span in the window (before LIMIT) —
	// measured 6.97× read_bytes on the audit's local run. Extras ride the
	// common, bounded phase-2 (fillTraceExtras) after the page is known.
	// v0.10.126 — YENİLİK DİLİMİ (trace_raw_probe.go, perf bütçesi P2).
	// Zaman-DESC listede önce dar kuyruk penceresi taranır; K =
	// offset+limit+1 satırın hepsi floor'un üstünde başlamışsa sayfa tam
	// taramayla bire bir aynıdır, değilse basamak genişler, en sonda tam
	// pencere (eski davranış). Sayım sorguları yukarıda, tam pencere.
	//
	// Argument order matches placeholder order in the SQL:
	//   1. WHERE  predicates (time / service / DSL filters)
	//   2. HAVING predicates (RequireServices fan-in)
	//   3. LIMIT / OFFSET
	// light — v0.10.245: kök alt sorgusuz HAVING (kök kontrolü satırlar
	// üstünde sonradan, filterRootTracesAt). Probe rootPostFilter'da light.
	runList := func(from time.Time, limit, offset int, light bool) ([]TraceRow, error) {
		lf := f
		lf.From = from
		lwc := buildGetTracesWhere(lf, s.clusterExpr())
		hs, ha := havingSQL, probeHavingArgs(havingArgs, rootSubInHaving, from)
		if light {
			hs, ha = lightHavingSQL, lightHavingArgs
		}
		querySQL := buildGetTracesListSQL(lwc.sql(), hs, sortCol, order)
		args := append([]any{}, lwc.args...)
		args = append(args, ha...)
		args = append(args, limit, offset)
		rows, err := s.telemetryReadConn().Query(ctx, querySQL, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		return scanTraceListRows(rows)
	}
	var out []TraceRow
	var hasMore, served bool
	// v0.10.235 — Operator-reported (prod): attribute filtreli (http.route)
	// 12 saatlik arama, sort=duration ile CH 241 "memory limit exceeded
	// 3.73 GiB" (SourceFromNativeStream / deserializeBinaryBulk: shard'dan
	// gelen GROUP BY trace_id durumları başlatıcıda birleşirken). Attribute
	// filtresi MV yolunu düşürür; süre/span/durum sıralaması tüm pencereyi
	// görmek zorunda ve tek geçiş her trace için STRING durumlar (anyIf(name),
	// any(service_name)) taşıyordu. Ölçüldü (lokal, aynı yüklem, 3 koşu):
	// tek geçiş 4.03 MiB / 626 ms, hafif 1. aşama 1.06 MiB / 324 ms —
	// bellek 3.8× (oran prod'a taşınır). İki aşama: 1) yalnız trace_id +
	// SAYISAL sıralama anahtarı, LIMIT/OFFSET burada; 2) tam satır YALNIZ
	// sayfanın id'leri için (traceExtrasSQL ile aynı `trace_id IN` deseni).
	if traceRawProbeEligible(f) {
		for _, w := range traceRawProbeWindows(f.From, f.To) {
			floor := f.To.Add(-w)
			rows, err := runList(floor.Add(-traceRawProbeLookback), probeFetchLimit(f.Offset+pageLimit, rootPostFilter), 0, rootPostFilter)
			if err != nil {
				return nil, 0, false, err
			}
			if rootPostFilter && len(rows) > 0 {
				// v0.10.245 — kök kontrolü basamak satırları üstünde (iki-IN nokta
				// okuma); alt sorgu basamak penceresinde bile MV'nin tüm
				// servislerini tarıyordu (lokal 2 s: 7-9 s).
				cands := make([]stage1Cand, len(rows))
				for i, r := range rows {
					cands[i] = stage1Cand{id: r.TraceID, t0: r.StartTime, t1: r.StartTime}
				}
				hasRoot, err := s.filterRootTracesAt(ctx, cands, f.From, f.To)
				if err != nil {
					return nil, 0, false, err
				}
				kept := rows[:0]
				for _, r := range rows {
					if hasRoot[r.TraceID] {
						kept = append(kept, r)
					}
				}
				rows = kept
			}
			if page, more, ok := traceRawProbePage(rows, floor.UnixNano(), f.Offset, f.Limit); ok {
				out, hasMore, served = page, more, true
				break
			}
		}
	}
	if !served && rawListLightEligible(f, rootPostFilter) {
		// v0.10.237 — 1. aşama tüm pencereyi GÖRMEK zorunda (süre sıralaması
		// zamanla kesilemez); kaynak tükenmesinde pencere yarılanır,
		// NarrowedFrom ile UI'ya söylenir (daha dar pencerenin top-N'i FARKLI
		// bir cevaptır, daha yavaşı değil).
		// v0.10.245 — uzun aralık (trace_raw_light.go başlığı): akışkan 1.
		// aşama (GROUP BY'sız, bellek düz) + pencereyle ölçeklenen tavan;
		// kök kontrolü demet nokta okuma; 2. aşama trace-başı zaman aralığı.
		from := f.From
		var cands []stage1Cand
		streaming := traceRawStage1Streaming(f.Sort, lightHavingSQL)
		s1Limit, s1Offset := rawLightStage1Window(f.Offset, pageLimit, rootPostFilter)
		wantK := s1Limit
		if streaming && !rootPostFilter {
			// Span-düzeyi OFFSET anlamsız: offset+sayfa kadar trace çekilir,
			// Go'da tekilleştirilip dilimlenir.
			wantK = f.Offset + pageLimit
			s1Offset = 0
		}
		for attempt := 0; ; {
			lf := f
			lf.From = from
			lwc := buildGetTracesWhere(lf, s.clusterExpr())
			maxExec := traceRawStage1MaxExec(from, f.To)
			var stage1SQL string
			s1args := append([]any{}, lwc.args...)
			if streaming {
				stage1SQL = traceRawStage1StreamSQL(lwc.sql(), f.Sort, order, maxExec)
				s1args = append(s1args, traceRawStage1OverFetch(wantK))
			} else {
				stage1SQL, _ = traceRawStage1GroupSQL(lwc.sql(), lightHavingSQL, f.Sort, order, maxExec)
				s1args = append(s1args, lightHavingArgs...)
				s1args = append(s1args, s1Limit, s1Offset)
			}
			got, err := s.queryStage1Cands(ctx, stage1SQL, s1args, streaming)
			if err == nil {
				if streaming {
					got = dedupeStage1(got, wantK)
				}
				cands = got
				break
			}
			next, ok := narrowOnExhaustion(err, from, f.To, attempt)
			if !ok {
				return nil, 0, false, err
			}
			attempt++
			from = next
			if f.NarrowedFrom != nil {
				*f.NarrowedFrom = from
			}
			log.Printf("[traces] raw stage1 exhausted resources (%v) — halving the window to %s and retrying (attempt %d/%d)",
				err, from.Format(time.RFC3339), attempt, traceStage2NarrowMaxRetry)
		}
		if rootPostFilter && len(cands) > 0 {
			hasRoot, err := s.filterRootTracesAt(ctx, cands, f.From, f.To)
			if err != nil {
				return nil, 0, false, err
			}
			cands = applyRootFilterPageCands(cands, hasRoot, f.Offset, pageLimit)
		} else if streaming {
			if f.Offset >= len(cands) {
				cands = nil
			} else {
				cands = cands[f.Offset:]
			}
			if len(cands) > pageLimit {
				cands = cands[:pageLimit]
			}
		}
		if len(cands) > 0 {
			lf := f
			lf.From = from
			// v0.10.258 — id'ler 1. aşamada hata-doğrulandı; 2. aşama satırı
			// TÜM span'lerden kurar (span-düzeyi status WHERE'i olmadan) ki
			// süre/span sayısı yalnız hatalı span'lerin toplamı çıkmasın.
			lf.HasError = false
			lwc := buildGetTracesWhere(lf, s.clusterExpr())
			// v0.10.241 — kök kontrolü zaten id'ler üstünde yapıldı; 2. aşama
			// HAFİF HAVING (alt sorgusuz). v0.10.245 — id listesi PREWHERE'de
			// (trace_raw_light.go §4: 2794 → 326 ms).
			idArgs := make([]any, len(cands))
			for i, c := range cands {
				idArgs[i] = c.id
			}
			querySQL := buildGetTracesListSQLWith(stage2PrewhereSQL(len(cands))+lwc.sql(), lightHavingSQL, sortCol, order, stage2Settings)
			args := append([]any{}, idArgs...)
			args = append(args, lwc.args...)
			args = append(args, lightHavingArgs...)
			args = append(args, len(cands), 0)
			rows, err := s.telemetryReadConn().Query(ctx, querySQL, args...)
			if err != nil {
				return nil, 0, false, err
			}
			page, err := scanTraceListRows(rows)
			rows.Close()
			if err != nil {
				return nil, 0, false, err
			}
			out = page
		}
		hasMore = len(cands) > f.Limit
		if len(out) > f.Limit {
			out = out[:f.Limit]
		}
		served = true
	}
	if !served {
		rows, err := runList(f.From, pageLimit, f.Offset, false)
		if err != nil {
			return nil, 0, false, err
		}
		out = rows
		hasMore = len(out) > f.Limit
		if hasMore {
			out = out[:f.Limit]
		}
	}
	// Common phase-2: extras for the trimmed page only (≤ Limit ids), bounded
	// by the page rows' real min/max timestamps. Also serves the CSV export
	// path — there the id list is up to 50k rows, but the derived bounds stay
	// within the export's list window, so the phase-2 scan is window-bounded
	// (audit §8) while pruning as tightly as the rows allow.
	if len(f.ExtraAttrs) > 0 {
		if err := s.fillTraceExtras(ctx, out, f.ExtraAttrs); err != nil {
			return nil, 0, false, fmt.Errorf("trace extras: %w", err)
		}
	}
	return out, total, hasMore, nil
}

// buildGetTracesListSQL assembles the raw-path phase-1 (narrow) list query.
// Pure — pinned by traces_extras_test.go so the FAZ 2 invariant ("the raw
// list never touches attr_values/res_values") cannot silently regress.
//
// Note: use if() not ternary ? : — ClickHouse treats ? as a param placeholder
// v0.5.351 — root_name/root_svc fallback. When the WHERE
// filter (service / name search) excludes the real root
// span (because it lives in a different service or has a
// different name), anyIf(parent_id=”) returns empty and
// the trace row renders blank. Fall back to ANY span's
// name/service so the operator at least sees a label — the
// trace detail view still shows the full trace on click.
func buildGetTracesListSQL(whereSQL, havingSQL, sortCol, order string) string {
	return buildGetTracesListSQLWith(whereSQL, havingSQL, sortCol, order, "")
}

// buildGetTracesListSQLWith — extraSettings: SETTINGS listesine eklenen
// ek ayar(lar) (v0.10.245 2. aşama: optimize_move_to_prewhere = 0).
// whereSQL "PREWHERE … WHERE …" ile başlayabilir.
func buildGetTracesListSQLWith(whereSQL, havingSQL, sortCol, order, extraSettings string) string {
	if extraSettings != "" {
		extraSettings = extraSettings + ",\n\t\t  "
	}
	return `
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
		       max(if(status_code = 'error', 1, 0))    AS has_error,
		       countIf(status_code = 'error')          AS err_spans
		FROM spans ` + whereSQL + `
		GROUP BY trace_id` + havingSQL + `
		ORDER BY ` + sortCol + ` ` + order + `
		LIMIT ? OFFSET ?
		SETTINGS
		  ` + extraSettings + `max_execution_time = 25,
		  optimize_read_in_order = 1,
		  optimize_aggregation_in_order = 1,
		  distributed_product_mode = 'global',
		  ` + tracesSpillSettings
}

// rawLightStage1Window — 1. aşamanın LIMIT/OFFSET'i. Kök son-filtresi
// yoksa sayfanın kendisi (limit+1, offset). Varsa aday kümesi: sayfayı
// sonradan süzeceğimiz için 0'dan başlayıp offset + 4×(limit+1) id (en az
// 200, en çok traceStage2MaxIDs) alınır; kısmi trace'ler seyrek olduğundan
// sayfa dolar, dolmazsa hasMore dürüstçe kalır. SAF; testli.
func rawLightStage1Window(offset, pageLimit int, rootPostFilter bool) (limit, off int) {
	if !rootPostFilter {
		return pageLimit, offset
	}
	k := offset + 4*pageLimit
	if k < 200 {
		k = 200
	}
	if k > traceStage2MaxIDs {
		k = traceStage2MaxIDs
	}
	return k, 0
}

// applyRootFilterPage — aday id'lerden kökü olanları (1. aşama sırasıyla)
// süzer ve offset'ten itibaren limit+1 tanesini döner (fazlası hasMore). SAF.
func applyRootFilterPage(ids []string, hasRoot map[string]bool, offset, pageLimit int) []string {
	kept := make([]string, 0, len(ids))
	for _, id := range ids {
		if hasRoot[id] {
			kept = append(kept, id)
		}
	}
	if offset >= len(kept) {
		return nil
	}
	kept = kept[offset:]
	if len(kept) > pageLimit {
		kept = kept[:pageLimit]
	}
	return kept
}

// filterRootTraces — aday id'ler için kök-varlığı (MV yolunun aynı katı
// kuralı: argMaxIfMerge(root_service_state) != ”), id listesiyle SINIRLI:
// maliyet pencereden değil id sayısından gelir (≤ traceStage2MaxIDs).
func (s *Store) filterRootTraces(ctx context.Context, ids []string, from, to time.Time) (map[string]bool, error) {
	out := make(map[string]bool, len(ids))
	for start := 0; start < len(ids); start += traceStage2MaxIDs {
		end := start + traceStage2MaxIDs
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[start:end]
		args := []any{from.Unix(), to.Unix()}
		for _, id := range chunk {
			args = append(args, id)
		}
		rows, err := s.telemetryReadConn().Query(ctx, `
			SELECT trace_id FROM trace_summary_5m
			WHERE time_bucket >= toStartOfFiveMinute(toDateTime(?, 'UTC'))
			  AND time_bucket < toDateTime(?, 'UTC')
			  AND trace_id IN (`+chPlaceholders(len(chunk))+`)
			GROUP BY trace_id
			HAVING argMaxIfMerge(root_service_state) != ''
			SETTINGS max_execution_time = 10`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			out[id] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// queryTraceIDs — tek kolonlu (trace_id) sorguyu dilime okur.
// queryStage1Cands — 1. aşama adayları: akışkan (id, t) ya da GROUP BY
// (id, t0, t1) satırları.
func (s *Store) queryStage1Cands(ctx context.Context, sql string, args []any, streaming bool) ([]stage1Cand, error) {
	rows, err := s.telemetryReadConn().Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []stage1Cand
	for rows.Next() {
		var c stage1Cand
		if streaming {
			if err := rows.Scan(&c.id, &c.t0); err != nil {
				return nil, err
			}
			c.t1 = c.t0
		} else if err := rows.Scan(&c.id, &c.t0, &c.t1); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// filterRootTracesAt — v0.10.245: aday sayısı rootCheckTupleMaxIDs'i
// aşmıyorsa (time_bucket, trace_id) demet nokta okuması (MV PK), aşıyorsa
// eski pencereli tarama (filterRootTraces).
func (s *Store) filterRootTracesAt(ctx context.Context, cands []stage1Cand, from, to time.Time) (map[string]bool, error) {
	if len(cands) > rootCheckTupleMaxIDs {
		ids := make([]string, len(cands))
		for i, c := range cands {
			ids[i] = c.id
		}
		return s.filterRootTraces(ctx, ids, from, to)
	}
	out := make(map[string]bool, len(cands))
	const chunkSize = 500
	for start := 0; start < len(cands); start += chunkSize {
		end := start + chunkSize
		if end > len(cands) {
			end = len(cands)
		}
		chunk := cands[start:end]
		buckets, ids := rootCheckArgs(chunk)
		args := append(buckets, ids...)
		rows, err := s.telemetryReadConn().Query(ctx, rootCheckSQL(len(buckets), len(ids)), args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			out[id] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// applyRootFilterPageCands — applyRootFilterPage'in aday karşılığı.
func applyRootFilterPageCands(cands []stage1Cand, hasRoot map[string]bool, offset, pageLimit int) []stage1Cand {
	kept := make([]stage1Cand, 0, len(cands))
	for _, c := range cands {
		if hasRoot[c.id] {
			kept = append(kept, c)
		}
	}
	if offset >= len(kept) {
		return nil
	}
	kept = kept[offset:]
	if len(kept) > pageLimit {
		kept = kept[:pageLimit]
	}
	return kept
}

// narrowOnExhaustion — kaynak tükenmesinde (241/159/…) pencereyi yarılar:
// attempt < traceStage2NarrowMaxRetry ve kalan pencere > traceStage2NarrowFloor
// ise yeni from (= to − span/2) ve ok=true; aksi hâlde ok=false (hata
// olduğu gibi yüzeye). MV yolunun (trace_slice.go) sabitleriyle aynı —
// iki yol farklı eşiklerle daralmasın. SAF; trace_raw_light_test.go pinler.
func narrowOnExhaustion(err error, from, to time.Time, attempt int) (time.Time, bool) {
	if err == nil || !isResourceExhaustion(err) || attempt >= traceStage2NarrowMaxRetry {
		return from, false
	}
	span := to.Sub(from)
	if span <= traceStage2NarrowFloor {
		return from, false
	}
	return to.Add(-span / 2), true
}

// rawListLightEligible — ham liste yolunda hafif 1. aşamanın uygulandığı
// sıralamalar: sıralama anahtarı SAYISAL olanlar (süre/span/durum). Zaman
// sıralaması pencere basamaklarıyla (trace_raw_probe.go) zaten daralıyor;
// servis/operasyon sıralamasının anahtarı string durumun kendisi
// (hafifletilecek bir şey yok); id listesi zaten sınırlı. SAF.
func rawListLightEligible(f TraceFilter, rootPostFilter bool) bool {
	if f.TraceID != "" || len(f.TraceIDs) > 0 {
		return false
	}
	switch f.Sort {
	case "duration", "spans", "status":
		return true
	case "", "time":
		// v0.10.239 — zaman sıralaması ancak kök son-filtresi gerekiyorsa
		// hafif yola girer (pencere basamağı ÖNCE denenir; buraya tam
		// pencereye düşüşte gelinir) — sınırsız GLOBAL IN HAVING'i
		// taşıyan tek geçişe düşmemek için.
		return rootPostFilter
	}
	return false
}

// getTracesFromMV implements the two-stage fast path:
//
//	Stage 1: scan trace_service_index_5m, find the top
//	(Limit+1) trace_ids that match the service in the
//	window, ordered by last activity. Uses the
//	(service_name, time_bucket) primary key prefix so the
//	scan is partition-pruned + sorted-access.
//
//	Stage 2: pull those trace_ids' full summaries from
//	trace_summary_5m (state merge). Apply RootOnly /
//	HasError / MinMs / MaxMs as HAVING filters here so the
//	page sort sees only matching traces. Final ORDER BY +
//	LIMIT runs on the page-bounded result.
//
// Sort: index supports time-desc; for duration / span_count
// / has_error we still produce the right page because stage
// 1 over-selects (10× LIMIT) and stage 2 re-sorts. This
// trades a small amount of recall for a 1000× speedup at
// scale; if an operator needs an exact ordering across the
// full window they can drop the service filter or narrow
// the time range.
// traceAttrMaterialized maps attribute keys to promoted NATIVE columns on
// the spans table (FAZ 2C prep, docs/audit/traces-attribute-columns.md).
// Ships EMPTY: entries appear only once the corresponding
// `ALTER TABLE spans ADD COLUMN … MATERIALIZED attr_values[indexOf(…)]`
// migration has actually been applied (and, on distributed setups, the
// hasXCol boot probe confirmed the column — the cluster-column precedent,
// see clusterExpr / distributed-column-safety).
//
// Deliberately a SEPARATE layer above WellKnownTraceCol rather than merged
// into it: WellKnown maps OTel SEMCONV keys to typed columns that exist in
// the schema since day one (always safe to reference), while this map holds
// operator-promoted CUSTOM keys whose columns exist only after an optional,
// per-install migration. Merging the two would let a semconv rename or an
// unapplied migration silently poison the other class; the projection
// generator consults this map first, then delegates to WellKnown, then
// falls back to the array lookup — one code path, three cost tiers.
// v0.9.625 — harita artık BOOT-SONRASI da güncellenebiliyor, bu yüzden
// çıplak map değil atomic pointer.
//
// Sebep: küme kipinde boot DDL'i ERTELİYOR (ddl_defer.go, v0.9.614).
// Terfi kolonu onarımı (v0.9.621) o kuyruğa giriyor, probe ise boot
// sırasında SENKRON koşuyor — yani probe henüz onarılmamış kolonu
// görüyor, kaydetmiyor ve düzeltme İKİNCİ BİR RESTART'a kadar devreye
// girmiyor. Ertelenen DDL bitince yeniden probe edebilmek için haritanın
// güvenle değiştirilebilir olması gerekiyor.
//
// Çıplak map'e boot sonrası yazmak Go'da "concurrent map read and map
// write" fatal'i demekti — süreç çöker. Kopyala-ve-değiştir + atomic
// pointer: okuyucular kilitsiz kalıyor, yazar yeni bir map yayınlıyor.
var promotedColsPtr atomic.Pointer[map[string]string]

// promotedCols — terfi kolonu haritasının o anki hâli. Dönen map SALT
// OKUNUR: çağıran ASLA yazmamalı, yayınlanmış bir kopyadır.
func promotedCols() map[string]string {
	if m := promotedColsPtr.Load(); m != nil {
		return *m
	}
	return nil
}

// registerTraceAttrMaterialized publishes the promoted-column map. Boot
// (migrate, after the probe confirms the columns actually CARRY the data —
// v0.9.621) and, in cluster mode, again once the deferred DDL lands
// (v0.9.625). Copy-on-write: readers never see a partially built map.
func registerTraceAttrMaterialized(cols map[string]string) {
	next := map[string]string{}
	for k, v := range promotedCols() {
		next[k] = v
	}
	for k, col := range cols {
		next[k] = col
	}
	promotedColsPtr.Store(&next)
}

// traceExtrasProjection builds the extras SELECT fragment for the requested
// attribute keys plus its bind args. Resolution order per key:
//  1. traceAttrMaterialized — promoted native column (cheapest; anyIf skips
//     empty values so the "first non-empty across the trace's spans"
//     semantics match the array path exactly),
//  2. WellKnownTraceCol — semconv key with a dedicated structured column,
//  3. attr_values[indexOf(attr_keys, ?)] with res_values fallback — the
//     generic (4-fat-array-column) path.
//
// Keys flow as `?` parameters; HTTP-layer sanitisation (isSafeAttrKey)
// already rejected anything outside [a-zA-Z0-9._-]. Pure — table-tested.
// clusterAliasKeys — spans.cluster kolonunun coalesce zincirindeki
// anahtarlar (clusterDeriveExpr / metricClusterExpr ile aynı altı
// permütasyonun anahtar seti) + FilterBuilder'ın `resource.` önekli
// yazımları. traces_extras_test.go clusterDeriveExpr'e pinler.
var clusterAliasKeys = map[string]bool{
	"cluster": true, "k8s.cluster.name": true, "openshift.cluster.name": true,
	"resource.cluster": true, "resource.k8s.cluster.name": true, "resource.openshift.cluster.name": true,
}

func traceExtrasProjection(attrs []string) (string, []any) {
	sel := ""
	args := []any{}
	for i, key := range attrs {
		// v0.10.143 — `cluster` kolon anahtarı spans'in terfi `cluster`
		// kolonuna (k8s.cluster.name / openshift.cluster.name / cluster
		// coalesce zinciri, v0.8.185) düşer; çıplak dizi araması yalnız
		// literal `cluster` attribute'unu bulur (çoğu kurulumda boş — inceleme).
		// v0.10.233 (docs/audit/traces-attribute-columns.md D1) — kolon adı
		// yalnız literal `cluster` değil, kolonun coalesce ZİNCİRİNDEKİ her
		// anahtar: varsayılan kolon `openshift.cluster.name` dizi yoluna
		// düşüyordu (84 MiB / 212 ms vs 5.25 MiB / 66 ms, 50 trace, lokal
		// query_log medyanı). Aynı kolon, aynı değer: clusterDeriveExpr'in
		// kendisi bu anahtarları coalesce ediyor (k8s.cluster.name önce).
		if clusterAliasKeys[key] {
			sel += fmt.Sprintf(", anyIf(cluster, cluster != '') AS extra_%d", i)
			continue
		}
		if col, ok := promotedCols()[key]; ok {
			sel += fmt.Sprintf(", anyIf(%s, %s != '') AS extra_%d", col, col, i)
			continue
		}
		if col, ok := WellKnownTraceCol[key]; ok {
			sel += fmt.Sprintf(", any(%s) AS extra_%d", col, i)
			continue
		}
		sel += fmt.Sprintf(
			", anyIf(coalesce("+
				"nullIf(attr_values[indexOf(attr_keys, ?)], ''),"+
				"nullIf(res_values[indexOf(res_keys, ?)], '')"+
				"), has(attr_keys, ?) OR has(res_keys, ?)) AS extra_%d", i)
		args = append(args, key, key, key, key)
	}
	return sel, args
}

// traceExtrasSQL assembles the phase-2 extras query for nIDs trace ids.
// FAZ 2 hard bounds (the pre-fix query violated the CLAUDE.md spans rule):
//   - time-bounded WHERE — placed BEFORE the id IN-list so partition
//     pruning and the idx_trace bloom compose instead of the bloom
//     filtering ALL retention's granules,
//   - LIMIT nIDs — GROUP BY trace_id over an id IN-list can't exceed nIDs
//     groups anyway; the LIMIT is the safety belt the rule demands,
//   - max_execution_time (kept from the original).
//
// Placeholder order: projection args (SELECT) → from, to → ids.
// Pure — table-tested.
func traceExtrasSQL(nIDs int, attrs []string) (string, []any) {
	sel, projArgs := traceExtrasProjection(attrs)
	holders := strings.TrimSuffix(strings.Repeat("?,", nIDs), ",")
	sql := "SELECT trace_id" + sel +
		" FROM spans WHERE time >= ? AND time <= ? AND trace_id IN (" + holders + ")" +
		" GROUP BY trace_id" +
		fmt.Sprintf(" LIMIT %d", nIDs) +
		" SETTINGS max_execution_time = 15"
	return sql, projArgs
}

// traceExtrasToSlack pads the phase-2 upper time bound: a trace's LAST span
// can start after the recorded trace end (late/async spans, clock skew, MV
// bucket staleness on the fast path), so the exact max(start+dur) bound
// could drop it from the anyIf scan. 5 minutes matches the MV bucket width.
const traceExtrasToSlack = 5 * time.Minute

// traceExtrasWindow applies the phase-2 slack to a raw [from, to] bound.
// The lower bound stays exact — no span can start before its trace's
// min(time) by definition. Pure — table-tested.
func traceExtrasWindow(from, to time.Time) (time.Time, time.Time) {
	return from, to.Add(traceExtrasToSlack)
}

// traceExtrasBounds derives the phase-2 raw time window from phase-1 rows:
// min(trace_start) .. max(trace_start + duration). Callers pass the result
// through TraceExtras, which applies traceExtrasWindow. Pure — table-tested.
func traceExtrasBounds(rows []TraceRow) (time.Time, time.Time) {
	var minStart, maxEnd int64
	for i, r := range rows {
		end := r.StartTime + int64(r.DurationMs*1e6)
		if i == 0 || r.StartTime < minStart {
			minStart = r.StartTime
		}
		if i == 0 || end > maxEnd {
			maxEnd = end
		}
	}
	return time.Unix(0, minStart).UTC(), time.Unix(0, maxEnd).UTC()
}

// traceExtrasChunkIDs caps the id IN-list of ONE phase-2 query. Same math
// as traceStage2MaxIDs (v0.8.363, found via self-telemetry): clickhouse-go
// interpolates bind args client-side at ~35 bytes per id and the server
// parses at most max_query_size (256 KiB). 5000 ids ≈ 175 KiB leaves room
// for the projection fragment + time bounds. Only the CSV-export path (up
// to 50k rows) ever exceeds one chunk — the UI page is ≤50 ids.
const traceExtrasChunkIDs = 5000

// TraceExtras fetches the requested attribute keys for an EXPLICIT trace-id
// set within [from, to] (+slack) and returns them keyed by trace id. Every
// requested key is present in each returned trace's map (” when absent) so
// callers can distinguish "fetched, empty" from "not fetched". Id sets past
// traceExtrasChunkIDs run as sequential chunked queries (export path).
//
// This is the single phase-2 implementation (FAZ 2): GetTraces' MV and raw
// paths reach it through fillTraceExtras (bounds derived from the page
// rows), and the /api/traces?traceIds= enrichment path calls it directly
// (bounds supplied by the client from the visible rows).
func (s *Store) TraceExtras(ctx context.Context, ids, attrs []string, from, to time.Time) (map[string]map[string]string, error) {
	out := make(map[string]map[string]string, len(ids))
	if len(ids) == 0 || len(attrs) == 0 {
		return out, nil
	}
	from, to = traceExtrasWindow(from, to)
	for start := 0; start < len(ids); start += traceExtrasChunkIDs {
		end := start + traceExtrasChunkIDs
		if end > len(ids) {
			end = len(ids)
		}
		if err := s.traceExtrasChunk(ctx, ids[start:end], attrs, from, to, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// traceExtrasChunk runs one bounded phase-2 query for ≤ traceExtrasChunkIDs
// ids and merges the rows into out.
func (s *Store) traceExtrasChunk(ctx context.Context, ids, attrs []string, from, to time.Time, out map[string]map[string]string) error {
	sql, projArgs := traceExtrasSQL(len(ids), attrs)
	args := append([]any{}, projArgs...)
	args = append(args, from, to)
	for _, id := range ids {
		args = append(args, id)
	}
	qrows, err := s.telemetryReadConn().Query(ctx, sql, args...)
	if err != nil {
		return err
	}
	defer qrows.Close()
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
		m := make(map[string]string, len(attrs))
		for i, k := range attrs {
			m[k] = extras[i]
		}
		out[id] = m
	}
	return qrows.Err()
}

// fillTraceExtras is the common phase-2 of BOTH GetTraces paths (FAZ 2):
// fetches the user-selected attribute columns for a page of trace rows and
// writes them into each row's Extras map. Bounds come from the page rows'
// REAL min/max timestamps (traceExtrasBounds), so even a 30-day list window
// scans only the partitions the page actually touches; the id IN-list rides
// the idx_trace bloom skip index within them. (v0.8.77, rebounded FAZ 2.)
func (s *Store) fillTraceExtras(ctx context.Context, rows []TraceRow, attrs []string) error {
	if len(rows) == 0 || len(attrs) == 0 {
		return nil
	}
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.TraceID
	}
	from, to := traceExtrasBounds(rows)
	byID, err := s.TraceExtras(ctx, ids, attrs, from, to)
	if err != nil {
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
		for _, key := range attrs {
			rows[i].Extras[key] = ex[key]
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
		WHERE time_bucket >= ? AND time_bucket < ?
		GROUP BY trace_id` + havingSQL + `
		ORDER BY ` + sortExpr + ` ` + order + `
		LIMIT ?
		SETTINGS max_execution_time = 15,
		         optimize_read_in_order = 1,
		         optimize_aggregation_in_order = 1`, true
}

// v0.9.633 — in-order ayarlarının ÖLÇÜLMÜŞ etkisi (denetim bulgusu:
// "ölü ayar"). Kaldırılmadılar — bir no-op'u silmenin operatöre faydası
// YOK, riski sıfır değil (aşağıdaki 1. satır bunun kanıtı: az kalsın
// ÇALIŞAN bir ayar silinecekti). Bilgi burada dursun ki bir dahaki
// okuyan sorgunun sıraya göre optimize edildiğini sanmasın.
//
// Yöntem: EXPLAIN PIPELINE'daki AggregatingInOrderTransform sayısı.
// ⚠ DÜZ `EXPLAIN` AYIRT ETMİYOR — ilk denememde açık/kapalı aynı planı
// verdi ve "hepsi ölü" diye yanlış sonuca varıyordum. Kontrol vakası
// (gerçek önek) olmadan bu ölçüm yapılmamalı.
//
//	SORGU ŞEKLİ                                      AÇIK  KAPALI
//	operation_summary_5m (service_name, name, tb)
//	  WHERE service_name=? GROUP BY name                 1       0  ← ETKİLİ
//	trace_summary_5m (time_bucket, trace_id)
//	  GROUP BY trace_id                                  0       0  ← ölü
//	trace_service_index_5m (service_name, tb, trace_id)
//	  WHERE service_name=? GROUP BY trace_id             0       0  ← ölü
//
// Ölü olanların sebebi aynı: GROUP BY anahtarı sıralama anahtarının
// ÖNEKİ değil (trace_id ikinci/üçüncü bileşen, aradaki time_bucket ise
// ARALIK filtresi), ve ORDER BY agregat ifade üstünde.
//
// ÖLÇÜLMEYEN iki nokta var — `spans` üzerinde GROUP BY trace_id ve
// alt-sorgu üstünde GROUP BY group_key: ikisi de yapısal olarak ölü
// GÖRÜNÜYOR ama ölçülmedi, dolayısıyla iddia edilmiyor.

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

// traceHasErrorHaving — trace-düzeyi "hata var" yüklemi (GROUP BY trace_id
// üstünde). Liste sorgusunun has_error kolonuyla aynı ifade.
const traceHasErrorHaving = "max(if(status_code = 'error', 1, 0)) = 1"

// hasErrorSpanLocal — v0.10.258: HasError'ın span-düzeyi WHERE olarak
// kalabileceği durum: trace'in ÖTEKİ span'lerinin sağlayabileceği başka bir
// yüklem yok (search / attr filtreleri / kök / RequireServices). Aksi hâlde
// "aynı span hem hata hem aranan" tuzağı → HAVING. SAF; tablo-testli.
func hasErrorSpanLocal(f TraceFilter) bool {
	if !f.HasError {
		return false
	}
	if f.Search != "" || len(f.Filters) > 0 || len(f.RequireServices) > 0 || f.RootOnly {
		return false
	}
	if f.FilterRoot != nil && f.FilterRoot.hasPredicate() {
		return false
	}
	return true
}

// aggHasErrorSpanLocal — AggregateFilter için aynı kural (search / attr
// filtreleri yoksa WHERE, varsa iç HAVING).
func aggHasErrorSpanLocal(f AggregateFilter) bool {
	if !f.HasError {
		return false
	}
	if f.Search != "" || len(f.Filters) > 0 {
		return false
	}
	if f.FilterRoot != nil && f.FilterRoot.hasPredicate() {
		return false
	}
	return true
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
	var stage2Floor, stage2Ceil time.Time
	// v0.10.265 — Operator-reported (prod 7g + servis + hasError, zaman sıralı):
	// her "Next" 40+ s ve "shortened" rozeti. İki eski servis şekli de
	// pencereyle lineerdi (trace_slice.go traceServiceSliceScanSQL başlığı).
	// Şimdi tek yol: servis indeksinden akışkan dilim (PK sırası, GROUP BY
	// yok) → id listesi + kesim → stage 2 zaten sınırlı (holders) ve desc'te
	// tabanı kesime çekilir. Plan saf (serviceSlicePlan), tablo-testli.
	if f.Service != "" {
		want, sliceOrder, sliced := serviceSlicePlan(f, pageLimit, stage1Limit)
		s1f := f
		s1f.Order = sliceOrder
		ids, cut, exhausted, err := s.traceServiceSlice(ctx, s1f, want)
		if err != nil {
			return nil, 0, false, fmt.Errorf("stage1: %w", err)
		}
		if len(ids) == 0 {
			return []TraceRow{}, 0, false, nil
		}
		traceIDs = ids
		holders = strings.Repeat("?,", len(ids))
		holders = holders[:len(holders)-1]
		stage2Floor, stage2Ceil = sliceStageBounds(sliceOrder, cut, exhausted)
		if sliced && f.RankedWithin != nil {
			*f.RankedWithin = len(ids)
			if exhausted {
				*f.RankedWithin = 0
			}
		}
	}

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
	// Zero means "no narrowing" — Stage 2 then uses f.From, i.e. today's
	// behaviour. Only the aggregation-free slice sets it, because only it
	// knows where the slice was actually cut.
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
		// v0.9.277 — the aggregation-free slice, for the shape the operator
		// actually hits: no HAVING, i.e. no rootOnly / minMs / maxMs. That is
		// every default page load and every wide-window browse, and it is the
		// case that was returning HTTP 500. hasError rides it too via a
		// row-level prefilter; the remaining aggregate filters keep the old
		// Stage 1 unchanged, because their predicates genuinely need the
		// merged state and there is nothing to gain by rewriting them here.
		errorsOnly := len(having) == 1 && having[0] == "countMerge(error_count_state) > 0"
		if budgetOK && (len(having) == 0 || errorsOnly) {
			ids, cut, exhausted, serr := s.traceRecencySlice(ctx, s1f, budget, errorsOnly)
			if serr != nil {
				return nil, 0, false, serr
			}
			if len(ids) == 0 {
				return []TraceRow{}, 0, false, nil
			}
			traceIDs = ids
			holders = strings.Repeat("?,", len(ids))
			holders = holders[:len(holders)-1]
			// v0.10.267 — asc'te kesim TAVAN'dır (id'ler pencerenin başında);
			// eskiden taban sayılıyor, kırpma tespiti ×8 genişleterek kurtarıyordu.
			stage2Floor, stage2Ceil = sliceStageBounds(s1f.Order, cut, exhausted)
			if ranked && f.RankedWithin != nil {
				// Report the REAL slice, not the constant. When the slice
				// exhausted the window the ordering is global, so the "ranked
				// within newest N" hint must not appear at all rather than
				// appear with a number that isn't true.
				*f.RankedWithin = len(ids)
				if exhausted {
					*f.RankedWithin = 0
				}
			}
		} else if s1, ok := traceStage1LightSQL(s1f, having); ok && budgetOK {
			rows1, err := s.telemetryReadConn().Query(ctx, s1, f.From, f.To, budget)
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
	// v0.9.299 — Stage 2 may NEVER run without a trace-id bound.
	//
	// This is the one shape that can reach gigabytes, and measurement is
	// what narrowed it down. On the live cluster the bounded statement
	// (5,000-id IN list) costs 25,197,112 bytes at 1 day, 2 days and 4
	// days — byte-for-byte identical — and 25,197,064 at 7 days: 48
	// bytes of drift across a 5.4x row range, and no movement at all
	// from max_threads 1 to 32. There is no per-row component to
	// extrapolate, so no window makes THAT query big.
	//
	// Unbounded is the opposite: `GROUP BY trace_id` with only a time
	// range builds one set of six merge-states per distinct trace in the
	// window. At production density a single 5-minute bucket holds on
	// the order of a million traces, so a 7-day window is 10^8-10^9
	// groups — which is where an operator's 4.15 GiB against a 3.73 GiB
	// cap comes from.
	//
	// Every no-service path is supposed to set `holders` (recency slice
	// or light Stage 1). If one ever doesn't — a new sort key, an early
	// return, a refactor — the fallback today is silently the most
	// expensive query in the codebase, aimed at the whole window. Refuse
	// it instead: a clear error names the bug in seconds, where an OOM
	// costs a node and tells you nothing.
	// So rather than refuse (which would trade one 500 for another), the
	// bound is BUILT here as a last resort. The recency slice is
	// aggregation-free and reads the sorting-key prefix, so it is cheap
	// at any window — it is exactly what the normal path uses. After
	// this, the unbounded shape is unreachable by construction.
	if stage2NeedsSafetySlice(holders) {
		log.Printf("[traces] stage2 had NO trace-id bound (sort=%q, window=%s) — "+
			"Stage 1 left it unset; building the recency slice here rather than "+
			"aggregating the whole window", f.Sort, f.To.Sub(f.From))
		s1f := f
		s1f.Sort, s1f.Order = "time", "desc"
		ids, cut, exhausted, serr := s.traceRecencySlice(ctx, s1f, traceRecencySliceN, false)
		if serr != nil {
			return nil, 0, false, fmt.Errorf("stage2 safety slice: %w", serr)
		}
		if len(ids) == 0 {
			return []TraceRow{}, 0, false, nil
		}
		traceIDs = ids
		holders = strings.Repeat("?,", len(ids))
		holders = holders[:len(holders)-1]
		if !exhausted {
			stage2Floor = cut
		}
		if f.RankedWithin != nil && traceSortRecencySliced(f.Sort) {
			*f.RankedWithin = len(ids)
			if exhausted {
				*f.RankedWithin = 0
			}
		}
	}

	traceIDClause := ""
	var idArgs []any
	// v0.10.265 — `trace_id GLOBAL IN (index GROUP BY trace_id)` dalı KALDIRILDI:
	// servis yolu artık her zaman holders taşır (yukarıdaki dilim).
	if holders != "" {
		traceIDClause = "trace_id IN (" + holders + ") AND "
		idArgs = traceIDs
	}

	stage2 := `
		SELECT trace_id,
		       argMaxIfMerge(root_name_state)                              AS root_name,
		       ` + s.traceDisplaySvcExpr() + `                           AS root_svc,
		       minMerge(trace_start_state)                                 AS trace_start,
		       (maxMerge(trace_end_state) -
		        toUnixTimestamp64Nano(minMerge(trace_start_state))) / 1e6  AS dur_ms,
		       countMerge(span_count_state)                                AS span_count,
		       toUInt8(countMerge(error_count_state) > 0)                  AS has_error,
		       countMerge(error_count_state)                               AS err_spans,
		       min(time_bucket)                                            AS first_bucket,
		       max(time_bucket)                                            AS last_bucket
		FROM trace_summary_5m
		WHERE ` + traceIDClause + `time_bucket >= ? AND time_bucket < ?
		GROUP BY trace_id` + havingSQL + `
		ORDER BY ` + sortExpr + ` ` + order + `
		LIMIT ? OFFSET ?
		SETTINGS max_execution_time = 12,
		         optimize_read_in_order = 1,
		         optimize_aggregation_in_order = 1`

	// v0.9.278 — Stage 2's floor. `trace_id IN (...)` prunes NOTHING on its
	// own: trace_summary_5m carries no skip index on trace_id (the "uses the
	// bloom filter on trace_id" note in store.go is stale — that bloom is on
	// `spans`). EXPLAIN indexes=1 with a 4996-element set kept 208/210
	// granules. The time floor is what prunes; measured 1,145,778 read rows
	// before, 99,094 after, same ids.
	//
	// But narrowing the floor can CLIP: a trace with rows below it loses part
	// of its own aggregate, so trace_start / dur_ms / span_count / has_error
	// come back wrong — the exact silent-wrong-number class this codebase
	// keeps getting bitten by. A fixed lookback would be an ASSUMPTION, and
	// one this install cannot test: the widest trace bucket-span measured
	// locally is 5 minutes and ZERO of 565,546 traces exceed it. So we do not
	// assume — we DETECT, by asking Stage 2 for min(time_bucket) and widening
	// the floor if any returned row sits on it.
	out, hasMore, err := s.runTraceStage2(ctx, f, stage2, idArgs, stage2Floor, stage2Ceil, pageLimit)
	if err != nil {
		return nil, 0, false, err
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
	Env string
	// Cluster — türetilmiş k8s/openshift cluster adı daraltması
	// (v0.9.943, B3). Env ile aynı birinci-sınıf gerekçe; boş değer
	// conjunct ÜRETMEZ ve MV hızlı yolunu diskalifiye ETMEZ.
	Cluster string
	Filters []FilterExpr
	// FilterRoot — grouped AND/OR builder (v0.8.x gap-2). Supersedes Filters
	// when non-nil; flat-AND is byte-identical to the legacy path, OR /
	// nested disqualifies the trace_summary MV fast-path.
	FilterRoot *FilterGroup
	Sort       string // "count"|"perMin"|"errorRate"|"avg"|"p50"|"p95"|"p99"|"max"|"name"
	Order      string // "asc"|"desc"
	Limit      int
	// Having — v0.8.453 (B2-c): genel post-aggregate koşullar
	// ("errorRate > 1 AND p95 > 500"). Yalnız compileHaving'in
	// whitelist'inden geçer; MV fast-path'i diskalifiye ETMEZ (dış
	// SELECT'in kolon takma adları iki yolda da aynı) — performans
	// operatör şartı.
	Having []HavingExpr
}

// HavingExpr — Aggregated görünümünde bir post-aggregate koşul.
// Metric adları sort whitelist'ini aynalar; Op yalnız karşılaştırma.
// SQL'e YALNIZ compileHaving'in kolon haritasından girer (değer her
// zaman bind parametresi) — ham interpolasyon yok.
type HavingExpr struct {
	Metric string  `json:"metric"` // count|perMin|errorRate|avg|p50|p95|p99|max
	Op     string  `json:"op"`     // > >= < <=
	Value  float64 `json:"value"`
}

// maxHavingExprs — UI'nin makul üstü; kötü niyetli/bozuk istek
// sınırsız koşul zinciriyle SQL şişiremesin.
const maxHavingExprs = 8

var havingCols = map[string]string{
	"count":     "trace_count",
	"perMin":    "per_min",
	"errorRate": "error_rate",
	"avg":       "avg_ms",
	"p50":       "p50_ms",
	"p95":       "p95_ms",
	"p99":       "p99_ms",
	"max":       "max_ms",
}

var havingOps = map[string]bool{">": true, ">=": true, "<": true, "<=": true}

// compileHaving derler: " AND col OP ?" parçaları + bind arg'ları.
// Bilinmeyen metric/op ve limit aşımı hatadır (HTTP katmanı 400'ler).
// Pure — having_test.go'da tablo-testli.
func compileHaving(hs []HavingExpr) (string, []any, error) {
	if len(hs) == 0 {
		return "", nil, nil
	}
	if len(hs) > maxHavingExprs {
		return "", nil, fmt.Errorf("having: en fazla %d koşul (%d geldi)", maxHavingExprs, len(hs))
	}
	var sb strings.Builder
	args := make([]any, 0, len(hs))
	for _, h := range hs {
		col, ok := havingCols[h.Metric]
		if !ok {
			return "", nil, fmt.Errorf("having: bilinmeyen metrik %q", h.Metric)
		}
		if !havingOps[h.Op] {
			return "", nil, fmt.Errorf("having: bilinmeyen operatör %q", h.Op)
		}
		sb.WriteString(" AND " + col + " " + h.Op + " ?")
		args = append(args, h.Value)
	}
	return sb.String(), args, nil
}

// ValidateHaving — HTTP katmanının 400 kapısı; derleyip atar.
func ValidateHaving(hs []HavingExpr) error {
	_, _, err := compileHaving(hs)
	return err
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
		// v0.9.943 — cluster aynı gerekçeyle (B3/H15); KOŞULLU.
		f.Env == "" && f.Cluster == "" &&
		len(f.Filters) == 0 &&
		!f.FilterRoot.hasPredicate() &&
		!f.From.IsZero() && !f.To.IsZero() &&
		f.To.Sub(f.From) >= 5*time.Minute &&
		!s.TraceMVGap(ctx, f.From, f.To) { // v0.10.124 — MV'de boş gün: ham yol
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
	// v0.10.258 — aynı sınıf: hata yüklemi başka span-düzeyi yüklem
	// (search / attr filtreleri) varken WHERE'de kalırsa aynı span hem
	// hata hem aranan olmak zorunda; iç HAVING'e taşınır (aşağıda).
	if f.HasError && aggHasErrorSpanLocal(f) {
		wc.add("status_code = 'error'")
	}
	// v0.8.383 — env narrowing, always ANDed (see AggregateFilter.Env).
	if f.Env != "" {
		wc.add("deploy_env = ?", f.Env)
	}
	// v0.9.943 (B3) — cluster narrowing. clusterExpr(): terfi edilmiş
	// kolon varsa o, yoksa 6-anahtar derive — /services, /endpoints ve
	// /databases ile AYNI soru. BOŞKEN conjunct YOK (H15).
	addClusterConjunct(&wc, s.clusterExpr(), f.Cluster)
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
	groupExpr, extraExpr, groupArgs := aggregateGroupExpr(f)

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
	// SETTINGS max_execution_time = 25 protects the UI thread —
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
	if f.HasError && !aggHasErrorSpanLocal(f) {
		searchHaving += " AND " + traceHasErrorHaving // v0.10.258 — trace-düzeyi hata
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
	// v0.8.453 — genel HAVING (B2-c); whitelist derlemesi.
	hSQL, hArgs, err := compileHaving(f.Having)
	if err != nil {
		return nil, err
	}
	postFilter += hSQL
	args = append(args, hArgs...)

	sql += `
		GROUP BY group_key, group_extra`
	if postFilter != "" {
		sql += `
		HAVING 1=1` + postFilter
	}
	sql += `
		ORDER BY ` + sortCol + ` ` + order + `
		LIMIT ?
		SETTINGS max_execution_time = 25, max_threads = 8`
	args = append(args, f.Limit)

	rows, err := s.telemetryReadConn().Query(ctx, sql, args...)
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
//   - argMaxIfState(root_service / root_name) → group key
//   - countState                              → trace_count
//   - quantilesState                          → p50 / p95 / p99
//   - minMerge(trace_start_state)             → trace_start
//   - countIfState(error_count_state)         → error_count
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
		keyExpr = s.traceDisplaySvcExpr()
		extraExpr = "''"
	default: // "" or "operation"
		keyExpr = "argMaxIfMerge(root_name_state)"
		extraExpr = s.traceDisplaySvcExpr()
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
	// v0.9.1185 — İKİ pencere, iki rol. İç sorgu GROUP BY trace_id yapıyor
	// (TOPLAMA) → tavan slack'li; içindeki servis-indeksi alt sorgusu
	// "hangi trace'ler" diyor (SEÇİM) → tavan `< f.To`, slacksiz. Aynı
	// sınırı ikisine de vermek, ya seçim penceresini kaydırırdı ya da
	// sınırı aşan trace'in süresini budardı.
	//
	// bounded, servis filtresine bağlı: filtre varsa iç sorgu
	// `trace_id IN (…)` ile kısıtlı ve slack ~bedava; filtresizken tüm
	// pencere GROUP BY ediliyor ve aggWindowEnd slack'i pencere
	// genişliğiyle kelepçeliyor (15dk pencerede 1sa slack 5× tarama
	// olurdu).
	innerWhere := "WHERE time_bucket >= ? AND time_bucket < ?"
	innerArgs := []any{f.From, aggWindowEnd(f.From, f.To, f.Service != "")}
	if f.Service != "" {
		// v0.10.102 — GLOBAL: liste yolundaki 288'in AYNISI Aggregated
		// sekmesindeydi (sözleşmenin ikinci yarısı; auditor'ın yeni
		// çok-satır taraması buldu).
		innerWhere += ` AND trace_id GLOBAL IN (
		    SELECT DISTINCT trace_id FROM trace_service_index_5m
		    WHERE service_name = ? AND time_bucket >= ? AND time_bucket < ?
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
		           ` + s.traceDisplaySvcExpr() + ` AS root_svc,
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
	// v0.8.453 — genel HAVING (B2-c). MV yolunun dış SELECT'i aynı
	// kolon takma adlarını taşır; fast-path HAVING'le de hızlı kalır.
	hSQL, hArgs, herr := compileHaving(f.Having)
	if herr != nil {
		return nil, herr
	}
	postFilter += hSQL
	postArgs = append(postArgs, hArgs...)
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

	rows, err := s.telemetryReadConn().Query(ctx, sql, args...)
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
// traceWindowSteps — pencere aramasının kademeleri (v0.9.578).
//
// Dar başlar çünkü trace_summary_5m partisyonu toDate(time_bucket):
// 24 saatlik bir sınır ~1 partisyona iner, 90 günlük sınırsız arama
// ise 90 partisyonu tarar. Operatörlerin açtığı trace'lerin ezici
// çoğunluğu tazedir.
var traceWindowSteps = []time.Duration{
	24 * time.Hour,
	7 * 24 * time.Hour,
	90 * 24 * time.Hour, // MV'nin TTL'i
}

// traceScanFloor — pencere hiç çözülemediğinde spans taramasının
// tavanı. spans TTL'i 30 gün; bundan eskisini sormanın karşılığı yok
// ve sınırsız bırakmak sert kısıt ihlali.
const traceScanFloor = 31 * 24 * time.Hour

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

// FindTraceIDBySpan — v0.9.548. Çıplak bir SPAN id'sinin ait olduğu
// trace'i bulur. "" = bulunamadı (hata değil).
//
// Operatör CoSRE'ye 16-hex yapıştırdığında gereken tek şey bu: trace
// bulununca mevcut kanıt paketi (buildTraceExplainInput, v0.9.537)
// olduğu gibi devreye giriyor.
//
// MALİYET UYARISI — spans ORDER BY (service_name, time) ve span_id'de
// İNDEKS YOK (trace_id'de bloom var, span_id'de yok). Yani bu arama
// pencere içinde bir kolon taraması. Kabul edilebilir çünkü:
//   - açık bir operatör eylemi (ID yapıştırdı), arka plan yoklaması değil
//   - LIMIT 1 → eşleşme bulununca CH erken çıkar
//   - pencere ÇAĞIRAN tarafından sınırlanır + max_execution_time tavanı,
//     yani eşleşme YOKKEN bile maliyet üstten kapalı
//
// Bunu bir hot path'e ya da bir poll'a bağlamak YANLIŞ olur.
func (s *Store) FindTraceIDBySpan(ctx context.Context, spanID string, from, to time.Time) (string, error) {
	spanID = strings.TrimSpace(spanID)
	if spanID == "" {
		return "", nil
	}
	var traceID string
	err := s.telemetryReadConn().QueryRow(ctx, `
		SELECT trace_id
		FROM spans
		WHERE span_id = ? AND time >= ? AND time <= ?
		LIMIT 1
		SETTINGS max_execution_time = 8`,
		spanID, chDateTime64Arg(from), chDateTime64Arg(to)).Scan(&traceID)
	if err != nil {
		// Satır yok = bulunamadı; çağıran bunu dürüst kanıt olarak
		// anlatır, hata olarak değil.
		if strings.Contains(err.Error(), "no rows") {
			return "", nil
		}
		return "", err
	}
	return traceID, nil
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
	// v0.9.578 (operator-reported, prod): pencere araması KADEMELİ ve
	// ZAMAN SINIRLI.
	//
	// Öncesi tek sorguydu: `WHERE trace_id = ?`, zaman sınırı YOK.
	// trace_summary_5m'in sıralaması (time_bucket, trace_id) ve
	// partisyonu toDate(time_bucket) — yani trace_id ile arama ÖNCÜ
	// kolonu kullanamıyor ve 90 GÜNLÜK TÜM partisyonları tarıyordu.
	// Prod log'u: her trace açılışında "Timeout exceeded: elapsed
	// 3019ms, maximum: 3000ms".
	//
	// Sonuç iki kat kötüydü: (a) her açılışta 3 saniye boşa yanıyor,
	// (b) ön-sorgu başarısız olunca spans taraması ZAMAN SINIRSIZ
	// kalıyordu — CLAUDE.md'nin sert kısıtının ihlali, ve zaten bu
	// optimizasyonun ÖNLEMEK için var olduğu şey.
	//
	// Kademeli pencere bunu tersine çeviriyor: dar pencere partisyon
	// budar ve ORDER BY önekini kullanır. Operatörlerin açtığı
	// trace'lerin ezici çoğunluğu tazedir, yani ilk adım pratikte
	// hemen isabet eder; eski bir trace için ikinci/üçüncü adımın
	// bedeli yalnız o istekte ödenir.
	var winStart time.Time
	var winEndNanos int64
	bounded := false
	for _, step := range traceWindowSteps {
		since := time.Now().Add(-step)
		err := s.telemetryReadConn().QueryRow(ctx, `
			SELECT minMerge(trace_start_state), toInt64(maxMerge(trace_end_state))
			FROM trace_summary_5m
			WHERE trace_id = ? AND time_bucket >= ?
			SETTINGS max_execution_time = 3`,
			// time_bucket DateTime (DateTime64 DEĞİL): toStartOfInterval
			// saniye grenli INTERVAL ile düz DateTime üretiyor. Nanosaniyeli
			// bir argüman code 53 ile reddedilir — v0.9.578'de tam bu oldu.
			traceID, chDateTimeArg(since)).Scan(&winStart, &winEndNanos)
		if err != nil {
			// Zaman aşımı/hata: daha GENİŞ pencere daha da yavaş olur,
			// denemeye devam etmek anlamsız. Sınırsız taramaya düşme —
			// aşağıdaki son çare TTL sınırını uyguluyor.
			log.Printf("[trace] %s pencere araması başarısız (%s içinde): %v", traceID, step, err)
			break
		}
		if lo, hi, ok := traceTimeBound(winStart, winEndNanos); ok {
			where += " AND time >= ? AND time <= ?"
			args = append(args, lo, hi)
			bounded = true
			break
		}
		// Satır yok: trace bu pencerede değil, bir sonrakini dene.
	}
	if !bounded {
		// SON ÇARE — yine de SINIRLI. Pencereyi çözemedik ama spans
		// taramasının sınırsız kalması kabul edilemez: retention
		// tavanı en azından niyeti koda yazıyor ve TTL uzarsa sorgu
		// sessizce büyümez.
		where += " AND time >= ?"
		args = append(args, chDateTime64Arg(time.Now().Add(-traceScanFloor)))
		log.Printf("[trace] %s penceresi çözülemedi — tarama %s ile sınırlandı", traceID, traceScanFloor)
	}

	rows, err := s.telemetryReadConn().Query(ctx, `
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
	Service string
	// Env (v0.8.400 — env-separation Phase 4) — deployment-environment
	// filter, applied as the bounded res-array conjunct
	// logsEnvChainSQL. Empty = all environments.
	Env string
	// Pod (v0.9.1249) — k8s pod scope for the /logs Context modal,
	// applied as the bounded res-array conjunct logsPodChainSQL.
	// Empty = all pods.
	Pod         string
	Search      string
	From, To    time.Time
	SeverityMin uint8
	TraceID     string
	SpanID      string // optional: only logs attached to this span
	// HasTrace (v0.8.406) — only rows with a trace correlation
	// (trace_id != ''). Applied before the SinceNs branch so the
	// count(), page read AND forward-tail all carry it.
	HasTrace bool
	Limit    int
	Offset   int
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
// for a missing key and arr[0] is ” in CH, so absent keys coalesce
// cleanly. Pinned by TestLogsEnvChainSQL (repo_logs_env_test.go).
const logsEnvChainSQL = `coalesce(
			nullIf(res_values[indexOf(res_keys, 'deployment.environment.name')], ''),
			nullIf(res_values[indexOf(res_keys, 'deployment.environment')], ''),
			'')`

// logsPodChainSQL is the logs-local k8s pod derivation (v0.9.1249 —
// the Context modal's "only this pod" scope). Canonical semconv key
// first, then the pipeline spellings; res-array indexOf lookups like
// every other logs-table derivation (the table stores attributes as
// parallel arrays, not Map columns). MUST stay semantically identical
// to logstore's chLogsPodExpr — GetLogs and Histogram/FieldStats
// filter the same rows, and a chain that diverges between them is the
// v0.8.400 map-access class all over again. Pinned by
// TestLogsPodChainSQL + TestLogsWhere_PodConjunct (repo_logs_pod_test.go).
const logsPodChainSQL = `coalesce(
			nullIf(res_values[indexOf(res_keys, 'k8s.pod.name')], ''),
			nullIf(res_values[indexOf(res_keys, 'kubernetes.pod_name')], ''),
			nullIf(res_values[indexOf(res_keys, 'kubernetes.pod.name')], ''),
			nullIf(res_values[indexOf(res_keys, 'pod_name')], ''),
			'')`

// logsRowKeyExpr is the ONE SQL expression that makes a log line
// distinguishable among rows sharing a DateTime64(9) timestamp. It is
// a deterministic 64-bit hash over the columns that together identify
// a log line: same row → same hash across every query, so it is a
// stable keyset tiebreak with no stored column / migration.
//
// v0.7.23 (SAFE-CORE) — the prior tiebreak was `span_id` (String
// DEFAULT ”). But (time, span_id) is NOT a total order on the logs
// table: most log lines are emitted OUTSIDE a span (span_id=”) and
// DateTime64(9) timestamps collide at billions/day. A page boundary
// landing inside a run of (t0,”) rows dropped every remaining
// (t0,”) row on the next page, because `time = t0 AND span_id < ”`
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
//
// v0.9.295 — the token also carries the DIRECTION it was minted in.
// The keyset is a strict inequality, so a cursor from a newest-first
// page paged in oldest-first order walks the wrong way and returns a
// plausible, wrong slice. Before, direction was simply ignored while a
// cursor was present; now the mismatch is DETECTED and the cursor is
// dropped, which costs the operator a return to page one instead of a
// silently wrong page.
type LogsCursor struct {
	TimeNs    int64
	RowKey    uint64
	Ascending bool
}

// EncodeLogsCursor renders a (timeNs, rowKey) position as the opaque
// base64 token the API layer round-trips. Kept as a pure function so
// the roundtrip is unit-testable (CLAUDE.md #11).
func EncodeLogsCursor(timeNs int64, rowKey uint64, ascending bool) string {
	raw := "ch|" + strconv.FormatInt(timeNs, 10) + "|" + strconv.FormatUint(rowKey, 10)
	// A 4th field marks oldest-first. Omitted for the default so tokens
	// minted before v0.9.295 keep decoding as what they were: DESC.
	if ascending {
		raw += "|a"
	}
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
	// Expect "ch|<ns>|<rowkey>" with an optional "|a" direction flag.
	parts := strings.Split(s, "|")
	if len(parts) < 3 || len(parts) > 4 || parts[0] != "ch" {
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
	asc := false
	if len(parts) == 4 {
		if parts[3] != "a" {
			// An unknown 4th field is a token from a future/other
			// encoder — reject rather than guess its meaning.
			return LogsCursor{}, false
		}
		asc = true
	}
	return LogsCursor{TimeNs: ns, RowKey: rk, Ascending: asc}, true
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
// `span_id < ”` did before v0.7.23. Returns ("", nil) when the cursor
// is empty so the first page applies no keyset. Pure function for
// table-driven testing (CLAUDE.md #11).
func logsKeysetPredicate(c LogsCursor, hasCursor bool) (string, []interface{}) {
	if !hasCursor {
		return "", nil
	}
	// v0.9.295 — direction. Oldest-first pages forward through GREATER
	// positions; the comparison flips wholesale, both legs, or the
	// boundary row is re-returned or skipped.
	cmp := "<"
	if c.Ascending {
		cmp = ">"
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
	sql := "(toUnixTimestamp64Nano(time) " + cmp + " ? OR (toUnixTimestamp64Nano(time) = ? AND " + logsRowKeyExpr + " " + cmp + " ?))"
	return sql, []interface{}{c.TimeNs, c.TimeNs, c.RowKey}
}

// GetLogs reads a page of the logs table newest-first. v0.7.22
// (SAFE-CORE) hardened it for billion-row scale:
//
//   - Bounded LIMIT (capped at logsMaxLimit) + SETTINGS
//     max_execution_time = 25 — CLAUDE.md hard constraint that was
//     missing before (the count() + main SELECT could full-scan
//     unbounded).
//   - STABLE sort: ORDER BY time DESC, <rowKey> DESC, where rowKey is
//     a deterministic cityHash64 over the line's identifying columns
//     (logsRowKeyExpr). v0.7.23 (SAFE-CORE) replaced the span_id
//     tiebreak: span_id is String DEFAULT ” and most log lines are
//     emitted outside a span, so (time, span_id) was not a total
//     order — a page boundary inside a run of (t0,”) rows dropped
//     every remaining (t0,”) row on the next page. The hash makes
//     (time, rowKey) a provable total order, so no boundary
//     drop/dup.
//   - Keyset cursor paging: when f.Cursor decodes, page strictly
//     AFTER the encoded (time, rowKey) instead of OFFSET. Empty
//     cursor → first page; Offset still honoured for back-compat.
//
// Returns the rows, the (capped-cost) total match count for the UI,
// and a NextCursor — empty when fewer than the requested limit came
// back (last page).
// logsWhere builds GetLogs' conjunct set. Extracted as a pure seam
// (v0.9.1249) so every filter field can be pinned WITHOUT a live CH:
// the failure this guards is not a wrong row set, it is a filter the
// caller sets and the query silently ignores — the class that cost
// v0.8.400 (map access on absent columns) and is invisible in a
// const-shape-only test. Every conjunct lands here, BEFORE GetLogs'
// SinceNs branch, so the count(), the page read AND the forward-tail
// all carry it.
func logsWhere(f LogFilter) whereClause {
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
		// v0.8.400 — env conjunct.
		wc.add(logsEnvChainSQL+" = ?", f.Env)
	}
	if f.Pod != "" {
		// v0.9.1249 — pod conjunct (Context modal "yalnız bu pod").
		wc.add(logsPodChainSQL+" = ?", f.Pod)
	}
	if f.Search != "" {
		// v0.9.1385 — yüklem log_search_predicate.go'ya çıktı. İki kusur
		// birden kapandı: LIKE argümanı artık kaçışlı (operatörün yazdığı
		// `%`/`_` joker olmuyor) ve v0.8.521'in çıplak-hex kolon dalı
		// burada da var — o sözleşme ekranın yalnız histogram yarısında
		// çalışıyordu.
		// v0.10.279 — yüklem artık logql AST'sinden derleniyor
		// (log_query_compile.go): alan yazımı gerçek kolon/res-array
		// yüklemi, serbest metin gövde araması; ayrışmayan metin eski
		// alt-dize yoluna düşer.
		if expr, args := LogSearchConjunct(f.Search); expr != "" {
			wc.add(expr, args...)
		}
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
	if f.HasTrace {
		wc.add("trace_id != ''")
	}
	return wc
}

func (s *Store) GetLogs(ctx context.Context, f LogFilter) ([]LogRow, uint64, string, error) {
	wc := logsWhere(f)
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
		rows, err := s.telemetryReadConn().Query(ctx, `
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
	if err := s.telemetryReadConn().QueryRow(ctx,
		"SELECT count() FROM logs "+wc.sql()+" SETTINGS max_execution_time = 25",
		wc.args...).Scan(&total); err != nil {
		return nil, 0, "", err
	}

	// Keyset: append the strict (time, span_id) predicate when a
	// cursor decodes. This is additive to the WHERE clause — the
	// first page (empty cursor) keeps OFFSET semantics for callers
	// that page by offset.
	cur, hasCursor := DecodeLogsCursor(f.Cursor)
	// v0.9.295 — a cursor minted in the OTHER direction is dropped, not
	// obeyed and not silently ignored. The keyset is a strict
	// inequality: replaying a newest-first position while paging
	// oldest-first walks away from the rows the operator is reading and
	// returns a plausible, wrong slice. The UI resets paging when the
	// direction flips, so this only fires for a hand-built or
	// bookmarked URL — and there it costs a return to page one, which
	// is visible, rather than a wrong page, which is not.
	if hasCursor && cur.Ascending != f.Ascending {
		hasCursor = false
		cur = LogsCursor{}
	}
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
	// v0.9.295 — direction is honoured WITH a cursor now, because the
	// cursor states which direction it was minted in. A cursor whose
	// direction disagrees with the request is dropped upstream, so by
	// here the two always agree.
	ascending := f.Ascending
	orderDir := "DESC"
	if ascending {
		orderDir = "ASC"
	}

	args := append(wc.args, f.Limit, offset)
	rows, err := s.telemetryReadConn().Query(ctx, `
		SELECT time, severity_num, severity_text, body,
		       service_name, trace_id, span_id,
		       attr_keys, attr_values, res_keys, res_values,
		       `+logsRowKeyExpr+` AS _rowkey
		FROM logs `+wc.sql()+`
		ORDER BY time `+orderDir+`, `+logsRowKeyExpr+` `+orderDir+`
		LIMIT ? OFFSET ?
		SETTINGS max_execution_time = 25`, args...)
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
	// the last page, so no cursor (the UI stops paging).
	//
	// v0.9.295 — ascending reads now page too. The token carries its
	// direction so a cursor minted oldest-first can never be replayed
	// newest-first (DecodeLogsCursor + the mismatch drop above).
	next := ""
	if len(out) == f.Limit {
		next = EncodeLogsCursor(lastTimeNs, lastRowKey, ascending)
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

// metricCatalogSelectSQL — the metric_catalog SELECT, pure so the
// column list is table-tested (v0.9.833).
//
// The two enrichment columns are free riders:
//
//   - last_seen — maxMerge(last_seen_state) was ALREADY computed for
//     the HAVING that ages silent metrics out of the picker; it was
//     just never projected. Reading it costs nothing extra.
//   - serviceCount — service_name is metric_catalog's FIRST ORDER BY
//     column and every row read here already carries it, so uniqExact
//     over the existing GROUP BY adds no scan.
//
// Neither needs a schema change (measured against live data before
// the code was written) — which matters, because an MV state-column
// addition opens a rolling-deploy read-error window.
//
// `paged` false is the defaultUnlimited call (picker prefetch): no
// LIMIT/OFFSET, everything else identical.
func metricCatalogSelectSQL(where string, paged bool) string {
	q := `SELECT metric, anyMerge(description_state), anyMerge(unit_state), anyMerge(instrument_state),
			toUnixTimestamp64Nano(maxMerge(last_seen_state)), uniqExact(service_name)
		 FROM metric_catalog ` + where + `
		 GROUP BY metric
		 HAVING maxMerge(last_seen_state) >= ?
		 ORDER BY metric`
	if paged {
		q += " LIMIT ? OFFSET ?"
	}
	return q + " SETTINGS max_execution_time = 10"
}

// metricNamesRawSelectSQL — the bounded raw metric_points fallback,
// projecting the SAME columns as the catalog path. Pure + tested for
// exactly that reason: the fallback runs on fresh upgrades, so a
// column list that drifts from the catalog's would ship a silently
// emptier catalogue for the first minutes after every deploy — the
// window nobody watches.
func metricNamesRawSelectSQL(where string, paged bool) string {
	q := `SELECT metric, any(description), any(unit), any(instrument),
			toUnixTimestamp64Nano(max(time)), uniqExact(service_name)
		 FROM metric_points ` + where + `
		 GROUP BY metric ORDER BY metric`
	if paged {
		q += " LIMIT ? OFFSET ?"
	}
	return q + " SETTINGS max_execution_time = 25"
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
		if err := s.telemetryReadConn().QueryRow(ctx,
			`SELECT count() FROM (
				SELECT metric FROM metric_catalog `+wc.sql()+`
				GROUP BY metric
				HAVING maxMerge(last_seen_state) >= ?
			) SETTINGS max_execution_time = 10`,
			append(append([]any{}, wc.args...), since)...).Scan(&total); err != nil {
			return nil, 0, err
		}
	}

	query := metricCatalogSelectSQL(wc.sql(), !defaultUnlimited)
	args := append(append([]any{}, wc.args...), since)
	if !defaultUnlimited {
		args = append(args, limit, offset)
	}
	rows, err := s.telemetryReadConn().Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []MetricInfo{}
	for rows.Next() {
		var m MetricInfo
		if err := rows.Scan(&m.Name, &m.Description, &m.Unit, &m.Type, &m.LastSeenNs, &m.ServiceCount); err != nil {
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
// metricCatalogHasRows probes whether metric_catalog holds ANYTHING at
// all — one row off a small state table behind a 5s cap. It is the only
// way to tell "this search matched nothing" (authoritative) from "the
// catalog hasn't populated yet" (the fresh-upgrade window the raw
// fallback exists for).
//
// A failed probe answers false, i.e. it degrades to the pre-v0.9.285
// behaviour: fall back. The probe must never be the reason a working
// install stops answering.
func (s *Store) metricCatalogHasRows(ctx context.Context) bool {
	var any uint8
	if err := s.telemetryReadConn().QueryRow(ctx,
		`SELECT count() > 0 FROM metric_catalog LIMIT 1 SETTINGS max_execution_time = 5`,
	).Scan(&any); err != nil {
		return false
	}
	return any == 1
}

// metricNamesSource is the outcome of decideMetricNamesSource.
type metricNamesSource int

const (
	// metricNamesUseCatalog — trust what the catalog returned, empty or not.
	metricNamesUseCatalog metricNamesSource = iota
	// metricNamesRawScan — run the bounded raw metric_points scan.
	metricNamesRawScan
)

// decideMetricNamesSource encodes WHEN the raw metric_points scan is
// allowed to run. It is deliberately a named function over three
// booleans rather than an inline condition: the v0.9.285 bug was a
// missing row in exactly this table (empty result + populated catalog +
// no search terms fell through to the raw scan), and an inline
// condition is where that kind of gap hides.
//
// The raw scan is the most expensive query this page can issue — the
// subject of two prod incidents — so it runs only when the catalog
// genuinely cannot answer: it errored, or it is empty.
func decideMetricNamesSource(catalogErr error, rows int, catalogHasRows bool) metricNamesSource {
	if catalogErr != nil {
		return metricNamesRawScan // catalog unreadable — nothing else to try
	}
	if rows > 0 {
		return metricNamesUseCatalog
	}
	if catalogHasRows {
		// Empty RESULT over a populated catalog: authoritative. The raw
		// scan carries the same lookback bound, so it could only
		// re-find metrics the catalog deliberately hides — at full cost.
		return metricNamesUseCatalog
	}
	return metricNamesRawScan // genuinely empty catalog (fresh upgrade)
}

func (s *Store) ListMetricNames(ctx context.Context, service, pattern string, limit, offset int) ([]MetricInfo, int, error) {
	defaultUnlimited := limit == 0 && offset == 0 && pattern == ""
	if limit <= 0 {
		limit = 200
	}

	catOut, catTotal, catalogErr := s.listMetricNamesFromCatalog(ctx, service, pattern, limit, offset, defaultUnlimited)
	// v0.9.285 — the probe now runs UNCONDITIONALLY. It used to sit
	// behind `pattern != "" || service != ""`, which excluded the one
	// call that matters most: /metrics' FIRST PAINT sends both empty
	// (Metrics.tsx), so the probe never ran and the page went straight
	// to the raw metric_points scan — the exact query of two prod
	// incidents (v0.8.311, v0.8.396). An empty catalog RESULT is not an
	// empty CATALOG, and only the probe can tell the two apart. The
	// probe is skipped when the catalog already answered with rows or
	// errored, so it costs nothing on the hot path.
	hasRows := false
	if catalogErr == nil && len(catOut) == 0 {
		hasRows = s.metricCatalogHasRows(ctx)
	}
	if decideMetricNamesSource(catalogErr, len(catOut), hasRows) == metricNamesUseCatalog {
		return catOut, catTotal, nil
	}
	if catalogErr != nil {
		log.Printf("[chstore] metric_catalog read failed (%v) — falling back to the raw metric-name scan", catalogErr)
	} else {
		log.Printf("[chstore] metric_catalog empty — falling back to the raw metric-name scan (fills within minutes of first ingest)")
	}

	wc := buildMetricNamesWhere(service, pattern, time.Now().Add(-metricNameLookback))

	var total uint64
	if !defaultUnlimited {
		if err := s.telemetryReadConn().QueryRow(ctx,
			"SELECT count(DISTINCT metric) FROM metric_points "+wc.sql()+
				" SETTINGS max_execution_time = 25",
			wc.args...).Scan(&total); err != nil {
			return nil, 0, err
		}
	}

	query := metricNamesRawSelectSQL(wc.sql(), !defaultUnlimited)
	args := append([]any{}, wc.args...)
	if !defaultUnlimited {
		args = append(args, limit, offset)
	}
	rows, err := s.telemetryReadConn().Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []MetricInfo
	for rows.Next() {
		var mi MetricInfo
		if err := rows.Scan(&mi.Name, &mi.Description, &mi.Unit, &mi.Type, &mi.LastSeenNs, &mi.ServiceCount); err != nil {
			return nil, 0, err
		}
		out = append(out, mi)
	}
	if defaultUnlimited {
		total = uint64(len(out))
	}
	return out, int(total), rows.Err()
}

func (s *Store) GetMetricPoints(ctx context.Context, metric, service string, from, to time.Time, limit int) ([]MetricPointRow, bool, error) {
	// v0.8.454 — pencere artık opsiyonel değil: sıfır from/to varsayılan
	// 1 saate bağlanır + max_execution_time. Önceden penceresiz çağrı tüm
	// metric_points tarihçesini tarıyordu (hard-constraint ihlali).
	from, to = boundWindow(from, to, time.Hour)
	var wc whereClause
	wc.add("metric = ?", metric)
	if service != "" {
		wc.add("service_name = ?", service)
	}
	wc.add("time >= ?", from)
	wc.add("time <= ?", to)
	if limit == 0 {
		limit = 500
	}
	// v0.9.464 (dürüstlük A12) — ORDER BY time DESC: eski ASC + LIMIT,
	// yüksek frekanslı metrikte pencerenin yalnız BAŞINI alıyordu — her
	// seri x-ekseninin ilk üçte birinde bitiyor, compare iki yarım-doğruyu
	// üst üste koyuyordu. Grafiğin asıl önemli kenarı "şimdi"dir: en yeni
	// noktalar alınır, Go'da zaman sırasına çevrilir; tavan dolduysa
	// truncated=true (UI şeridi "pencerenin son N noktası").
	rows, err := s.telemetryReadConn().Query(ctx,
		`SELECT time, value, count, sum_value,
		        arrayStringConcat(arrayMap((k, v) -> concat(k, '=', v), attr_keys, attr_values), ',')
		 FROM metric_points `+wc.sql()+
			` ORDER BY time DESC LIMIT ?
		 SETTINGS max_execution_time = 10`,
		append(wc.args, limit)...)
	if err != nil {
		return nil, false, err
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
			return nil, false, err
		}
		p.Time = t.UnixNano()
		out = append(out, p)
	}
	// DESC geldi — grafiğe zaman sırasıyla ver.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, len(out) >= limit, rows.Err()
}

// boundWindow — v0.8.454: sıfır bırakılmış from/to'yu güvenli varsayılana
// bağlar (to → now, from → to-def). Sınırsız raw tarama hard-constraint
// ihlaliydi (GetMetricPoints / GetExceptions çağıranı pencere geçirmezse
// tüm partisyonları tarıyordu). Pure — bound_window_test.go'da tablo-testli.
func boundWindow(from, to time.Time, def time.Duration) (time.Time, time.Time) {
	if to.IsZero() {
		to = time.Now()
	}
	if from.IsZero() {
		from = to.Add(-def)
	}
	return from, to
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

// aggregateGroupExpr — /traces "Aggregated" sekmesinin gruplama ifadesi,
// ek sütunu ve bind argümanları. SAF (tablo testli).
//
// v0.9.634'te handler'dan çıkarıldı: terfi kolonu çözümlemesi eklenince
// dal karmaşıklaştı ve saf olmayan bir switch'i test etmenin tek yolu
// tüm sorguyu kurmaktı. Emsal: traceExtrasProjection / businessDimExpr —
// aynı "çözümleme saf, sorgu ayrı" ayrımı.
//
// KÖK-SPAN semantiği HER dalda korunur (anyIf … parent_id kökü). Bu
// BİLİNÇLİ bir tasarım — "Uptrace-style: group traces by root
// attributes" — hata değil; kovalamanın orta span'lerdeki değeri
// görmemesi ayrı bir karar konusu ve operatör onayı ister.
func aggregateGroupExpr(f AggregateFilter) (groupExpr, extraExpr string, groupArgs []any) {
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
	groupArgs = []any{}
	switch {
	case f.GroupAttr != "":
		// Sanitisation happened on the HTTP layer — the key is
		// safe to flow through as a `?` parameter. We try span
		// attrs first then resource attrs.
		//
		// v0.9.634 — TERFİ ETMİŞ KOLON önce. Bu dal, haritayı danışan
		// üç kardeşin (traceExtrasProjection, businessDimExpr,
		// FilterExpr.sql) yanında TEK istisnaydı: /traces Aggregated
		// sekmesi koşulsuz dizi araması yapıyordu. Aynı sayfada aynı
		// anahtar, filtrede indeksli kolondan, kırılımda dört şişman
		// diziden okunuyordu.
		//
		// promotedCols() KULLANILIYOR, promotedAttrResolve DEĞİL:
		// GroupAttr KULLANICI girdisi ve OTel'de attribute anahtarları
		// harf duyarlı (v0.9.624'ün ayrımı — kod içi sabit liste bir
		// KAVRAMI, kullanıcı girdisi bir ANAHTARI ifade eder).
		//
		// Kolon yalnız SPAN attribute'unu taşıyor; harita ıskalarsa
		// resource fallback'li coalesce olduğu gibi kalıyor.
		// v0.9.635 — KÖK-ÖNCELİKLİ, kök taşımıyorsa DÜŞÜŞ YOLU.
		//
		// Öncesi yalnız kök span'e bakıyordu ve bu SESSİZ VERİ KAYBI
		// üretiyordu: kök anahtarı taşımıyorsa group_key='' oluyor ve
		// trace `HAVING group_key != ''` ile TAMAMEN ELENİYORDU —
		// yanlış kovaya düşmüyor, yok oluyordu. Hiçbir trace'in kökünde
		// yoksa tablo komple boş dönüyor ve operatör "bu attribute yok"
		// diye okuyor.
		//
		// Tipik şekil tam bunu tetikliyor: kök span gateway'in generic
		// HTTP span'i, iş kodu bir alt serviste set ediliyor.
		//
		// Dahası AYNI SAYFA iki cevap veriyordu: trace listesinin ekstra
		// kolonu kök-bağımsız (repo.go traceExtrasProjection), yani
		// listede `channel_code = MOBILE` görünen bir trace'i Aggregated
		// sekmesi hiç saymıyordu. İki yüzeyin çelişmesi tasarım değil.
		//
		// "Uptrace-style: group traces by root attributes" yorumu niyeti
		// anlatıyordu ama blok terfi-kolonu katmanlarından aylar önce
		// yazılmış ve hiç revize edilmemiş.
		//
		// argMinIf (anyIf DEĞİL): düşüş yolu DETERMİNİSTİK olmalı —
		// değeri taşıyan EN ERKEN span kazanır. anyIf ile aynı trace
		// iki koşuda farklı kovaya düşebilirdi.
		val := "coalesce(" +
			"nullIf(attr_values[indexOf(attr_keys, ?)], ''), " +
			"nullIf(res_values[indexOf(res_keys, ?)], ''))"
		valArgs := []any{f.GroupAttr, f.GroupAttr}
		if col, ok := promotedCols()[f.GroupAttr]; ok {
			val, valArgs = col, nil
		}
		groupExpr = "coalesce(" +
			"nullIf(anyIf(" + val + ", (parent_id = '' OR parent_id = '0000000000000000')), ''), " +
			"argMinIf(" + val + ", time, " + val + " != ''), '')"
		// Bind sırası SQL'deki görünme sırasıyla BİREBİR: val üç kez
		// geçiyor (kök anyIf, argMin değeri, argMin koşulu).
		groupArgs = append(groupArgs, valArgs...)
		groupArgs = append(groupArgs, valArgs...)
		groupArgs = append(groupArgs, valArgs...)
		extraExpr = "anyIf(service_name, (parent_id = '' OR parent_id = '0000000000000000'))"
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
	return groupExpr, extraExpr, groupArgs
}
