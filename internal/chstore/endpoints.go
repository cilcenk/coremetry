package chstore

import (
	"context"
	"time"
)

// EndpointRow is one (service, path) tuple's RED rollup for the
// /endpoints page. Path resolves to http.route when the SDK
// emits the templated form (e.g. "/api/users/{id}"); falls back
// to url.path (the concrete request path) when route is empty
// — matches the operator-confirmed v0.5.365 priority order so
// frameworks that already route-template don't blow cardinality
// while plainly-instrumented services still surface useful
// rows.
type EndpointRow struct {
	Service    string  `json:"service"`
	Path       string  `json:"path"`
	Method     string  `json:"method,omitempty"`
	Calls      uint64  `json:"calls"`
	Errors     uint64  `json:"errors"`
	ErrorRate  float64 `json:"errorRate"`
	AvgMs      float64 `json:"avgMs"`
	P99Ms      float64 `json:"p99Ms"`
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
}

// GetEndpoints aggregates RED stats per (service_name, derived
// endpoint path) over the window. Returns top `limit` rows by
// call count so a high-cardinality path (concrete IDs that
// slipped past the http.route fallback) doesn't dominate the
// JSON payload.
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
// frameworks that don't auto-decorate kind. We now keep any
// span with a real path that isn't an OUTGOING call.
func (s *Store) GetEndpoints(ctx context.Context, from, to time.Time, service string, search string, cluster string, limit int) ([]EndpointRow, error) {
	if limit <= 0 || limit > 10000 {
		limit = 500
	}
	if from.IsZero() {
		from = time.Now().Add(-1 * time.Hour)
	}
	if to.IsZero() {
		to = time.Now()
	}
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
	args := []any{from, to}
	whereSvc := ""
	if service != "" {
		whereSvc = " AND service_name = ?"
		args = append(args, service)
	}
	whereSearch := ""
	if search != "" {
		whereSearch = " AND positionCaseInsensitive(" + pathExpr + ", ?) > 0"
		args = append(args, search)
	}
	// v0.5.372 — cluster filter, same derive expression as the
	// /services page so an operator who filtered there sees a
	// symmetric set here. clusterDeriveExpr coalesces six
	// resource/attr keys (k8s.cluster.name, openshift.cluster.name,
	// cluster — across res + attr arrays) into a single canonical
	// string. Empty cluster filter = "all clusters".
	whereCluster := ""
	if cluster != "" {
		whereCluster = " AND " + clusterDeriveExpr + " = ?"
		args = append(args, cluster)
	}
	// v0.5.370 — single-pass aggregation including sparkline.
	// Inner per-bucket CTE keys on (service, path, b) and
	// records per-bucket call+error+sum_dur+max_p99 plus a
	// representative method via anyHeavy. Outer GROUP BY
	// (service, path) collapses the buckets, sums the counts,
	// derives error_rate + avg, takes max(p99_per_bucket) as
	// the conservative window p99 (same merge idiom topology
	// MVs use), and arrayMap-rebuilds the dense 30-element
	// sparkline from the sparse (b_idx, b_vals) groupArrays.
	const sparkBuckets = 30
	bucketNs := (to.UnixNano() - from.UnixNano()) / int64(sparkBuckets)
	if bucketNs <= 0 {
		bucketNs = 1
	}
	allArgs := []any{from.UnixNano(), bucketNs}
	allArgs = append(allArgs, args...)
	// Three sparkline arrayMaps, each parameterised by
	// sparkBuckets → three ? placeholders for range(0, ?).
	allArgs = append(allArgs, sparkBuckets, sparkBuckets, sparkBuckets, limit)
	q := `
		WITH per_bucket AS (
		  SELECT service_name,
		         ` + pathExpr + `                                AS path,
		         intDiv(toUnixTimestamp64Nano(time) - ?, ?)      AS b,
		         count()                                         AS bv,
		         countIf(status_code = 'error')                  AS bv_err,
		         sum(duration)                                   AS bv_sum_dur,
		         quantile(0.99)(duration) / 1e6                  AS bv_p99,
		         anyHeavy(http_method)                           AS bv_method
		  FROM spans
		  WHERE time >= ? AND time <= ?
		    AND kind NOT IN ('client', 'producer')
		    AND ` + pathExpr + ` != ''` + whereSvc + whereSearch + whereCluster + `
		  GROUP BY service_name, path, b
		)
		SELECT service_name,
		       path,
		       anyHeavy(bv_method)                              AS method,
		       sum(bv)                                          AS calls,
		       sum(bv_err)                                      AS errors,
		       sum(bv_err) * 100.0 / nullIf(sum(bv), 0)         AS error_rate,
		       sum(bv_sum_dur) / nullIf(sum(bv), 0) / 1e6       AS avg_ms,
		       max(bv_p99)                                      AS p99_ms,
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
		ORDER BY calls DESC
		LIMIT ?
		SETTINGS max_execution_time = 15`
	rows, err := s.conn.Query(ctx, q, allArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EndpointRow{}
	for rows.Next() {
		var r EndpointRow
		var avgMs, p99Ms, errRate *float64
		var sparkline, errorsSparkline, p99Sparkline []float64
		if err := rows.Scan(
			&r.Service, &r.Path, &r.Method,
			&r.Calls, &r.Errors, &errRate, &avgMs, &p99Ms,
			&sparkline, &errorsSparkline, &p99Sparkline,
		); err != nil {
			return nil, err
		}
		r.ErrorRate = safeF(errRate)
		r.AvgMs = safeF(avgMs)
		r.P99Ms = safeF(p99Ms)
		r.Sparkline = sparkline
		r.ErrorsSparkline = errorsSparkline
		r.P99Sparkline = p99Sparkline
		out = append(out, r)
	}
	return out, rows.Err()
}
