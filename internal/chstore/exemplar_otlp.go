package chstore

// OTLP metric exemplars — v0.8.328, cross-signal pivot Phase 1a.
//
// This file owns the `exemplars` table's write + read paths. These are the
// REAL producer-recorded OTLP exemplars (trace context captured inside the
// measured operation); the span-derived exemplar states in exemplar.go
// (spanmetrics argMax rollups) stay — OTLP exemplars are additive truth,
// not a replacement (pivot-audit §1).
//
// Read shape by construction:
//   - ExemplarsForSeries: WHERE series_fingerprint IN (…) + timestamp window
//     = a pure primary-key scan on ORDER BY (series_fingerprint, timestamp).
//     This is THE metric→trace pivot query.
//   - ExemplarsForMetric: metric+service fallback for callers without a
//     fingerprint (legacy rows with fingerprint 0, or per-service rollup
//     charts). Granule scan bounded by the time window + LIMIT +
//     max_execution_time; a minmax skip index on service_name is deferred
//     until profiled (audit call).

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// OTLPExemplar is one exemplar row as served to the pivot read path.
type OTLPExemplar struct {
	Fingerprint uint64            `json:"fingerprint"`
	TimeUnixNs  int64             `json:"timeUnixNs"`
	Value       float64           `json:"value"`
	TraceID     string            `json:"traceId"`
	SpanID      string            `json:"spanId"`
	Attrs       map[string]string `json:"attrs,omitempty"`
}

const (
	// exemplarDefaultLimit — a chart shows at most a handful of ◆ glyphs per
	// bucket; 100 across the window is generous.
	exemplarDefaultLimit = 100
	// exemplarMaxLimit caps operator-supplied limits (bounded read, house rule).
	exemplarMaxLimit = 1000
)

// NOTE (v0.8.431, audit Faz A): the MCP tool get_exemplar_traces clamps
// tighter on purpose — default 20 / max 100 (mcptools/pivots.go) — because
// tool output feeds an LLM context window, not a chart. This HTTP-side
// clamp (default 100 / max 1000) is the chart budget. The divergence is
// intentional; keep both documented when changing either.
func clampExemplarLimit(limit int) int {
	if limit <= 0 {
		return exemplarDefaultLimit
	}
	if limit > exemplarMaxLimit {
		return exemplarMaxLimit
	}
	return limit
}

// InsertExemplars is the batched exemplar write — the flush function of the
// `exemplars` consumer (main.go), riding the same asyncInsertCtx coalescing
// as every other ingest INSERT (v0.5.346 settings, untouched).
func (s *Store) InsertExemplars(ctx context.Context, rows []*ExemplarRow) error {
	if len(rows) == 0 {
		return nil
	}
	ctx = asyncInsertCtx(ctx)
	// Named column list (v0.8.186 discipline): fails loudly on a stale
	// schema instead of writing into the wrong column.
	batch, err := s.conn.PrepareBatch(ctx, `INSERT INTO exemplars
		(series_fingerprint, metric_name, service_name, timestamp,
		 value, trace_id, span_id, filtered_attributes)`)
	if err != nil {
		return fmt.Errorf("prepare exemplars: %w", err)
	}
	for _, ex := range rows {
		attrs := ex.FilteredAttrs
		if attrs == nil {
			attrs = map[string]string{} // Map column rejects nil
		}
		if err := batch.Append(
			ex.Fingerprint, ex.Metric, ex.Service, ex.Time,
			ex.Value, ex.TraceID, ex.SpanID, attrs,
		); err != nil {
			return fmt.Errorf("append exemplar: %w", err)
		}
	}
	return batch.Send()
}

// Read SQL as package constants (v0.8.431, exemplar audit Faz A) so the
// SQL-shape tests can pin the house bounds — LIMIT + max_execution_time +
// time-bounded WHERE + the fingerprint IN predicate — without a live CH,
// same style as msgE2ESQL.
const exemplarsForSeriesSQL = `
		SELECT series_fingerprint, toUnixTimestamp64Nano(timestamp) AS ts,
		       value, trace_id, span_id, filtered_attributes
		FROM exemplars
		WHERE series_fingerprint IN (?)
		  AND timestamp >= ? AND timestamp <= ?
		ORDER BY timestamp
		LIMIT ?
		SETTINGS max_execution_time = 10`

const exemplarsForMetricSQLTmpl = `
		SELECT series_fingerprint, toUnixTimestamp64Nano(timestamp) AS ts,
		       value, trace_id, span_id, filtered_attributes
		FROM exemplars
		WHERE %s
		ORDER BY timestamp
		LIMIT ?
		SETTINGS max_execution_time = 10`

// ExemplarsForSeries is the canonical metric→trace pivot read: exemplars for
// a set of series fingerprints (one chart = the fingerprints of its plotted
// series) inside a time window. Primary-key scan by construction — see the
// file header. fingerprint 0 is the legacy-row sentinel and never a real
// identity; callers shouldn't pass it, and rows can't match it anyway
// (SeriesFingerprint of real inputs is never stored as 0 by the write path).
func (s *Store) ExemplarsForSeries(ctx context.Context, fingerprints []uint64, from, to time.Time, limit int) ([]OTLPExemplar, error) {
	if len(fingerprints) == 0 {
		return nil, nil
	}
	if from.IsZero() || to.IsZero() {
		return nil, fmt.Errorf("from/to are required")
	}
	rows, err := s.conn.Query(ctx, exemplarsForSeriesSQL,
		fingerprints, from, to, clampExemplarLimit(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanOTLPExemplars(rows)
}

// ExemplarsForMetric is the fingerprint-less fallback (legacy rows /
// service-level rollups): every exemplar of one metric on one service in the
// window, regardless of series. Granule scan — bounded by the window, the
// LIMIT and the execution cap.
func (s *Store) ExemplarsForMetric(ctx context.Context, metric, service string, from, to time.Time, limit int) ([]OTLPExemplar, error) {
	if strings.TrimSpace(metric) == "" {
		return nil, fmt.Errorf("metric is required")
	}
	if from.IsZero() || to.IsZero() {
		return nil, fmt.Errorf("from/to are required")
	}
	conds := []string{"metric_name = ?", "timestamp >= ?", "timestamp <= ?"}
	args := []any{metric, from, to}
	if svc := strings.TrimSpace(service); svc != "" {
		conds = append(conds, "service_name = ?")
		args = append(args, svc)
	}
	args = append(args, clampExemplarLimit(limit))
	rows, err := s.conn.Query(ctx, fmt.Sprintf(exemplarsForMetricSQLTmpl,
		strings.Join(conds, " AND ")), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanOTLPExemplars(rows)
}

// scanOTLPExemplars drains one exemplar result set (shared by both readers —
// identical projections).
func scanOTLPExemplars(rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}) ([]OTLPExemplar, error) {
	var out []OTLPExemplar
	for rows.Next() {
		var e OTLPExemplar
		var attrs map[string]string
		if err := rows.Scan(&e.Fingerprint, &e.TimeUnixNs, &e.Value,
			&e.TraceID, &e.SpanID, &attrs); err != nil {
			return nil, err
		}
		if len(attrs) > 0 {
			e.Attrs = attrs
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// metricSeriesFPSQLTmpl — gk → fingerprint-set lookup for one chart's
// series (v0.8.432, audit Faz B). %s slots: groupSelect, WHERE. The
// groupSelect and filters are built with the SAME helpers the chart
// query uses (groupKeyExpr / ApplyMetricFilters) so gk strings align
// with SpanMetricSeries.GroupKey byte-for-byte — the API joins the two
// on that key. groupUniqArray(8) caps per-series identity fan-out
// (instances collapsing into one display series).
const metricSeriesFPSQLTmpl = `
		SELECT %s AS gk, groupUniqArray(8)(series_fingerprint) AS fps
		FROM metric_points
		%s
		GROUP BY gk
		LIMIT 1000
		SETTINGS max_execution_time = 10`

// MetricSeriesFingerprints resolves a metric chart's series (as drawn:
// same groupBy, same filters, same window) to the series_fingerprint
// sets behind them — the missing server half that kept the
// /api/exemplars?fingerprints= PK-scan mode unused (audit Faz B).
// Returns nil on installs where the fingerprint column never reached
// the shards (hasSeriesFpCol=false, external-Distributed fallback) —
// callers degrade to no-◆, exactly today's behavior.
func (s *Store) MetricSeriesFingerprints(ctx context.Context, f MetricQueryFilter) (map[string][]uint64, error) {
	if !s.hasSeriesFpCol {
		return nil, nil
	}
	if f.Name == "" {
		return nil, fmt.Errorf("metric name required")
	}
	now := time.Now()
	if f.To.IsZero() {
		f.To = now
	}
	if f.From.IsZero() {
		f.From = f.To.Add(-24 * time.Hour)
	}

	var wc whereClause
	wc.add("metric = ?", f.Name)
	if f.Service != "" {
		wc.add("service_name = ?", f.Service)
	}
	wc.add("time >= ?", f.From)
	wc.add("time <= ?", f.To)
	// Legacy rows (pre-v0.8.328 or fallback ingests) carry the 0
	// sentinel — never a real identity, keep them out of the sets.
	wc.add("series_fingerprint != 0")
	ApplyMetricFilters(&wc, f.Filters)

	groupSelect := "[]::Array(String)"
	if len(f.GroupBy) > 0 {
		parts := make([]string, len(f.GroupBy))
		var groupArgs []any
		for i, k := range f.GroupBy {
			expr, args := groupKeyExpr(k, true)
			parts[i] = expr
			groupArgs = append(groupArgs, args...)
		}
		groupSelect = "[" + strings.Join(parts, ", ") + "]"
		wc.args = append(groupArgs, wc.args...)
	}

	rows, err := s.conn.Query(ctx,
		fmt.Sprintf(metricSeriesFPSQLTmpl, groupSelect, wc.sql()), wc.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string][]uint64)
	for rows.Next() {
		var gk []string
		var fps []uint64
		if err := rows.Scan(&gk, &fps); err != nil {
			return nil, err
		}
		out[strings.Join(gk, "|")] = fps
	}
	return out, rows.Err()
}
