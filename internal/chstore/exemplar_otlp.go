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
