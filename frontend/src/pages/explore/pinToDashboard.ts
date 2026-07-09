// pinToDashboard (v0.8.419, Data-Explorer parity DE4) — pure converter
// from an Explore builder query to a dashboard Panel. Dynatrace's
// "pin to dashboard" flow: the operator builds a chart in the
// explorer, pins it, and the dashboard re-renders it live from the
// SAME query semantics — no screenshot, no re-typing.
//
// The mapping is exact because dashboards already speak the two
// Explore source dialects: a catalogue-metric query becomes a
// `metric` panel (MetricPanelConfig) and a span query becomes a
// `spanmetric` panel (SpanMetricPanelConfig); both configs carry the
// same FilterExpr[] JSON the builder holds. Two honest refusals:
//   • formula panels (client-side expression — dashboards have no
//     formula engine) → not convertible (callers hide the pin).
//   • genuine OR/nested filter groups — panel configs carry flat
//     AND filters only; silently flattening would change the data.
import type { BuilderQuery } from './model';
import { queryDesc } from './model';
import type {
  FilterExpr, MetricPanelConfig, Panel, SpanMetricPanelConfig,
} from '@/lib/types';

function rid(): string {
  return Math.random().toString(36).slice(2, 10);
}

// isPinnable — cheap gate for the UI affordance (📌 visibility).
export function isPinnable(q: BuilderQuery): boolean {
  if (q.filterGroup) return false;                 // OR/nested — see header
  if (q.source === 'metric' && !q.metric) return false; // nothing picked yet
  return true;
}

// queryToPanel — builds a half-width dashboard Panel from a builder
// query, or null when not pinnable. step 0/undefined stays absent so
// the panel keeps the width-aware auto step (GRAN-C v0.8.248).
export function queryToPanel(
  q: BuilderQuery, opts?: { title?: string; step?: number },
): Panel | null {
  if (!isPinnable(q)) return null;
  const title = opts?.title?.trim() || queryDesc(q);
  const step = opts?.step && opts.step > 0 ? opts.step : undefined;
  const groupBy = q.splitBy.length ? q.splitBy.join(',') : undefined;

  if (q.source === 'metric') {
    const config: MetricPanelConfig = {
      metricName: q.metric,
      ...(q.scope ? { service: q.scope } : {}),
      ...(q.agg ? { agg: q.agg } : {}),
      ...(groupBy ? { groupBy } : {}),
      ...(step ? { step } : {}),
      ...(q.filters.length ? { filters: JSON.stringify(q.filters) } : {}),
    };
    return { id: rid(), type: 'metric', title, width: 2, config };
  }

  // Span query — the scope slot is a service.name pin in Explore's
  // compiler; fold it into the flat filter list the same way.
  const filters: FilterExpr[] = q.scope
    ? [{ k: 'service.name', op: '=', v: [q.scope] }, ...q.filters]
    : q.filters;
  const config: SpanMetricPanelConfig = {
    agg: q.agg,
    ...(q.metric && q.metric !== 'duration_ms' ? { field: q.metric } : {}),
    ...(groupBy ? { groupBy } : {}),
    ...(step ? { step } : {}),
    ...(q.dsl.trim() ? { dsl: q.dsl.trim() } : {}),
    ...(filters.length ? { filters: JSON.stringify(filters) } : {}),
  };
  return { id: rid(), type: 'spanmetric', title, width: 2, config };
}
