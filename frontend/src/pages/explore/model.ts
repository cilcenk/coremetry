// pages/explore/model.ts — the Explore v2 multi-query builder state model.
//
// Phase-2 (explore-v2): BuilderState is what rides the URL as ?q= (see
// urlCodec.ts) and what the panel stack + group table render from. It is
// the MQE A–D + formula model (components/viz/MetricQueryEditor.tsx)
// ported onto span signals + catalogue metrics, per the 2026-06-10 plan.
//
// Pure types + helpers only — no React, no fetch — so urlCodec and
// formulaSeries stay unit-testable without the chart bundle.

import type { FilterExpr, SpanAgg } from '@/lib/types';

// Per-query source: 'span' aggregates the spans table via api.spanMetric
// (rate / error_rate / percentiles over duration_ms or any numeric attr);
// 'metric' reads a catalogue metric via api.metricQuery.
export type QuerySource = 'span' | 'metric';

// Phase-2 viz set — line/area/bars render on TimeSeriesPanel; heatmap keeps
// the existing LatencyHeatmap path (driven by query A). toplist / stat /
// table land in Phase 4.
export type ExploreViz = 'line' | 'area' | 'bars' | 'heatmap';
export const EXPLORE_VIZ: ExploreViz[] = ['line', 'area', 'bars', 'heatmap'];

// Aggregations differ per source (plan ground-truth #10): the metric query
// API supports avg|sum|min|max|last|p50|p95|p99; span signals add
// rate / count / errors / error_rate and the wider percentile set.
export type MetricCatalogAgg = 'avg' | 'sum' | 'min' | 'max' | 'last' | 'p50' | 'p95' | 'p99';
export const METRIC_CATALOG_AGGS: MetricCatalogAgg[] = ['avg', 'sum', 'min', 'max', 'last', 'p50', 'p95', 'p99'];

export interface BuilderQuery {
  letter: string;          // 'A'..'D' — stable id the formula references
  source: QuerySource;
  enabled: boolean;
  // span source: the measured numeric field ('duration_ms' default; '' for
  // count-shaped aggs). metric source: the catalogue metric name.
  metric: string;
  unit: string;            // metric source: MetricInfo.unit; span source: derived from agg
  agg: string;             // SpanAgg (span source) | MetricCatalogAgg (metric source)
  scope: string;           // service.name pin ('' = all) — synthesized into a filter at fetch
  splitBy: string[];       // group-by keys → series fan-out
  filters: FilterExpr[];   // AND-ed attribute filters
  dsl: string;             // advanced span DSL (legacy decode surface; AND-joined with filters)
}

export interface BuilderState {
  queries: BuilderQuery[];
  formula: string;         // '' = none. Expression over letters, e.g. "A / B * 100"
  viz: ExploreViz;
  step: number;            // seconds; 0 = auto. GLOBAL so formula buckets stay aligned.
}

export const MAX_QUERIES = 4;
export const QUERY_LETTERS = ['A', 'B', 'C', 'D'];

// Per-panel client-side series cap (plan perf guard: 4 panels × ≤10 series
// stays inside the uPlot budget). Biggest-by-area series win.
export const PANEL_SERIES_CAP = 10;

export function blankQuery(letter: string, source: QuerySource = 'span'): BuilderQuery {
  return {
    letter, source, enabled: true,
    metric: source === 'span' ? 'duration_ms' : '',
    unit: '', agg: source === 'span' ? 'count' : 'avg',
    scope: '', splitBy: [], filters: [], dsl: '',
  };
}

export function defaultBuilderState(): BuilderState {
  return { queries: [blankQuery('A')], formula: '', viz: 'line', step: 0 };
}

export function nextLetter(queries: BuilderQuery[]): string | null {
  const used = new Set(queries.map(q => q.letter));
  for (const l of QUERY_LETTERS) if (!used.has(l)) return l;
  return null;
}

// spanNeedsField — latency-style span aggs measure a field; count-style
// don't (mirrors presets.needsField, kept here so model.ts stays leaf).
export function spanNeedsField(agg: string): boolean {
  return !['count', 'rate', 'errors', 'error_rate'].includes(agg);
}

// spanAggUnit — the y-unit a span aggregation produces (matches
// presets.AGG_OPTIONS). Metric-source queries carry MetricInfo.unit instead.
export function spanAggUnit(agg: string): string {
  if (agg === 'rate') return '/s';
  if (agg === 'error_rate') return '%';
  if (['avg', 'p50', 'p90', 'p95', 'p99', 'p999', 'min', 'max', 'sum'].includes(agg)) return 'ms';
  return '';
}

// produces — does this query yield series? Span queries always can (count of
// all spans is a valid signal); metric queries need a picked metric.
export function produces(q: BuilderQuery): boolean {
  return q.enabled && (q.source === 'span' || !!q.metric);
}

// effectiveFilters — the filter set actually sent to the backend: the scope
// pin synthesized as a service.name chip + the explicit chips. The scope chip
// is byte-identical to what the legacy single-query workspace sent, so cache
// keys and results line up with pre-v2 behaviour.
export function effectiveFilters(q: BuilderQuery): FilterExpr[] {
  const scoped: FilterExpr[] = q.scope
    ? [{ k: 'service.name', op: '=', v: [q.scope] }]
    : [];
  return [...scoped, ...q.filters];
}

// querySignature — stable serialization of every fetch-relevant input, used
// as the react-query cache key component (lib/queries/keys.ts explore.query).
// Letter intentionally EXCLUDED: two letters with identical inputs share one
// fetch.
export function querySignature(q: BuilderQuery, step: number): string {
  return JSON.stringify({
    s: q.source, m: q.metric, a: q.agg, sc: q.scope,
    by: q.splitBy, f: q.filters, d: q.dsl, st: step,
  });
}

// queryUnit — resolved display unit for a query's series.
export function queryUnit(q: BuilderQuery): string {
  return q.source === 'span' ? spanAggUnit(q.agg) : q.unit;
}

// queryDesc — one-line human summary ("p95 of duration_ms by service.name").
// Drives the panel header + the recent-queries history label.
export function queryDesc(q: BuilderQuery): string {
  const what = q.source === 'span'
    ? (spanNeedsField(q.agg) ? `${q.agg} of ${q.metric || 'duration_ms'}` : q.agg)
    : `${q.agg}(${q.metric || '?'})`;
  const scope = q.scope ? ` · ${q.scope}` : '';
  const split = q.splitBy.length ? ` by ${q.splitBy.join(', ')}` : '';
  return `${what}${scope}${split}`;
}

// builderDesc — history-ring label for a whole builder state. Stable for the
// same state so re-runs bump in the ring instead of duplicating.
export function builderDesc(s: BuilderState): string {
  const parts = s.queries.filter(produces).map(q => `${q.letter}: ${queryDesc(q)}`);
  if (s.formula.trim()) parts.push(`ƒ=${s.formula.trim()}`);
  return `${parts.join(' · ') || 'empty'} · ${s.viz}`;
}

// SpanAgg type re-export convenience for consumers narrowing span aggs.
export type { SpanAgg };
