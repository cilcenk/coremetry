package chstore

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// v0.9.496 — bu dosyadaki HER okuma telemetryReadConn() üzerinden
// gider (RoundRobin havuzu), s.conn üzerinden değil. Dosya baştan sona
// telemetri okur: service_summary_5m, operation_summary_5m ve bir tane
// spans fallback'i — hepsi Distributed sarmalayıcı / MV, yani hangi
// node koordine ederse etsin cevap aynıdır. /api/services'i besleyen
// en sıcak yol burası (<50ms warm bütçesi), okuma havuzunun ilk dilimi
// olarak seçilme sebebi de bu.
//
// Buraya bir STATE tablosu okuması eklenirse (users, teams,
// system_settings…) havuzu DEĞİŞTİR: o tablolar her kurulumda
// replicate değil, RoundRobin her çağrıda başka node'un kopyasına
// düşer → v0.9.486'nın /users tutarsızlığı. conn_strategy_test.go
// dosya yüzeyini pinliyor.

// ServiceSummaryRow is one 5-minute bucket of pre-aggregated stats for a
// single service, sourced from the service_summary_5m materialized view.
// Use for time-bucketed reads that span hours/days — the MV merges
// AggregateFunction states cheaply at query time, no raw spans scan.
type ServiceSummaryRow struct {
	Service     string  `json:"service"`
	BucketStart int64   `json:"bucketStart"` // unix ns
	SpanCount   uint64  `json:"spanCount"`
	ErrorCount  uint64  `json:"errorCount"`
	AvgMs       float64 `json:"avgMs"`
	P50Ms       float64 `json:"p50Ms"`
	P95Ms       float64 `json:"p95Ms"`
	P99Ms       float64 `json:"p99Ms"`
}

// ListServiceNames is the lookup behind UI service-name pickers (traces,
// logs, services filter, alerts, SLOs, exceptions, ...).
//
// Reads DISTINCT service_name from the 5-minute MV. The MV stores one
// row per (service, 5min bucket) so DISTINCT is essentially "what
// services have we seen in the last 90 days" (= MV TTL) — exactly the
// set the pickers care about, and the read is cheap because the MV's
// ORDER BY (service_name, time_bucket) makes the distinct streamable.
//
// `pattern` accepts simple Lucene-style wildcards:
//   - bare text  → case-insensitive substring (LIKE '%text%')
//   - "*"        → multi-char wildcard
//   - "?"        → single-char wildcard
//
// SQL LIKE special chars in user input ('%', '_') are escaped first so
// they're matched literally rather than acting as inadvertent wildcards.
// ListOperationNames — operations-picker counterpart to
// ListServiceNames (v0.5.180). Reads operation_summary_5m so
// the GROUP BY is cheap even at billions of spans / tens of
// thousands of operations per service. Service filter is
// optional but recommended at scale — a global op listing on
// an install with 10k services × 100 ops/service is
// approaching the limits of "useful in a dropdown".
//
// Wildcard semantics match ListServiceNames: `*` and `?` map
// to CH `%` / `_`; bare strings are wrapped in `%…%` for
// substring match. Returns (names, total, err) so the UI can
// surface "showing 200 of 12,345 — refine" hints.
func (s *Store) ListOperationNames(ctx context.Context, service, pattern string, limit, offset int) ([]string, int, error) {
	if limit <= 0 {
		limit = 200
	}
	var wc whereClause
	if service != "" {
		wc.add("service_name = ?", service)
	}
	like := ""
	if pattern != "" {
		like = strings.NewReplacer(`*`, `%`, `?`, `_`).Replace(pattern)
		if !strings.ContainsAny(pattern, "*?") {
			like = "%" + like + "%"
		}
		wc.add("name ILIKE ?", like)
	}

	var total uint64
	if err := s.telemetryReadConn().QueryRow(ctx,
		"SELECT count(DISTINCT name) FROM operation_summary_5m "+wc.sql()+
			" SETTINGS max_execution_time = 25",
		wc.args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if total == 0 {
		// v0.8.234 — same MV-empty fallback as ListServiceNames (see the
		// comment there): degraded external-Distributed installs keep
		// their OperationPicker instead of an empty dropdown.
		return s.operationNamesFromSpans(ctx, service, like, limit, offset)
	}

	args := append(append([]any{}, wc.args...), limit, offset)
	rows, err := s.telemetryReadConn().Query(ctx,
		"SELECT DISTINCT name FROM operation_summary_5m "+wc.sql()+
			" ORDER BY name LIMIT ? OFFSET ?"+
			" SETTINGS max_execution_time = 25",
		args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, 0, err
		}
		out = append(out, n)
	}
	return out, int(total), rows.Err()
}

// ListActiveServiceNames returns the distinct service names seen in
// the last `window`, from the MV. v0.8.506 (perf raporu #3):
// evaluator (1dk tick) + anomaly detector (2dk tick) yalnız İSİM
// listesi için GetServices(24h)'ü — yani apdex+quantile'lı ham spans
// GROUP BY'ını (~3.7M satır/koşu lokalde) — çağırıyordu. MV'den
// DISTINCT, ORDER BY (service_name, ...) sayesinde read_in_order ile
// neredeyse bedava.
func (s *Store) ListActiveServiceNames(ctx context.Context, window time.Duration) ([]string, error) {
	rows, err := s.telemetryReadConn().Query(ctx, `
		SELECT DISTINCT service_name
		FROM service_summary_5m
		WHERE time_bucket >= now() - INTERVAL ? SECOND
		LIMIT 20000
		SETTINGS max_execution_time = 25`, int64(window.Seconds()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) ListServiceNames(ctx context.Context, pattern string, limit, offset int) ([]string, int, error) {
	if limit <= 0 {
		limit = 200
	}
	like := ""
	args := []any{}
	where := ""
	if pattern != "" {
		// Translate user pattern → ClickHouse ILIKE. Service names in
		// the wild are typically [a-zA-Z0-9._-]+ so the SQL wildcard
		// chars (%, _) almost never appear literally; we accept the
		// edge case rather than escape (CH doesn't support ESCAPE
		// on ILIKE anyway).
		like = strings.NewReplacer(`*`, `%`, `?`, `_`).Replace(pattern)
		// If the user didn't include any wildcards, default to a
		// substring match — that's what they expect when typing into a
		// picker, not an exact equality.
		if !strings.ContainsAny(pattern, "*?") {
			like = "%" + like + "%"
		}
		where = " WHERE service_name ILIKE ?"
		args = append(args, like)
	}

	var total uint64
	if err := s.telemetryReadConn().QueryRow(ctx,
		"SELECT count(DISTINCT service_name) FROM service_summary_5m"+where+
			" SETTINGS max_execution_time = 25",
		args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if total == 0 {
		// v0.8.234 — operator-reported (external Distributed test env,
		// degraded ALLOW_UNSET_CLUSTER mode): the summary MVs are empty BY
		// DESIGN there, but this lookup read ONLY the MV — so the /traces
		// ServicePicker listed nothing while raw spans held fresh data,
		// violating the v0.8.213 "raw-spans reads only" degraded contract.
		// Empty MV (also: first 5 minutes of a fresh install, MV-recreate
		// window) → bounded raw-spans fallback. Zero cost when the MV has
		// rows (this branch never runs).
		return s.serviceNamesFromSpans(ctx, like, limit, offset)
	}

	args = append(args, limit, offset)
	rows, err := s.telemetryReadConn().Query(ctx,
		"SELECT DISTINCT service_name FROM service_summary_5m"+where+
			" ORDER BY service_name LIMIT ? OFFSET ?"+
			" SETTINGS max_execution_time = 25",
		args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, 0, err
		}
		out = append(out, n)
	}
	return out, int(total), rows.Err()
}

// rawPickerSQL builds the bounded raw-spans fallback queries (count +
// page) for the name pickers. Pure so the CH hard-constraint bounds —
// time-bounded WHERE on the indexed prefix, LIMIT, max_execution_time —
// can't silently drop off a branch (v0.8.234 regression guard).
// `col` is the picked column; `extraWhere` is zero or more " AND …"
// clauses appended after the time bound. uniq() (approximate) for the
// count: the picker total is a "+N more" hint, and an exact
// count(DISTINCT) would be a second full pass over the window.
func rawPickerSQL(col, extraWhere string) (countQ, pageQ string) {
	base := " FROM spans WHERE time >= ?" + extraWhere
	countQ = "SELECT uniq(" + col + ")" + base +
		" SETTINGS max_execution_time = 10"
	pageQ = "SELECT " + col + base +
		" GROUP BY " + col +
		" ORDER BY " + col +
		" LIMIT ? OFFSET ?" +
		" SETTINGS max_execution_time = 10"
	return countQ, pageQ
}

// rawPickerWindow bounds the raw-spans picker fallback: "what can I
// filter by lately" is a 24h question, and the GROUP BY streams over
// the (service_name, …) primary-key prefix so the window mostly guards
// the pathological case.
const rawPickerWindow = 24 * time.Hour

// serviceNamesFromSpans is ListServiceNames' bounded raw-spans fallback
// (v0.8.234) — used only when service_summary_5m has no rows for the
// filter (degraded external-Distributed mode, fresh install's first
// bucket, MV-recreate window). `like` is the pre-built ILIKE argument
// ("" = no pattern filter).
func (s *Store) serviceNamesFromSpans(ctx context.Context, like string, limit, offset int) ([]string, int, error) {
	extra := ""
	args := []any{time.Now().Add(-rawPickerWindow)}
	if like != "" {
		extra = " AND service_name ILIKE ?"
		args = append(args, like)
	}
	countQ, pageQ := rawPickerSQL("service_name", extra)
	var total uint64
	if err := s.telemetryReadConn().QueryRow(ctx, countQ, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	pargs := append(append([]any{}, args...), limit, offset)
	rows, err := s.telemetryReadConn().Query(ctx, pageQ, pargs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, 0, err
		}
		out = append(out, n)
	}
	return out, int(total), rows.Err()
}

// operationNamesFromSpans is ListOperationNames' raw-spans sibling of
// serviceNamesFromSpans (v0.8.234). The optional service filter rides
// the (service_name, time) primary-key prefix, so even at billions of
// rows the scan is service-scoped.
func (s *Store) operationNamesFromSpans(ctx context.Context, service, like string, limit, offset int) ([]string, int, error) {
	extra := ""
	args := []any{time.Now().Add(-rawPickerWindow)}
	if service != "" {
		extra += " AND service_name = ?"
		args = append(args, service)
	}
	if like != "" {
		extra += " AND name ILIKE ?"
		args = append(args, like)
	}
	countQ, pageQ := rawPickerSQL("name", extra)
	var total uint64
	if err := s.telemetryReadConn().QueryRow(ctx, countQ, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	pargs := append(append([]any{}, args...), limit, offset)
	rows, err := s.telemetryReadConn().Query(ctx, pageQ, pargs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, 0, err
		}
		out = append(out, n)
	}
	return out, int(total), rows.Err()
}

// GetServicesAgg returns one aggregate row per service for the requested
// window, reading entirely from service_summary_5m. Replaces the raw-spans
// scan in GetServices for any window where the MV has data — orders of
// magnitude faster at scale (sub-second across 10s of thousands of
// services / billions of source spans).
//
// `limit` caps the result to the top-N services by span count; pass 0 to
// disable. Apdex is computed from the new countIfState columns; if the
// MV pre-dates the schema upgrade those columns are NULL → apdex = 0.
//
// 30-second hard execution timeout via SETTINGS — this endpoint must
// never hang the UI thread, even when the MV itself has a backlog.
func (s *Store) GetServicesAgg(ctx context.Context, from, to time.Time, limit int) ([]ServiceSummary, error) {
	return s.GetServicesAggFilteredIn(ctx, from, to, "", nil, "", "", limit, 0)
}

// GetServicesAggFiltered — preserves the prior surface (no
// service-name allowlist). New callers should use
// GetServicesAggFilteredIn directly.
func (s *Store) GetServicesAggFiltered(ctx context.Context, from, to time.Time, nameMatch, sort, dir string, limit, offset int) ([]ServiceSummary, error) {
	return s.GetServicesAggFilteredIn(ctx, from, to, nameMatch, nil, sort, dir, limit, offset)
}

// servicesAggSortExpr — alias for servicesSortExpr but using
// the column names produced by the MV-aggregation SELECT
// (`spans` / `errs` instead of `span_count` / `error_count`).
// Same whitelist; never interpolate the raw key.
func servicesAggSortExpr(sort, dir string) string {
	col := "spans"
	switch sort {
	case "name":
		col = "service_name"
	case "spans", "span_count", "spanCount":
		col = "spans"
	case "errorCount", "errors", "error_count":
		col = "errs"
	case "errorRate", "error_rate":
		col = "(errs / nullIf(spans, 0))"
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

// GetServicesAggFiltered narrows the row set by a substring match on
// service_name *before* the GROUP BY — used by the Services page
// dropdown so a service that's outside the limited top-N still
// surfaces when the user types its name. `nameMatch` empty disables
// the filter.
// GetServicesAggFilteredIn — same as GetServicesAggFiltered
// plus a service-name allowlist (the API uses this to
// pre-narrow the universe by ownerTeam / sreTeam without
// joining at query time). nil / empty = no constraint.
// CountServicesAgg returns the number of DISTINCT services matching the same
// MV-path filters as GetServicesAggFilteredIn — the denominator the Services
// page needs for First/Last paging. uniqExact over service_summary_5m is cheap
// (no per-service aggregation). Kept OPT-IN at the handler (?withTotal=1) so the
// default hot path stays count-free per the /api/services p99<50ms budget
// (v0.7.44).
func (s *Store) CountServicesAgg(ctx context.Context, from, to time.Time, nameMatch string, serviceIn []string) (int, error) {
	// v0.9.555 — MV kovaları başlangıçlarıyla etiketli; [from,to]
	// aralığını kapsamak için from kova başına inmeli, yoksa
	// baştaki kısmi kova tamamen elenir (bkz. alignBucketStart).
	from = alignBucketStart(from)
	if from.IsZero() {
		from = time.Now().Add(-24 * time.Hour)
	}
	if to.IsZero() {
		to = time.Now()
	}
	nameClause := ""
	args := []any{from, to}
	if nameMatch != "" {
		nameClause = " AND positionCaseInsensitive(service_name, ?) > 0"
		args = append(args, nameMatch)
	}
	if len(serviceIn) > 0 {
		holders := make([]string, len(serviceIn))
		for i, n := range serviceIn {
			holders[i] = "?"
			args = append(args, n)
		}
		nameClause += " AND service_name IN (" + strings.Join(holders, ",") + ")"
	}
	var n uint64
	err := s.telemetryReadConn().QueryRow(ctx, `
		SELECT toUInt64(uniqExact(service_name))
		FROM service_summary_5m
		WHERE time_bucket >= ? AND time_bucket < ?`+nameClause+`
		SETTINGS max_execution_time = 25`,
		args...).Scan(&n)
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

// mvQuantileMemSettings bounds the memory of summary-MV reads that merge
// the duration_q_state quantile state.
//
// History: v0.8.191 (operator-reported PRODUCTION code-241 OOM + code-159
// timeout on the external Distributed cluster) added `max_threads = 2` +
// `distributed_aggregation_memory_efficient = 1` as a band-aid while the
// MVs still stored 8192-sample reservoir quantilesState (~64 KiB/row —
// parallel per-granule read buffers of that state were the memory sink).
// v0.8.194 migrated every duration_q_state to quantilesTDigestState
// (~4.3 KiB/row, ~15x smaller, parallel-safe; verified full-parallelism
// read fits where the reservoir OOM'd).
//
// v0.8.233 — with TDigest in place the `max_threads = 2` throttle only
// slowed the hottest reads (/services agg + /service ops tables) with no
// remaining memory justification, so it's removed: CH's default read
// parallelism applies again. The streaming cross-shard merge stays — it
// bounds the initiator's peak on wide Distributed reads regardless of
// state size, and single-node installs ignore it.
const mvQuantileMemSettings = `distributed_aggregation_memory_efficient = 1`

func (s *Store) GetServicesAggFilteredIn(ctx context.Context, from, to time.Time, nameMatch string, serviceIn []string, sort, dir string, limit, offset int) ([]ServiceSummary, error) {
	return s.GetServicesAggFiltered2(ctx, from, to, nameMatch, serviceIn, sort, dir, limit, offset, ServiceDisplayFilters{})
}

// GetServicesAggFiltered2 is the MV fast path with the v0.9.345 display
// filters. Kept as a second entry point so the eight-argument original stays
// valid for its existing callers.
//
// The HAVING is built by the SAME method the raw path uses, with this query's
// aliases passed in — the two paths cannot drift into disagreeing about what
// "Errors only" means, which matters because the operator flips between them
// just by picking a cluster or an env.
func (s *Store) GetServicesAggFiltered2(ctx context.Context, from, to time.Time, nameMatch string, serviceIn []string, sort, dir string, limit, offset int, display ServiceDisplayFilters) ([]ServiceSummary, error) {
	// v0.9.555 — MV kovaları başlangıçlarıyla etiketli; [from,to]
	// aralığını kapsamak için from kova başına inmeli, yoksa
	// baştaki kısmi kova tamamen elenir (bkz. alignBucketStart).
	from = alignBucketStart(from)
	if from.IsZero() {
		from = time.Now().Add(-24 * time.Hour)
	}
	if to.IsZero() {
		to = time.Now()
	}
	limitClause := ""
	if limit > 0 {
		limitClause = fmt.Sprintf(" LIMIT %d OFFSET %d", limit, offset)
	}
	const apdexT = 200.0
	nameClause := ""
	args := []any{from, to}
	if nameMatch != "" {
		// Case-insensitive substring match — matches what the
		// service-names autocomplete does.
		nameClause = " AND positionCaseInsensitive(service_name, ?) > 0"
		args = append(args, nameMatch)
	}
	if len(serviceIn) > 0 {
		// IN-list against the allowlist. Splat each value as
		// its own placeholder so the driver binds them one
		// per `?` (the IN clause itself takes a parenthesised
		// list of literals).
		holders := make([]string, len(serviceIn))
		for i, n := range serviceIn {
			holders[i] = "?"
			args = append(args, n)
		}
		nameClause += " AND service_name IN (" + strings.Join(holders, ",") + ")"
	}
	// Display filters, rendered against THIS query's aliases (the MV names them
	// spans/errs, the raw path span_count/error_count). Args append last so
	// they follow the WHERE binds in placeholder order.
	aggHaving, havingArgs := display.having("spans", "errs", "p99_ms")
	args = append(args, havingArgs...)

	rows, err := s.telemetryReadConn().Query(ctx, `
		SELECT service_name,
		       countMerge(span_count_state)                                            AS spans,
		       countIfMerge(error_count_state)                                         AS errs,
		       sumMerge(duration_sum_state) / nullIf(spans, 0) / 1e6                   AS avg_ms,
		       arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 3) / 1e6 AS p99_ms,
		       (countIfMerge(apdex_satisfied_state) + countIfMerge(apdex_tolerating_state) / 2)
		         / nullIf(spans, 0)                                                     AS apdex
		FROM service_summary_5m
		WHERE time_bucket >= ? AND time_bucket < ?`+nameClause+`
		GROUP BY service_name`+aggHaving+`
		ORDER BY `+servicesAggSortExpr(sort, dir)+limitClause+`
		SETTINGS max_execution_time = 25, `+mvQuantileMemSettings,
		args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ServiceSummary{}
	for rows.Next() {
		var (
			sv    ServiceSummary
			avg   *float64
			p99   *float64
			apdex *float64
		)
		if err := rows.Scan(&sv.Name, &sv.SpanCount, &sv.ErrorCount, &avg, &p99, &apdex); err != nil {
			return nil, err
		}
		// v0.5.301 — same NaN-from-quantilesMerge guard as the
		// operations path; encoding/json rejects NaN+Inf so a
		// rogue float silently 500s the /services response.
		sv.AvgMs = safeF(avg)
		sv.P99Ms = safeF(p99)
		sv.Apdex = safeF(apdex)
		if sv.SpanCount > 0 {
			sv.ErrorRate = float64(sv.ErrorCount) / float64(sv.SpanCount) * 100
		}
		sv.ApdexThresholdMs = apdexT
		out = append(out, sv)
	}
	return out, rows.Err()
}

// GetServiceSummary5mFor reads MV buckets for a set of named services.
// Same shape as GetServiceSummary5m but accepts a list — used by the
// sparklines endpoint to scope the result to the visible top-N rows on
// the services page (otherwise the response is one array per service
// across all of them, which is multi-MB at high cardinality).
//
// Empty list returns ALL services (so an internal caller that genuinely
// wants the full set still has a path).
func (s *Store) GetServiceSummary5mFor(ctx context.Context, services []string, from, to time.Time) ([]ServiceSummaryRow, error) {
	// v0.9.555 — MV kovaları başlangıçlarıyla etiketli; [from,to]
	// aralığını kapsamak için from kova başına inmeli, yoksa
	// baştaki kısmi kova tamamen elenir (bkz. alignBucketStart).
	from = alignBucketStart(from)
	args := []any{from, to}
	svcFilter := ""
	if len(services) > 0 {
		// Use the IN(...) tuple form. clickhouse-go takes a slice and
		// binds it as an array; keeps the SQL parameterised (no
		// hand-quoted values).
		svcFilter = " AND service_name IN ?"
		args = append(args, services)
	}
	rows, err := s.telemetryReadConn().Query(ctx, `
		SELECT
		  service_name,
		  toUnixTimestamp64Nano(toDateTime64(time_bucket, 9)) AS bucket_ns,
		  countMerge(span_count_state)                      AS spans,
		  countIfMerge(error_count_state)                   AS errors,
		  sumMerge(duration_sum_state) / nullIf(countMerge(span_count_state), 0) / 1e6 AS avg_ms,
		  arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 1) / 1e6 AS p50_ms,
		  arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 2) / 1e6 AS p95_ms,
		  arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 3) / 1e6 AS p99_ms
		FROM service_summary_5m
		WHERE time_bucket >= ? AND time_bucket < ?`+svcFilter+`
		GROUP BY service_name, time_bucket
		ORDER BY service_name, time_bucket
		SETTINGS max_execution_time = 25`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ServiceSummaryRow{}
	for rows.Next() {
		var r ServiceSummaryRow
		if err := rows.Scan(&r.Service, &r.BucketStart, &r.SpanCount, &r.ErrorCount,
			&r.AvgMs, &r.P50Ms, &r.P95Ms, &r.P99Ms); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetServiceSummary5m reads pre-aggregated 5-minute buckets from the MV.
// Suitable for "show last N hours per-service trend" without paying the
// cost of scanning raw span rows. Buckets that haven't materialised yet
// (under 5 minutes old) will be missing — callers should overlay raw
// spans for the most recent window if they need second-fresh numbers.
// serviceSummarySlotsSQL — v0.10.269 (kuyruk 1, /perf-triage): sparkline
// okuması KOVA yerine SLOT bazında toplar. Ölçüm (lokal 2 shard, 101 servis,
// query_log): 5-dk satır başına üç quantilesTDigestMerge + arrayElement ile
// 24 s 8.6 s / 26.7k grup (CPU 0.7 s — süre shard→başlatıcı tDigest durum
// aktarımında), 7 g 25 s tavanında 500. Slot GROUP BY (tek merge, ≤120
// grup/servis): 24 s 2.4 s, 7 g 6.4 s. Slot indeksi intDiv ile epoch'a değil
// pencere başına (origin) hizalanır ki api/sparkline_slots.go grid'iyle
// bire bir örtüşsün (toStartOfInterval'ın origin parametresi 24.x+ ister,
// prod CH sürümü bilinmiyor). optimize_distributed_group_by_sharding_key
// BİLİNÇLİ YOK: ölçümde result_rows 26.722 → 26.802 (slot 9.042 → 9.095)
// büyüdü = bazı servis grupları iki shard'da da var, itilen GROUP BY
// kopya kısmi satır döndürür (yanlış sayı). SAF; summary_slots_test.go.
func serviceSummarySlotsSQL(svcFilter bool) string {
	f := ""
	if svcFilter {
		f = " AND service_name IN ?"
	}
	return `
		SELECT
		  service_name,
		  intDiv(toUInt64(toUnixTimestamp(time_bucket)) - ?, ?) AS slot_k,
		  countMerge(span_count_state)                      AS spans,
		  countIfMerge(error_count_state)                   AS errors,
		  sumMerge(duration_sum_state) / nullIf(countMerge(span_count_state), 0) / 1e6 AS avg_ms,
		  quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state) AS qs
		FROM service_summary_5m
		WHERE time_bucket >= ? AND time_bucket < ?` + f + `
		GROUP BY service_name, slot_k
		ORDER BY service_name, slot_k
		SETTINGS max_execution_time = 25`
}

// GetServiceSummarySlots — v0.10.269: servis başına slot (genişlik `slot`,
// 5 dk'nın katı) satırları; BucketStart = slot başlangıcı (origin + k·slot).
// Kuantiller slot içinde tDigest birleşimiyle (kova p99'larının maksimumu
// değil). services boşsa tüm servisler.
func (s *Store) GetServiceSummarySlots(ctx context.Context, services []string, from, to time.Time, slot time.Duration) ([]ServiceSummaryRow, error) {
	from = alignBucketStart(from)
	if slot < mvBucketWidth {
		slot = mvBucketWidth
	}
	originSec := uint64(from.Unix())
	widthSec := uint64(slot / time.Second)
	args := []any{originSec, widthSec, from, to}
	if len(services) > 0 {
		args = append(args, services)
	}
	rows, err := s.telemetryReadConn().Query(ctx, serviceSummarySlotsSQL(len(services) > 0), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ServiceSummaryRow{}
	for rows.Next() {
		var r ServiceSummaryRow
		// CH: UInt64 − UInt64 → Int64 (işaretli terfi); intDiv(Int64, UInt64)
		// Int64 döner. uint64'e tarama sürücüde tip hatasıyla düşer (ilk 269
		// deploy'unda 500). WHERE time_bucket >= from olduğundan k ≥ 0.
		var k int64
		var qs []float64
		if err := rows.Scan(&r.Service, &k, &r.SpanCount, &r.ErrorCount, &r.AvgMs, &qs); err != nil {
			return nil, err
		}
		if k < 0 {
			k = 0
		}
		r.BucketStart = from.UnixNano() + k*slot.Nanoseconds()
		if len(qs) == 3 {
			r.P50Ms, r.P95Ms, r.P99Ms = qs[0]/1e6, qs[1]/1e6, qs[2]/1e6
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) GetServiceSummary5m(ctx context.Context, service string, from, to time.Time) ([]ServiceSummaryRow, error) {
	// v0.9.555 — MV kovaları başlangıçlarıyla etiketli; [from,to]
	// aralığını kapsamak için from kova başına inmeli, yoksa
	// baştaki kısmi kova tamamen elenir (bkz. alignBucketStart).
	from = alignBucketStart(from)
	args := []any{from, to}
	svcFilter := ""
	if service != "" {
		svcFilter = " AND service_name = ?"
		args = append(args, service)
	}
	// time_bucket is plain DateTime (seconds precision — toStartOfInterval
	// strips sub-second precision regardless of input type), so explicitly
	// widen to DateTime64(9) before extracting nanoseconds. Otherwise CH
	// errors with "illegal type ... Expected: DateTime64, got: DateTime".
	rows, err := s.telemetryReadConn().Query(ctx, `
		SELECT
		  service_name,
		  toUnixTimestamp64Nano(toDateTime64(time_bucket, 9)) AS bucket_ns,
		  countMerge(span_count_state)                      AS spans,
		  countIfMerge(error_count_state)                   AS errors,
		  sumMerge(duration_sum_state) / nullIf(countMerge(span_count_state), 0) / 1e6 AS avg_ms,
		  arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 1) / 1e6 AS p50_ms,
		  arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 2) / 1e6 AS p95_ms,
		  arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 3) / 1e6 AS p99_ms
		FROM service_summary_5m
		WHERE time_bucket >= ? AND time_bucket < ?`+svcFilter+`
		GROUP BY service_name, time_bucket
		ORDER BY service_name, time_bucket`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ServiceSummaryRow
	for rows.Next() {
		var r ServiceSummaryRow
		if err := rows.Scan(&r.Service, &r.BucketStart, &r.SpanCount, &r.ErrorCount,
			&r.AvgMs, &r.P50Ms, &r.P95Ms, &r.P99Ms); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// mvBucketWidth — service_summary_5m ve kardeş MV'lerin kova genişliği.
// Sabit, MV tanımından geliyor (store.go CREATE MATERIALIZED VIEW).
const mvBucketWidth = 5 * time.Minute

// alignBucketStart — bir zamanı ait olduğu MV kovasının başına indirir
// (v0.9.555).
//
// Neden gerekli: MV kovaları BAŞLANGIÇLARIYLA etiketlidir. time_bucket
// = 10:00 olan satır 10:00–10:05 arasındaki veriyi taşır. Dolayısıyla
// [from, to] aralığını KAPSAMAK için sorgunun `time_bucket >=
// truncate(from)` demesi gerekir.
//
// Öncesi `time_bucket >= from` diyordu; bu "from'dan sonra BAŞLAYAN
// kovalar" demek ve baştaki kısmi kovayı tamamen eler. from=10:03 ise
// 10:00 kovası (yani 10:00–10:05 arası tüm veri) düşer.
//
// Zararı en çok olay incelemesinde: operatör pencereyi olayın başına
// ayarlar, olayın İLK DAKİKALARI görünmez olur ve yüzey "anomali yok"
// der — tam patlama anında. Kısa pencerelerde oran daha da kötü:
// 15 dakikalık pencerede bir kova kaybı verinin üçte biri demek.
//
// Repo'nun kendi doğru deseni zaten vardı (blast_radius.go:79,
// backtrace.go:133) ama summary.go'ya hiç uygulanmamıştı.
//
// Takas: hizalama, from'dan ÖNCEKİ birkaç dakikayı da dahil eder.
// Bu bilinçli — MV'nin greninden ince bir soruya dürüst cevap yok ve
// eksik göstermek fazla göstermekten kötü: biri olayı gizler, diğeri
// biraz komşu veri katar.
//
// ── ÜST SINIR (v0.9.1168) ──
// Alt sınırın simetriği DEĞİL: üst sınır `< to`, `<= to` değil. Yukarıdaki
// "fazla göstermek daha iyi" takası burada GEÇERSİZ, çünkü iki kova farklı
// şeyler taşıyor:
//
//	from'u İÇEREN kova   → pencere verisi + biraz komşu (almamak veri kaybı)
//	`to` ETİKETLİ kova   → [to, to+5dk), yani pencereden SIFIR veri
//
// `<= to` ikinci kovayı alır: kazanç yok, beş dakikalık tamamen yabancı
// trafik var. `< to` ise pencereden hiçbir şey kaybetmez — son alınan kova
// [to-5dk, to) ve tamamı pencere içinde. Hizalanmamış bir `to`'da (örn.
// 11:02) iki operatör AYNI sonucu verir, fark yalnız `to` tam kova
// sınırına otururken çıkar; sınıfın v0.9.823→1156→1167 boyunca dört kez
// yeniden bulunmasının sebebi bu — hatalı sınır çoğu pencerede doğru
// görünüyor.
//
// Kardeş okumalar aynı sözleşmede: anomaly.go / behavior.go (`<` zaten),
// dependencies.go + db_trends.go (v0.9.1156), dbqueries.go +
// dbstmt_detail.go (v0.9.1167). Kapı: summary_bucket_bound_test.go.
func alignBucketStart(t time.Time) time.Time {
	return t.Truncate(mvBucketWidth)
}
