// lib/resolverEligibility.ts — GRAN-D (v0.8.249): the spanmetrics-resolver
// eligibility contract, extracted from pages/explore/model.ts so non-Explore
// surfaces (ServiceCharts' RED family) can gate on it without importing one
// page's builder model from another page/component — the layering concern
// recorded with the 2026-06-15 doorway-resolver decision. model.ts re-exports
// these verbatim; Explore behaviour is unchanged.
//
// The resolver (/api/metrics/resolve, chstore/metricresolve.go) serves a
// MetricQuery off the spanmetrics 1s/10s/1m rollup tiers — far finer than the
// 5m summary MVs — but only for shapes the tiers can express: equality-only
// filters and group-bys on the five rollup dimensions, a rollup-served agg,
// and duration as the only measured field. Anything else must ride the raw
// spanMetric path.

import { metricQuery, type MetricQuery } from './metricQuery';

// Mirror of chstore tierDimColumn's accepted keys (both OTel and column
// spellings). A filter or split on anything else can't ride the tiers.
export const TIER_DIM_KEYS = new Set([
  'service.name', 'service_name',
  'name', 'operation',
  'kind', 'span.kind',
  'status', 'status_code',
  'http.route', 'http_route',
]);

// Aggs spanmetricStateAgg can serve (no p999/min/max/last on the rollups).
// Historically named for the exemplar (◆ glyph) path — eligibility for
// exemplars and for tier-served series is the same server-side gate.
export const EXEMPLAR_AGGS = new Set([
  'count', 'rate', 'per_min', 'errors', 'error_rate', 'apdex', 'avg', 'sum', 'p50', 'p90', 'p95', 'p99',
]);

// The spanmetrics names the rollup tiers materialise. Unknown metrics fail
// eligible-checks CLOSED (→ caller's legacy path), never open.
const TIER_METRICS = new Set(['calls_total', 'duration_milliseconds_bucket']);

// isResolverEligible — would the resolver's planner accept this descriptor?
// Complements model.ts exemplarDescriptor (which builds a descriptor FROM a
// BuilderQuery, applying the same rules); this checks an already-built
// MetricQuery, for surfaces that carry descriptors natively (doorway panels).
export function isResolverEligible(mq: MetricQuery): boolean {
  if (mq.source !== 'spanmetrics') return false;
  if (!TIER_METRICS.has(mq.metric)) return false;
  if (!EXEMPLAR_AGGS.has(mq.agg)) return false;
  for (const k of Object.keys(mq.filters ?? {})) if (!TIER_DIM_KEYS.has(k)) return false;
  for (const k of mq.groupBy ?? []) if (!TIER_DIM_KEYS.has(k)) return false;
  return true;
}

// serviceRedDescriptors — the service-detail RED triple (throughput / error
// rate / p99 latency, split by operation) as resolver descriptors. These are
// the SAME objects ServiceCharts hands its doorway ⋮ menus, and since GRAN-D
// also what it fetches: service.name is a tier dim, 'name' is a tier dim,
// and all three aggs are rollup-served — eligible by construction (pinned by
// resolverEligibility.test.ts so a descriptor edit can't silently knock the
// charts back onto the 5m path).
export function serviceRedDescriptors(service: string): {
  rps: MetricQuery; err: MetricQuery; p99: MetricQuery;
} {
  const filters = { 'service.name': service };
  const groupBy = ['name'];
  return {
    rps: metricQuery({ metric: 'calls_total', agg: 'rate', unit: 'rps', filters, groupBy }),
    err: metricQuery({ metric: 'calls_total', agg: 'error_rate', unit: '%', filters, groupBy }),
    p99: metricQuery({ metric: 'duration_milliseconds_bucket', agg: 'p99', unit: 'ms', filters, groupBy }),
  };
}
