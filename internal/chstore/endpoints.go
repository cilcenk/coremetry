package chstore

import (
	"context"
	"errors"
	"strings"
	"time"
)

// errEndpointsMVEnv — GetEndpointsMV's refusal when called with an
// env filter (v0.8.385). See the guard inside GetEndpointsMV.
var errEndpointsMVEnv = errors.New(
	"endpoints MV path cannot filter by env: spanmetrics_1m has no deploy_env dimension — route through GetEndpoints (raw fallback)")

// EndpointRow is one (service, path) tuple's RED rollup for the
// /endpoints page. Path resolves to http.route when the SDK
// emits the templated form (e.g. "/api/users/{id}") with an
// http.target fallback (the ingest column's chain — v0.8.356 MV
// path); the legacy raw path (cluster filter only) additionally
// falls back to url.path, matching the operator-confirmed
// v0.5.365 priority order.
type EndpointRow struct {
	Service   string  `json:"service"`
	Path      string  `json:"path"`
	Method    string  `json:"method,omitempty"`
	Calls     uint64  `json:"calls"`
	Errors    uint64  `json:"errors"`
	ErrorRate float64 `json:"errorRate"`
	AvgMs     float64 `json:"avgMs"`
	P99Ms     float64 `json:"p99Ms"`
	// v0.8.356 — MV-backed columns (Stage-2 slice E1). True window
	// quantiles from the spanmetrics_1m tdigest states (the old raw
	// CTE approximated window p99 as max(per-bucket p99)) plus
	// req/min throughput (calls / window minutes) so the operator
	// compares endpoints across window widths.
	P50Ms     float64 `json:"p50Ms"`
	P95Ms     float64 `json:"p95Ms"`
	ReqPerMin float64 `json:"reqPerMin"`
	// v0.5.370 — call-rate sparkline (30 buckets across the
	// requested window). Lets the operator eye-scan "is this
	// endpoint steady / spiking / dying" from the table row
	// without a chart drill-in. Bucketing happens server-side
	// so the JSON payload size stays bounded regardless of
	// window width.
	Sparkline []float64 `json:"sparkline,omitempty"`
	// v0.5.387 — errors + p99 sparklines on the same 30-bucket
	// grid so the row-level drill-in modal can render all three
	// RED dimensions without a second round-trip. Same payload
	// shape as Sparkline; one float per bucket. Both fields
	// share the bucket boundaries of Sparkline so the modal can
	// drive them off a single time axis.
	ErrorsSparkline []float64         `json:"errorsSparkline,omitempty"`
	P99Sparkline    []float64         `json:"p99Sparkline,omitempty"`
	StatusBreakdown map[string]uint64 `json:"statusBreakdown,omitempty"`
	// v0.5.403 — HTTP status class counts for the (service, path)
	// over the window. Source: http.status_code attr. Server-side
	// classification keeps the payload tight (4 ints per row vs
	// shipping raw codes). Operator reads "is this endpoint
	// throwing 5xx, returning 4xx, or just slow" without drilling
	// into a trace. Zero values when the spans don't carry
	// http.status_code (non-HTTP endpoints, gRPC-only services).
	Http2xx uint64 `json:"http2xx,omitempty"`
	Http3xx uint64 `json:"http3xx,omitempty"`
	Http4xx uint64 `json:"http4xx,omitempty"`
	Http5xx uint64 `json:"http5xx,omitempty"`
	// v0.5.404 — prior-window comparison values, populated only
	// when the caller asked for trend deltas (?compare=prior).
	// Frontend derives the % delta arrows + colour. Zero when
	// the (service, path) didn't exist in the prior window — UI
	// renders these as "NEW" instead of "+∞%".
	PriorCalls   uint64  `json:"priorCalls,omitempty"`
	PriorErrors  uint64  `json:"priorErrors,omitempty"`
	PriorAvgMs   float64 `json:"priorAvgMs,omitempty"`
	PriorP99Ms   float64 `json:"priorP99Ms,omitempty"`
}

// opSig* are the read-time ID-collapsing regexes for the Endpoints
// "Group by shape" toggle, applied IN ORDER (UUID first so the numeric/hex
// rules don't chew its runs). v0.8.x — ALIGNED with the ingest-time
// normalizer templater.NormalizeOperation (the op_group column): same `:id`
// placeholder for every id type, and a long-hex rule mirroring
// LooksLikeOpaqueID's hex≥16 case — so a given path segment collapses to the
// SAME shape whether it's grouped read-time here (/endpoints) or by the
// stored op_group on the service Operations tab. (The op_sig + op_group
// op_sig_align_test pins this.) RE2 syntax (== Go regexp). Residual: opSigWrap
// covers the COMMON id types (numeric / UUID / long-hex); the rarer opaque
// kinds LooksLikeOpaqueID catches (base64url / consonant-only) over-match as a
// SQL regex, so they collapse only on the ingest path — documented, accepted.
// Exported so the cross-package op_sig↔op_group alignment regression test
// (internal/templater, which can't be imported here without a cycle) can pin
// that these read-time patterns collapse a path to the same shape as the
// ingest-time NormalizeOperation.
const (
	OpSigReUUID = `[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`
	OpSigReHex  = `/[0-9a-fA-F]{16,}`
	OpSigReNum  = `/[0-9]+`
)

// opSigWrap wraps an endpoint-path SQL expression in the deterministic
// ID-collapsing above so high-cardinality paths carrying IDs (/orders/8421,
// /orders/8422) cluster into one stable group (/orders/:id). Applied only to
// the GROUP-BY projection; the per-bucket CTE re-computes the quantile over
// the normalized group, so p99/error-rate stay exact (no MV, no ingest
// fan-out). Placeholder + rules match templater.NormalizeOperation.
//
// v0.8.356 — the regex patterns bind as ? args (opSigArgs) instead of
// being inlined. Root cause of the broken "Group by shape" toggle:
// clickhouse-go v2 flips a statement into SERVER-SIDE parameter mode
// whenever the query text matches `{.+:.+}` (query_parameters.go) —
// the inlined `[0-9a-fA-F]{8}` quantifier braces plus the ':id'
// literal satisfied that regex, so every positional arg then failed
// with "unsupported query parameter type" and the whole endpoint
// 500'd. Binding the patterns keeps the braces out of the SQL text.
// Every opSigWrap call site MUST splice opSigArgs() at the matching
// placeholder position.
func opSigWrap(expr string) string {
	return `replaceRegexpAll(replaceRegexpAll(replaceRegexpAll(` + expr +
		`, ?, ':id'), ?, '/:id'), ?, '/:id')`
}

// opSigArgs returns the bind args for ONE opSigWrap expansion, in
// placeholder order (UUID first so the numeric/hex rules don't chew
// its runs — same ordering contract as the consts above).
func opSigArgs() []any { return []any{OpSigReUUID, OpSigReHex, OpSigReNum} }

// EndpointsQuery bundles the /endpoints read inputs (v0.8.356) —
// the arg list outgrew a flat signature when server-side sort +
// the MV/raw dispatch landed.
type EndpointsQuery struct {
	From, To    time.Time
	Service     string
	Search      string
	Cluster     string
	// Env narrows to spans.deploy_env — the global Topbar env picker
	// (v0.8.385, env-separation Phase 2). Like Cluster it forces the
	// raw-spans path: spanmetrics_1m carries no env dimension and the
	// approved strategy is cluster-parity raw-fallback, NO MV changes.
	// Unlike the cluster derive, deploy_env is a typed LowCardinality
	// column, so the conjunct is cheap.
	Env         string
	Limit       int
	BySignature bool
	// Sort / Dir (v0.8.356) — server-side global ordering, whitelisted
	// via endpointsOrderBy. Before this the backend always returned
	// top-N by calls and the client re-sorted that page — "top by p95"
	// was really "top-N-by-calls, reordered".
	Sort, Dir string
	// SkipStatus skips the raw-spans status/method sidecar — set by the
	// compare=prior read, which only needs calls/errors/avg/p99 for the
	// delta merge.
	SkipStatus bool
}

// forcesRaw reports whether the query carries a filter dimension the
// spanmetrics_1m MV does not have — cluster (res/attr derive,
// v0.8.356) or env (deploy_env, v0.8.385). Either one sends the read
// down the bounded raw-spans path. Pinned by env_filter_phase2_test.go.
func (q EndpointsQuery) forcesRaw() bool {
	return q.Cluster != "" || q.Env != ""
}

// endpointsRawFilters builds the OPTIONAL AND-conjuncts of the raw
// /endpoints read's WHERE (service / path-substring / cluster / env)
// plus their bind args in placeholder order — appended by the caller
// right after the mandatory [from, to] pair. Factored pure (no conn)
// so the v0.8.385 SQL-shape tests can pin the env conjunct without a
// live ClickHouse, the env_filter_test.go pattern.
//
// Cluster (v0.5.372) matches the same derive expression as the
// /services page so an operator who filtered there sees a symmetric
// set here: clusterExpr coalesces six resource/attr keys
// (k8s.cluster.name, openshift.cluster.name, cluster — res + attr
// arrays) into one canonical string. Env (v0.8.385) is the global
// Topbar picker's deploy_env — a typed LowCardinality column, no
// derive needed. Empty filter = no conjunct ("all").
func (s *Store) endpointsRawFilters(q EndpointsQuery, pathExpr string) (string, []any) {
	var sql strings.Builder
	var args []any
	if q.Service != "" {
		sql.WriteString(" AND service_name = ?")
		args = append(args, q.Service)
	}
	if q.Search != "" {
		sql.WriteString(" AND positionCaseInsensitive(" + pathExpr + ", ?) > 0")
		args = append(args, q.Search)
	}
	if q.Cluster != "" {
		// v0.8.386 — same PK narrowing the /services raw path gained
		// (operator-reported prod 500): without the promoted cluster
		// column the derive runs per-row over the window; narrowing
		// to the cached cluster members first keeps the derive on the
		// member services' granules only. Skipped when the query is
		// already service-scoped; empty membership = no narrowing
		// (never an empty page from a cold map).
		if q.Service == "" {
			if members := s.clusterMemberServices(context.Background(), q.Cluster); len(members) > 0 {
				holders := make([]string, len(members))
				for i, n := range members {
					holders[i] = "?"
					args = append(args, n)
				}
				sql.WriteString(" AND service_name IN (" + strings.Join(holders, ",") + ")")
			}
		}
		sql.WriteString(" AND " + s.clusterExpr() + " = ?")
		args = append(args, q.Cluster)
	}
	if q.Env != "" {
		sql.WriteString(" AND deploy_env = ?")
		args = append(args, q.Env)
	}
	return sql.String(), args
}

// endpointsOrderBy maps the UI sort ids onto whitelisted ORDER BY
// clauses (v0.8.356, same pattern as exceptionGroupsOrderBy
// v0.8.318) — caller input never reaches the SQL string — with a
// deterministic (service, path) tiebreak so equal-value rows keep a
// stable order across refetches. Unknown ids/dirs fall back to the
// historical calls DESC. Column aliases exist in BOTH the MV read
// and the raw fallback, so the two paths share this mapper.
func endpointsOrderBy(sort, dir string) string {
	col, ok := map[string]string{
		"service":   "service_name",
		"path":      "path",
		"calls":     "calls",
		"errors":    "errors",
		"errorRate": "error_rate",
		"avgMs":     "avg_ms",
		"p50Ms":     "p50_ms",
		"p95Ms":     "p95_ms",
		"p99Ms":     "p99_ms",
		// reqPerMin = calls / windowMinutes with a constant divisor per
		// query — ordering by calls is the identical permutation, and it
		// saves binding the divisor into the ORDER BY.
		"reqPerMin": "calls",
		// Composite "fix me first" score — matches the frontend's
		// impactOf(): calls × p99 × (1 + errorRate).
		"impact": "calls * p99_ms * (1 + error_rate / 100.0)",
	}[sort]
	if !ok {
		return "ORDER BY calls DESC, service_name ASC, path ASC"
	}
	d := "DESC"
	if dir == "asc" {
		d = "ASC"
	}
	return "ORDER BY " + col + " " + d + ", service_name ASC, path ASC"
}

// GetEndpoints dispatches the /endpoints read (v0.8.356, Stage-2
// slice E1). Default path reads the spanmetrics_1m MV (MV-first
// invariant — the old raw CTE was a bounded full-scan of spans at
// billion-span scale; on the reference install it ran 16-19s cold
// and sometimes tripped its own 15s cap). The raw path survives
// ONLY for the cluster + env filters: cluster is derived from
// res/attr arrays (clusterExpr) and env is spans.deploy_env —
// dimensions the MV doesn't carry (v0.8.385 kept it that way:
// cluster-parity raw-fallback, NO MV changes).
//
// Known trade-off, documented: spans.http_route is populated at
// ingest from http.route with an http.target fallback
// (internal/otlp/convert.go) — the MV path therefore does NOT see
// url.path-only spans the raw CTE's coalesce chain caught. Those
// are the untemplated, cardinality-bomb paths; losing them from
// the DEFAULT view is acceptable (they still appear under the
// cluster-filtered raw path).
func (s *Store) GetEndpoints(ctx context.Context, q EndpointsQuery) ([]EndpointRow, error) {
	if q.Limit <= 0 || q.Limit > 10000 {
		q.Limit = 500
	}
	if q.From.IsZero() {
		q.From = time.Now().Add(-1 * time.Hour)
	}
	if q.To.IsZero() {
		q.To = time.Now()
	}
	if q.forcesRaw() {
		return s.getEndpointsRaw(ctx, q)
	}
	return s.GetEndpointsMV(ctx, q)
}

// endpointsWindowMinutes returns the reqPerMin divisor — window
// width in minutes, floored at 1 so sub-minute windows don't
// inflate the rate (same guard QueryTraceGroups' perMin uses).
func endpointsWindowMinutes(from, to time.Time) float64 {
	m := to.Sub(from).Minutes()
	if m < 1 {
		return 1
	}
	return m
}

// spanmetricsSource returns the FROM source for spanmetrics_* reads.
// History: the doorway MVs (v0.8.50) originally missed
// highVolumeTables, so on a chstore-owned cluster they lived as
// PER-SHARD bare-name MVs with no Distributed wrapper — a bare read
// returned one shard's slice, and v0.8.356/358 bridged it with a
// cluster() fan-out here.
//
// v0.8.408 — the real promotion LANDED: spanmetrics_{1m,10s,1s} are
// in highVolumeTables, existing cluster installs are migrated at boot
// by promoteCombinedMVs (RENAME bare → _local + Distributed wrapper,
// data preserved), so the bare name IS the wrapper and already fans
// out across shards. The helper therefore collapses to the bare name
// — and MUST stay collapsed: cluster() over a Distributed wrapper
// reads every shard N times (N× overcount), the exact inverse of the
// v0.8.356 undercount. Kept as a seam (the one place every doorway
// reader resolves its FROM); the table-driven regression test pins
// the collapse.
func (s *Store) spanmetricsSourceFor(table string) string {
	return table
}

// GetEndpointsMV is the spanmetrics_1m-backed /endpoints read
// (v0.8.356). One MV scan produces the whole table: RED counts via
// countMerge/countIfMerge/sumMerge, TRUE window p50/p95/p99 via a
// two-level tdigest merge (per-bucket -MergeState in the CTE, final
// -Merge across buckets — the raw CTE could only max() per-bucket
// p99s), and the three 30-bucket sparklines rebuilt from the MV's
// 1-minute time_bucket series. Sparkline buckets floor at the MV
// grain (1min): a 5m window ships 5 buckets, not 30 — the frontend
// bucketsToSeries derives the axis from array length, so variable
// length is safe.
//
// HTTP status-class pills + the method chip need http_status /
// http_method, which the MV does NOT carry — those come from ONE
// bounded raw-spans sidecar over the returned top-N keys only
// (endpointStatusSidecar), skipped for compare=prior reads.
func (s *Store) GetEndpointsMV(ctx context.Context, q EndpointsQuery) ([]EndpointRow, error) {
	// v0.8.385 — the MV REFUSES an env filter rather than silently
	// ignoring it: spanmetrics_1m has no deploy_env dimension, so
	// honouring the call would return all-env numbers under an env
	// filter (the silent-unfiltered class the env-separation audit
	// bans). Callers must route env through GetEndpoints, which
	// dispatches to the raw path.
	if q.Env != "" {
		return nil, errEndpointsMVEnv
	}
	// Align the window start to the MV grain so the first bucket is
	// wholly inside [from, to] rather than half-clipped.
	from := q.From.Truncate(time.Minute)
	windowSec := q.To.Unix() - from.Unix()
	if windowSec <= 0 {
		windowSec = 60
	}
	// 30 sparkline buckets. v0.9.27 (second-resolution audit R2):
	// kısa pencerede 10s tier'ından oku — 5dk penceresi 60s tabanında
	// yalnız 5 bucket alıyordu, artık 10s'te 30 bucket. 10s tier
	// http_route TAŞIR (route-scoped sorgu uyumlu; canlı kolon
	// paritesi doğrulandı) ve 2g TTL'li — pencere ≤2g VE 1m taban
	// bucket'ı kabalaştırıyorsa (windowSec/30 < 60) 10s'e geç.
	// ClickHouse span-metrik 10s tabanı (operatör kararı) burada da:
	// grain 10'un altına inilmez.
	sourceMV := "spanmetrics_1m"
	minGrain := int64(60)
	if windowSec <= 2*24*3600 && windowSec/30 < 60 {
		sourceMV = "spanmetrics_10s"
		minGrain = 10
	}
	bucketSec := windowSec / 30
	if bucketSec < minGrain {
		bucketSec = minGrain
	}
	nBuckets := int((windowSec + bucketSec - 1) / bucketSec)
	if nBuckets < 1 {
		nBuckets = 1
	}

	// Group-by projection: raw route by default, ID-collapsed
	// signature when the operator toggles "group by shape". The
	// signature transform is a plain string rewrite over the MV's
	// http_route dimension, and tdigest states merge exactly across
	// the collapsed groups — so the toggle rides the MV too.
	pathProj := "http_route"
	if q.BySignature {
		pathProj = opSigWrap("http_route")
	}

	where := "time_bucket >= ? AND time_bucket <= ?" +
		" AND kind NOT IN ('client', 'producer')" +
		" AND http_route != ''"
	// Placeholder order follows appearance in the SQL text: the
	// signature-regex args (inside pathProj, when present) and the
	// intDiv bucket args sit in the SELECT list, BEFORE the WHERE.
	var args []any
	if q.BySignature {
		args = append(args, opSigArgs()...)
	}
	args = append(args, from.Unix(), bucketSec, from, q.To)
	if q.Service != "" {
		where += " AND service_name = ?"
		args = append(args, q.Service)
	}
	if q.Search != "" {
		// Filter on the raw route (like the raw path filters the raw
		// pathExpr) so normalization only affects clustering.
		where += " AND positionCaseInsensitive(http_route, ?) > 0"
		args = append(args, q.Search)
	}
	args = append(args, nBuckets, nBuckets, nBuckets, q.Limit)

	sql := `
		WITH per_bucket AS (
		  SELECT service_name,
		         ` + pathProj + `                                 AS path,
		         intDiv(toUnixTimestamp(time_bucket) - ?, ?)      AS b,
		         countMerge(calls_state)                          AS bv,
		         countIfMerge(error_state)                        AS bv_err,
		         sumMerge(duration_sum_state)                     AS bv_sum_dur,
		         arrayElement(quantilesTDigestMerge(0.5, 0.9, 0.95, 0.99)(duration_q_state), 4) / 1e6 AS bv_p99,
		         quantilesTDigestMergeState(0.5, 0.9, 0.95, 0.99)(duration_q_state) AS q_state
		  FROM ` + s.spanmetricsSourceFor(sourceMV) + `
		  WHERE ` + where + `
		  GROUP BY service_name, path, b
		)
		SELECT service_name,
		       path,
		       sum(bv)                                          AS calls,
		       sum(bv_err)                                      AS errors,
		       sum(bv_err) * 100.0 / nullIf(sum(bv), 0)         AS error_rate,
		       sum(bv_sum_dur) / nullIf(sum(bv), 0) / 1e6       AS avg_ms,
		       arrayElement(quantilesTDigestMerge(0.5, 0.9, 0.95, 0.99)(q_state), 1) / 1e6 AS p50_ms,
		       arrayElement(quantilesTDigestMerge(0.5, 0.9, 0.95, 0.99)(q_state), 3) / 1e6 AS p95_ms,
		       arrayElement(quantilesTDigestMerge(0.5, 0.9, 0.95, 0.99)(q_state), 4) / 1e6 AS p99_ms,
		       arrayMap(i ->
		         toFloat64(coalesce(arrayElement(groupArray(bv), indexOf(groupArray(b), i)), 0)),
		         range(0, ?)
		       )                                                AS sparkline,
		       arrayMap(i ->
		         toFloat64(coalesce(arrayElement(groupArray(bv_err), indexOf(groupArray(b), i)), 0)),
		         range(0, ?)
		       )                                                AS errors_sparkline,
		       arrayMap(i ->
		         toFloat64(coalesce(arrayElement(groupArray(bv_p99), indexOf(groupArray(b), i)), 0)),
		         range(0, ?)
		       )                                                AS p99_sparkline
		FROM per_bucket
		GROUP BY service_name, path
		` + endpointsOrderBy(q.Sort, q.Dir) + `
		LIMIT ?
		SETTINGS max_execution_time = 15` + heavyScanSpill
	rows, err := s.conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	windowMin := endpointsWindowMinutes(q.From, q.To)
	out := []EndpointRow{}
	for rows.Next() {
		var r EndpointRow
		var errRate, avgMs, p50Ms, p95Ms, p99Ms *float64
		var sparkline, errorsSparkline, p99Sparkline []float64
		if err := rows.Scan(
			&r.Service, &r.Path,
			&r.Calls, &r.Errors, &errRate, &avgMs, &p50Ms, &p95Ms, &p99Ms,
			&sparkline, &errorsSparkline, &p99Sparkline,
		); err != nil {
			return nil, err
		}
		r.ErrorRate = safeF(errRate)
		r.AvgMs = safeF(avgMs)
		r.P50Ms = safeF(p50Ms)
		r.P95Ms = safeF(p95Ms)
		r.P99Ms = safeF(p99Ms)
		r.ReqPerMin = float64(r.Calls) / windowMin
		r.Sparkline = sparkline
		r.ErrorsSparkline = errorsSparkline
		r.P99Sparkline = p99Sparkline
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if !q.SkipStatus && len(out) > 0 {
		// Best-effort decoration: a sidecar failure degrades the Status
		// pills / Method chip to "—" instead of failing the whole table
		// (the RED columns above are already final).
		_ = s.endpointStatusSidecar(ctx, q, out)
	}
	return out, nil
}

// endpointStatusSidecar fills Http2xx..Http5xx + Method for rows
// the MV read returned (v0.8.356 decision: keep the status-class
// pills via ONE bounded raw-spans query over the top-N keys rather
// than dropping them — the MV's status_code dimension is span
// status ok/error, not the HTTP class). Bounds: time WHERE on the
// (service_name, time) PK prefix, service/route IN-lists from the
// already-LIMITed result set, LIMIT with cross-product headroom,
// max_execution_time. Uses the dedicated http_status UInt16 column
// (minmax-indexed) — cheaper than the attr_values lookup the old
// CTE did, same ingest source (internal/otlp/convert.go).
func (s *Store) endpointStatusSidecar(ctx context.Context, q EndpointsQuery, rowsOut []EndpointRow) error {
	services := make([]string, 0, 16)
	paths := make([]string, 0, len(rowsOut))
	seenSvc := map[string]struct{}{}
	seenPath := map[string]struct{}{}
	for i := range rowsOut {
		if _, ok := seenSvc[rowsOut[i].Service]; !ok {
			seenSvc[rowsOut[i].Service] = struct{}{}
			services = append(services, rowsOut[i].Service)
		}
		if _, ok := seenPath[rowsOut[i].Path]; !ok {
			seenPath[rowsOut[i].Path] = struct{}{}
			paths = append(paths, rowsOut[i].Path)
		}
	}
	// Same projection as the MV read so signature-mode keys
	// (/orders/:id) match the grouped rows.
	pathProj := "http_route"
	if q.BySignature {
		pathProj = opSigWrap("http_route")
	}
	// The IN-lists cross-product can match (svc, path) combos outside
	// the requested row set (two services sharing "/health"); 3x+100
	// headroom keeps the group scan bounded while making truncation of
	// a requested key unlikely. A truncated key just keeps empty pills.
	sidecarLimit := 3*len(rowsOut) + 100
	// pathProj appears twice (SELECT + WHERE) — splice its regex args
	// at both placeholder positions when signature mode is on.
	var args []any
	if q.BySignature {
		args = append(args, opSigArgs()...)
	}
	args = append(args, q.From, q.To, services)
	if q.BySignature {
		args = append(args, opSigArgs()...)
	}
	args = append(args, paths, sidecarLimit)
	rows, err := s.conn.Query(ctx, `
		SELECT service_name,
		       `+pathProj+`                              AS path,
		       anyHeavy(http_method)                     AS method,
		       countIf(http_status BETWEEN 200 AND 299)  AS http_2xx,
		       countIf(http_status BETWEEN 300 AND 399)  AS http_3xx,
		       countIf(http_status BETWEEN 400 AND 499)  AS http_4xx,
		       countIf(http_status BETWEEN 500 AND 599)  AS http_5xx
		FROM spans
		WHERE time >= ? AND time <= ?
		  AND `+endpointKindPred+`
		  AND service_name IN (?)
		  AND `+pathProj+` IN (?)
		GROUP BY service_name, path
		LIMIT ?
		SETTINGS max_execution_time = 10`,
		args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	type key struct{ svc, path string }
	idx := make(map[key]int, len(rowsOut))
	for i := range rowsOut {
		idx[key{rowsOut[i].Service, rowsOut[i].Path}] = i
	}
	for rows.Next() {
		var svc, path, method string
		var h2, h3, h4, h5 uint64
		if err := rows.Scan(&svc, &path, &method, &h2, &h3, &h4, &h5); err != nil {
			return err
		}
		if i, ok := idx[key{svc, path}]; ok {
			rowsOut[i].Method = method
			rowsOut[i].Http2xx = h2
			rowsOut[i].Http3xx = h3
			rowsOut[i].Http4xx = h4
			rowsOut[i].Http5xx = h5
		}
	}
	return rows.Err()
}

// getEndpointsRaw is the legacy raw-spans read (the pre-v0.8.356
// GetEndpoints), retained ONLY for the cluster + env filters:
// clusterExpr derives the cluster from res/attr arrays and env is
// spans.deploy_env (v0.8.385) — dimensions spanmetrics_1m doesn't
// carry. Upgraded in the same slice so both
// paths return the same shape: true window p50/p95/p99 via
// per-bucket quantilesTDigestState merged in the outer level (the
// old max(per-bucket p99) shipped here since v0.5.370), reqPerMin,
// and the shared endpointsOrderBy whitelist.
//
// Path resolution priority (matches operator config v0.5.365):
//  1. spans.http_route (LowCardinality column populated by the
//     OTel ingest path)
//  2. attr_values[indexOf(attr_keys, 'http.route')] (alt-conv
//     SDKs that put it in attrs)
//  3. attr_values[indexOf(attr_keys, 'url.path')]
//  4. attr_values[indexOf(attr_keys, 'http.target')] (older
//     semconv)
//
// Span filter: kind != 'client' / 'producer' so we count
// inbound requests only — outbound client / messaging-producer
// spans land under the callee's row, not the caller's.
//
// v0.5.386 — operator-reported: rows looked sparse vs reality.
// Root cause: previous filter was `kind IN ('server','consumer')`
// which dropped every span whose SDK left SpanKind unspecified.
// OTLP's UNSPECIFIED gets mapped to 'internal' on ingest (see
// internal/otlp/convert.go kindStr default branch), which is the
// case for a lot of manual instrumentation + older SDKs +
// frameworks that don't auto-decorate kind. We keep any span
// with a real path that isn't an OUTGOING call.
// endpointKindPred — the inbound-only span filter, with the v0.8.560
// client carve-out. ONE definition shared by getEndpointsRaw and
// endpointStatusSidecar so the two can never drift apart (a sidecar with
// a narrower filter silently returns no status breakdown for rows the
// main query admitted).
//
// The plain `kind NOT IN ('client','producer')` killed pathExpr's
// documented url.path/http.target fallback layers: url.path lives almost
// exclusively on kind=client spans (OTel semconv), so the rows that
// needed coalesce layer 3-4 were dropped before the coalesce ever ran —
// the fallback was dead code in practice (operator-reported; measured
// live: 0 such rows locally, the population exists only in prod).
//
// The carve-out admits ONLY client spans that carry no route anywhere.
// A ROUTED client span stays excluded, which is what preserves the
// double-counting rationale above (v0.5.386): those calls already land
// under the callee's server-span row. Producer spans stay excluded
// unconditionally — messaging has no route concept and that traffic
// already has its own page (/messaging, M1 v0.8.364); admitting it here
// would list the same traffic twice.
//
// Relaxing kind does not change scan cost — spans orders by
// (service_name, time); kind was never in the primary key.
const endpointKindPred = `(
		      kind NOT IN ('client', 'producer')
		      OR (
		        kind = 'client'
		        AND nullIf(http_route, '') IS NULL
		        AND nullIf(attr_values[indexOf(attr_keys, 'http.route')], '') IS NULL
		      )
		    )`

func (s *Store) getEndpointsRaw(ctx context.Context, q EndpointsQuery) ([]EndpointRow, error) {
	from, to := q.From, q.To
	limit, bySignature := q.Limit, q.BySignature
	// Build a typed args slice; placeholders are positional so we
	// must guard against the optional service/search clauses
	// being absent.
	const pathExpr = `coalesce(
		nullIf(http_route, ''),
		nullIf(attr_values[indexOf(attr_keys, 'http.route')], ''),
		nullIf(attr_values[indexOf(attr_keys, 'url.path')], ''),
		nullIf(attr_values[indexOf(attr_keys, 'http.target')], ''),
		''
	)`
	filterSQL, filterArgs := s.endpointsRawFilters(q, pathExpr)
	args := append([]any{from, to}, filterArgs...)
	// v0.5.370 — single-pass aggregation including sparkline.
	// Inner per-bucket CTE keys on (service, path, b) and
	// records per-bucket call+error+sum_dur+p99 plus a
	// representative method via anyHeavy. Outer GROUP BY
	// (service, path) collapses the buckets, sums the counts,
	// derives error_rate + avg, merges the per-bucket tdigest
	// states into true window p50/p95/p99 (v0.8.356), and
	// arrayMap-rebuilds the dense 30-element sparkline from the
	// sparse (b_idx, b_vals) groupArrays.
	const sparkBuckets = 30
	bucketNs := (to.UnixNano() - from.UnixNano()) / int64(sparkBuckets)
	if bucketNs <= 0 {
		bucketNs = 1
	}
	// v0.8.356 — signature-regex args (inside pathProj, when present)
	// lead: pathProj sits first in the inner SELECT list.
	var allArgs []any
	if bySignature {
		allArgs = append(allArgs, opSigArgs()...)
	}
	allArgs = append(allArgs, from.UnixNano(), bucketNs)
	allArgs = append(allArgs, args...)
	// Three sparkline arrayMaps, each parameterised by
	// sparkBuckets → three ? placeholders for range(0, ?).
	allArgs = append(allArgs, sparkBuckets, sparkBuckets, sparkBuckets, limit)
	// v0.5.403 — http.status_code class breakdown. Read once into
	// per-row `sc` so the four countIf clauses don't re-evaluate
	// the attr_values[indexOf(…)] expression each time (CH does
	// CSE on identical sub-expressions but the explicit alias is
	// clearer + safer across CH versions). toUInt16OrZero returns
	// 0 on non-numeric or missing values; the BETWEEN bounds
	// naturally exclude 0 so non-HTTP spans don't bias any class.
	const statusExpr = `toUInt16OrZero(attr_values[indexOf(attr_keys, 'http.status_code')])`
	// Group-by projection: raw path by default, normalized signature when the
	// operator toggles "group by shape". Filtering (search, != '') stays on the
	// raw pathExpr so the normalization only affects clustering.
	pathProj := pathExpr
	if bySignature {
		pathProj = opSigWrap(pathExpr)
	}
	// v0.8.356 — per-bucket tdigest states merged in the outer level
	// give TRUE window p50/p95/p99 (parity with the MV path); the old
	// max(per-bucket p99) overstated spiky endpoints. Per-bucket p99
	// (sparkline only) switched quantile→quantileTDigest — bounded
	// memory at any bucket population.
	sql := `
		WITH per_bucket AS (
		  SELECT service_name,
		         ` + pathProj + `                                AS path,
		         intDiv(toUnixTimestamp64Nano(time) - ?, ?)      AS b,
		         count()                                         AS bv,
		         countIf(status_code = 'error')                  AS bv_err,
		         countIf(` + statusExpr + ` BETWEEN 200 AND 299) AS bv_2xx,
		         countIf(` + statusExpr + ` BETWEEN 300 AND 399) AS bv_3xx,
		         countIf(` + statusExpr + ` BETWEEN 400 AND 499) AS bv_4xx,
		         countIf(` + statusExpr + ` BETWEEN 500 AND 599) AS bv_5xx,
		         sum(duration)                                   AS bv_sum_dur,
		         quantileTDigest(0.99)(duration) / 1e6           AS bv_p99,
		         quantilesTDigestState(0.5, 0.95, 0.99)(duration) AS q_state,
		         anyHeavy(http_method)                           AS bv_method
		  FROM spans
		  WHERE time >= ? AND time <= ?
		    AND ` + endpointKindPred + `
		    AND ` + pathExpr + ` != ''` + filterSQL + `
		  GROUP BY service_name, path, b
		)
		SELECT service_name,
		       path,
		       anyHeavy(bv_method)                              AS method,
		       sum(bv)                                          AS calls,
		       sum(bv_err)                                      AS errors,
		       sum(bv_err) * 100.0 / nullIf(sum(bv), 0)         AS error_rate,
		       sum(bv_sum_dur) / nullIf(sum(bv), 0) / 1e6       AS avg_ms,
		       arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(q_state), 1) / 1e6 AS p50_ms,
		       arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(q_state), 2) / 1e6 AS p95_ms,
		       arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(q_state), 3) / 1e6 AS p99_ms,
		       sum(bv_2xx)                                      AS http_2xx,
		       sum(bv_3xx)                                      AS http_3xx,
		       sum(bv_4xx)                                      AS http_4xx,
		       sum(bv_5xx)                                      AS http_5xx,
		       arrayMap(i ->
		         toFloat64(coalesce(arrayElement(groupArray(bv), indexOf(groupArray(b), i)), 0)),
		         range(0, ?)
		       )                                                AS sparkline,
		       arrayMap(i ->
		         toFloat64(coalesce(arrayElement(groupArray(bv_err), indexOf(groupArray(b), i)), 0)),
		         range(0, ?)
		       )                                                AS errors_sparkline,
		       arrayMap(i ->
		         toFloat64(coalesce(arrayElement(groupArray(bv_p99), indexOf(groupArray(b), i)), 0)),
		         range(0, ?)
		       )                                                AS p99_sparkline
		FROM per_bucket
		GROUP BY service_name, path
		` + endpointsOrderBy(q.Sort, q.Dir) + `
		LIMIT ?
		SETTINGS max_execution_time = 15` + heavyScanSpill
	rows, err := s.conn.Query(ctx, sql, allArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	windowMin := endpointsWindowMinutes(from, to)
	out := []EndpointRow{}
	for rows.Next() {
		var r EndpointRow
		var avgMs, p50Ms, p95Ms, p99Ms, errRate *float64
		var sparkline, errorsSparkline, p99Sparkline []float64
		if err := rows.Scan(
			&r.Service, &r.Path, &r.Method,
			&r.Calls, &r.Errors, &errRate, &avgMs, &p50Ms, &p95Ms, &p99Ms,
			&r.Http2xx, &r.Http3xx, &r.Http4xx, &r.Http5xx,
			&sparkline, &errorsSparkline, &p99Sparkline,
		); err != nil {
			return nil, err
		}
		r.ErrorRate = safeF(errRate)
		r.AvgMs = safeF(avgMs)
		r.P50Ms = safeF(p50Ms)
		r.P95Ms = safeF(p95Ms)
		r.P99Ms = safeF(p99Ms)
		r.ReqPerMin = float64(r.Calls) / windowMin
		r.Sparkline = sparkline
		r.ErrorsSparkline = errorsSparkline
		r.P99Sparkline = p99Sparkline
		out = append(out, r)
	}
	return out, rows.Err()
}
