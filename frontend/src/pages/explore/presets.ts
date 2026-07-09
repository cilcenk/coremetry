// pages/explore — shared constants + types for the Explore workspace.
//
// Phase-1 mechanical extraction (explore-v2): these lived inline in
// Explore.tsx; pulled into a leaf module so the extracted result
// components (TracesResult / RepeatsResult) and QuestionCards can
// import them without a circular dependency back to Explore.tsx.
// Verbatim moves — no behaviour change.

import type { DataTableColumn } from '@/lib/dataTable';
import type { FilterExpr, SpanAgg, RepeatedSpanRow } from '@/lib/types';

export type ResultMode = 'metric' | 'traces' | 'repeats';
export type Source = 'spans' | 'metrics' | 'logs';
export type Viz = 'line' | 'bar' | 'topN' | 'kpi' | 'heatmap' | 'red';

// BubbleUpMode — chooses the (baseline, selection) predicate
// pair for the BubbleUp investigator.
//   off     — panel hidden, no fetch
//   errors  — selection = status_code='error'
//   slow1s  — selection = duration_ms > 1000
//   slow5s  — selection = duration_ms > 5000
//   custom  — selection = the last filter chip the user added;
//             everything before it is the baseline. Legacy
//             behaviour for power users who already know the
//             "stage 2 chips" trick.
export type BubbleUpMode = 'off' | 'errors' | 'slow1s' | 'slow5s' | 'custom';

export const AGG_OPTIONS: { v: SpanAgg; label: string; unit?: string }[] = [
  { v: 'count',      label: 'Count',           unit: '' },
  { v: 'rate',       label: 'Rate (per sec)',  unit: '/s' },
  { v: 'per_min',    label: 'Rate (per min)',  unit: '/min' },
  { v: 'errors',     label: 'Error count',     unit: '' },
  { v: 'error_rate', label: 'Error rate (%)',  unit: '%' },
  { v: 'apdex',      label: 'Apdex',           unit: '' },
  { v: 'avg',        label: 'Avg',             unit: 'ms' },
  { v: 'p50',        label: 'P50 (median)',    unit: 'ms' },
  { v: 'p90',        label: 'P90',             unit: 'ms' },
  { v: 'p95',        label: 'P95',             unit: 'ms' },
  { v: 'p99',        label: 'P99',             unit: 'ms' },
  { v: 'p999',       label: 'P99.9',           unit: 'ms' },
  // v0.8.411 — Tempo/Grafana-style duration band: p50/p90/p95/p99 in
  // ONE resolver call, exemplars riding the p99 line.
  { v: 'band',       label: 'Percentile band (p50-p99)', unit: 'ms' },
  { v: 'min',        label: 'Min',             unit: 'ms' },
  { v: 'max',        label: 'Max',             unit: 'ms' },
  { v: 'sum',        label: 'Sum',             unit: 'ms' },
];

export const SUGGESTED_GROUPBY = [
  'service.name', 'name', 'op_group', 'kind', 'status_code',
  'http.method', 'http.route', 'http.status_code',
  'db.system', 'rpc.method', 'peer.service',
  'resource.host.name', 'resource.deployment.environment',
];

// Quick-metric presets — one click swaps the (agg, field, viz)
// triplet to a common-use shape. Saves operators from "wait,
// which option gives me error rate per service" navigation.
// Each preset is the answer to one of the questions operators
// actually ask during triage. Dynatrace's metric picker pre-
// computes these as "key metrics"; we keep them lightweight
// (no separate column) and consistent with the existing
// builder + DSL flow.
export type MetricPreset = {
  key: string;
  label: string;
  hint: string;
  agg: SpanAgg;
  field: string;
  viz: Viz;
  // Optional split-by recommendation applied when picked from
  // an empty / single-key split state. Operator overrides freely.
  groupBy?: string[];
};
export const METRIC_PRESETS: MetricPreset[] = [
  { key: 'rps',     label: 'Requests / sec',   hint: 'Throughput (rate of all matching spans)',          agg: 'rate',       field: 'duration_ms', viz: 'line', groupBy: ['service.name'] },
  { key: 'errpct',  label: 'Error rate %',     hint: 'Percentage of spans with status_code = error',     agg: 'error_rate', field: 'duration_ms', viz: 'line', groupBy: ['service.name'] },
  { key: 'errcnt',  label: 'Errors / period',  hint: 'Absolute error count per bucket',                  agg: 'errors',     field: 'duration_ms', viz: 'bar',  groupBy: ['service.name'] },
  { key: 'p99',     label: 'P99 latency',      hint: 'Tail latency — slowest 1% per bucket',             agg: 'p99',        field: 'duration_ms', viz: 'line', groupBy: ['service.name'] },
  { key: 'p95',     label: 'P95 latency',      hint: 'Standard tail-latency SLO indicator',              agg: 'p95',        field: 'duration_ms', viz: 'line', groupBy: ['service.name'] },
  { key: 'avglat',  label: 'Avg latency',      hint: 'Mean duration — best for noisy quantile sets',     agg: 'avg',        field: 'duration_ms', viz: 'line', groupBy: ['service.name'] },
  { key: 'count',   label: 'Span count',       hint: 'Raw count per bucket, no normalisation',           agg: 'count',      field: 'duration_ms', viz: 'bar' },
  { key: 'heatmap', label: 'Latency heatmap',  hint: 'Honeycomb-style 2D density (time × log-duration)', agg: 'count',      field: 'duration_ms', viz: 'heatmap' },
  // v0.5.260 — Uptrace-style "group by operation signature".
  // Splits by (service.name, name) together so every distinct
  // service+operation pair gets its own line. Pairs with the
  // RED viz so the operator sees rate / errors / p99 broken
  // down per operation in one click — Uptrace's `group by
  // _group_id` killer view, native to Coremetry.
  { key: 'red-op',  label: 'RED by operation', hint: 'Rate + errors + p99 stacked, broken down by (service, operation)', agg: 'rate', field: 'duration_ms', viz: 'red',  groupBy: ['service.name', 'name'] },
];

// REPEAT_PRESETS — one-click pick of (groupBy, minRepeats) that
// turn the Repeats mode into a question. "SQL N+1" groups by
// db.statement at ≥5 (typical ORM offender). "Chatty RPC"
// groups by name+peer.service at ≥3 (matches the user's
// example: 3 gRPC calls to the same operation in one trace
// surface as a row). "Endpoint fan-out" groups by http.route
// at ≥5 (a service hammering its own endpoint).
export type RepeatPreset = {
  key: string;
  label: string;
  hint: string;
  groupBy: string[];
  minRepeats: number;
  // Optional filter pins added to the chip list when the preset
  // fires. "Chatty RPC" sets kind=client so we count caller-side
  // outbound spans only — otherwise each duplication double-
  // counts (3 caller client spans + 3 callee server spans → two
  // rows for the same root issue). Filters are AND-merged with
  // whatever the operator already has.
  filters?: FilterExpr[];
};
export const REPEAT_PRESETS: RepeatPreset[] = [
  { key: 'rpc',     label: 'Chatty RPC',
    hint: '≥ 3 client-side calls with the same (name, peer.service) — repeated outbound chatter (e.g. api-gateway calling order-service.getOrder 3× in one trace)',
    groupBy: ['name', 'peer.service'], minRepeats: 3,
    filters: [{ k: 'kind', op: '=', v: ['client'] }] },
  { key: 'sql',     label: 'SQL N+1',
    hint: '≥ 5 spans with the same db.statement inside one trace — classic ORM N+1',
    groupBy: ['db.statement'], minRepeats: 5 },
  { key: 'route',   label: 'Endpoint fan-out',
    hint: '≥ 5 spans on the same http.route inside one trace — endpoint hammering itself',
    groupBy: ['http.route'], minRepeats: 5 },
  { key: 'op',      label: 'Same operation',
    hint: '≥ 3 spans with the same name (operation) inside one trace — repeated work regardless of target',
    groupBy: ['name'], minRepeats: 3 },
];

// Top-N split-by — when split is set, cap the chart to the busiest N
// series by total count. Anything past N is silently dropped client-
// side. Prevents the chart from drowning under 200 services on a
// "split by service.name" with a fresh deploy. Default 10.
export const TOPN_OPTIONS = [5, 10, 20, 50];

// v0.5.259 — sub-10s steps. See Metrics.tsx for the rationale.
export const STEP_OPTIONS = [
  { v: 0,    label: 'Auto' },
  { v: 1,    label: '1 s' },
  { v: 5,    label: '5 s' },
  { v: 10,   label: '10 s' },
  { v: 30,   label: '30 s' },
  { v: 60,   label: '1 min' },
  { v: 300,  label: '5 min' },
  { v: 1800, label: '30 min' },
];

// Per-series summary row — one line of the split-by metric
// breakdown table (Series / Last / Avg / Max / Buckets).
export type SummaryRow = { key: string[]; count: number; last: number; max: number; avg: number };

// SUMMARY_COLS — column defs for the per-series summary table,
// adopted onto the shared sortable+resizable primitive (v0.7.53).
// Default sort is Max desc so the heaviest series surfaces first,
// matching the chart's Top-N intent. The Series text column is
// ascending-natural; the four numeric columns right-align via
// numeric:true. Body-cell order below must match this order.
export const SUMMARY_COLS: DataTableColumn<SummaryRow>[] = [
  { id: 'series',  label: 'Series',  sortValue: r => r.key.join(' / '), naturalDir: 'asc', width: 280 },
  { id: 'last',    label: 'Last',    sortValue: r => r.last,  numeric: true, width: 130 },
  { id: 'avg',     label: 'Avg',     sortValue: r => r.avg,   numeric: true, width: 130 },
  { id: 'max',     label: 'Max',     sortValue: r => r.max,   numeric: true, width: 130 },
  { id: 'buckets', label: 'Buckets', sortValue: r => r.count, numeric: true, width: 110 },
];

// REPEATS_COLS — column defs for the N+1 / fan-out finder table.
// The backend already returns heaviest-first (by repeat count);
// the consumer preserves that as the initial client sort (count
// desc). The "Repeated shape" column label is dynamic (tracks the
// active split-by) so the column set is built per-render inside the
// component via this helper.
export function repeatCols(groupBy: string[]): DataTableColumn<RepeatedSpanRow>[] {
  return [
    { id: 'trace',   label: 'Trace',   sortValue: r => r.traceId, naturalDir: 'asc', width: 130 },
    { id: 'service', label: 'Service · root', sortValue: r => `${r.service}${r.rootName ? ' · ' + r.rootName : ''}`, naturalDir: 'asc', width: 240 },
    { id: 'shape',   label: `Repeated shape (${groupBy.length ? groupBy.join(' + ') : 'db.statement'})`, sortValue: r => (r.groupValues ?? []).join(' · '), naturalDir: 'asc', width: 360 },
    { id: 'count',   label: 'Repeats',    sortValue: r => r.count,           numeric: true, width: 100 },
    { id: 'total',   label: 'Total time', sortValue: r => r.totalDurationMs, numeric: true, width: 120 },
    { id: 'started', label: 'Started',    sortValue: r => r.startedAt,                      width: 180 },
  ];
}

// needsField — latency-style aggs measure a field (duration_ms),
// count-style aggs don't. Drives the "of <field>" picker visibility.
export function needsField(agg: SpanAgg): boolean {
  return !['count', 'rate', 'errors', 'error_rate'].includes(agg);
}
