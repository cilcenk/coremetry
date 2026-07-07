package chstore

// red_series.go — the service RED-series composition (v0.8.333, pivot
// Phase 4). Lifted VERBATIM from internal/api/correlate.go's redSeries so
// the api layer (correlate bundle + /api/spans/window-metrics) and the MCP
// get_metrics_for_span tool share ONE implementation instead of duplicating
// the aggregation choices — package api isn't importable from mcptools
// (api → mcp wiring would cycle), so the shared piece lives at the store
// level where both already depend.

import (
	"context"
	"time"
)

// ServiceREDSeries fetches one service's three RED series (rate /
// error_rate / p99) over [from,to] via the SAME QuerySpanMetric path the
// live chart + RED panel + DQL hit — one cache + one MV story. Each series
// is grouped by service.name so the MV fast-path (service_summary_5m, step
// ≥ 5m) applies and the result is one line per metric. Soft-fails
// per-query: a missing series just drops out of the bundle. The GroupKey[0]
// is overwritten with the metric label so the three lines are
// self-describing without a side channel.
func (s *Store) ServiceREDSeries(ctx context.Context, service string, from, to time.Time) []SpanMetricSeries {
	svcFilter := []FilterExpr{{Key: "service.name", Op: "=", Values: []string{service}}}
	out := make([]SpanMetricSeries, 0, 3)
	add := func(label, agg, field string) {
		f := SpanMetricFilter{
			Aggregation: agg, Field: field, Filters: svcFilter, From: from, To: to,
			GroupBy: []string{"service.name"},
		}
		rows, err := s.QuerySpanMetric(ctx, f)
		if err != nil || len(rows) == 0 {
			return
		}
		// One series per metric; relabel GroupKey so the legend reads
		// rate / error_rate / p99 instead of the service name three times.
		ser := rows[0]
		ser.GroupKey = []string{label}
		out = append(out, ser)
	}
	add("rate", "rate", "")
	add("error_rate", "error_rate", "")
	add("p99", "p99", "duration_ms")
	return out
}
