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
// Span filter: kind = 'server' or 'consumer' so we count
// inbound requests only — outbound client spans land under
// the callee's row, not the caller's.
func (s *Store) GetEndpoints(ctx context.Context, from, to time.Time, service string, search string, limit int) ([]EndpointRow, error) {
	if limit <= 0 || limit > 5000 {
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
	args = append(args, limit)
	q := `
		SELECT service_name,
		       ` + pathExpr + ` AS path,
		       anyHeavy(http_method)                            AS method,
		       count()                                          AS calls,
		       countIf(status_code = 'error')                   AS errors,
		       countIf(status_code = 'error') / count() * 100   AS error_rate,
		       avg(duration) / 1e6                              AS avg_ms,
		       quantile(0.99)(duration) / 1e6                   AS p99_ms
		FROM spans
		WHERE time >= ? AND time <= ?
		  AND kind IN ('server', 'consumer')
		  AND ` + pathExpr + ` != ''` + whereSvc + whereSearch + `
		GROUP BY service_name, path
		ORDER BY calls DESC
		LIMIT ?
		SETTINGS max_execution_time = 15`
	rows, err := s.conn.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EndpointRow{}
	for rows.Next() {
		var r EndpointRow
		var avgMs, p99Ms, errRate *float64
		if err := rows.Scan(
			&r.Service, &r.Path, &r.Method,
			&r.Calls, &r.Errors, &errRate, &avgMs, &p99Ms,
		); err != nil {
			return nil, err
		}
		r.ErrorRate = safeF(errRate)
		r.AvgMs = safeF(avgMs)
		r.P99Ms = safeF(p99Ms)
		out = append(out, r)
	}
	return out, rows.Err()
}
