// panelToExplore (v0.9.773) — the INVERSE of pinToDashboard.queryToPanel.
//
// Pinning an Explore query onto a dashboard has existed since v0.8.419; the
// return trip did not. An operator staring at a dashboard panel that just
// went bad had to retype the query in Explore to break it down — the exact
// re-typing the pin flow exists to abolish, only in the other direction.
//
// Two rules make this honest rather than approximate:
//
//  1. VARIABLES EXPAND FIRST. A dashboard panel's config can reference
//     ${service}; Explore has no variables. So the href is built from the
//     config the panel is CURRENTLY rendering (applyVarsToMetric /
//     applyVarsToSpan — the same functions PanelRenderer and the data bundle
//     use), not from the stored template. Reusing those keeps the empty-value
//     contract too: an unset variable drops its predicate line instead of
//     resolving to `service.name = ""`.
//
//  2. NULL IS A REAL ANSWER. Only the two builder-expressible panel types
//     round-trip. A stat/gauge/markdown/row/heatmap panel, and a metric panel
//     driven by raw PromQL (BuilderQuery has no promql slot — model.ts), have
//     no faithful builder form. Returning null hides the menu item; silently
//     opening a DIFFERENT query would be worse than not offering the door.
import type { TimeRange, MetricPanelConfig, Panel, SpanMetricPanelConfig } from '@/lib/types';
import { decodeFilters, encodeRange } from '@/lib/urlState';
import { applyVarsToMetric, applyVarsToSpan } from '@/components/dashboard/PanelRenderer';
import { blankQuery, type BuilderQuery, type BuilderState } from './model';
import { encodeBuilder, extractScope } from './urlCodec';

// splitKeys — panel configs carry group-by as a comma-separated string; the
// builder carries string[]. Mirrors the split the dashboard data bundle does
// (Dashboard.tsx) so an empty/blank string becomes [] rather than [''].
function splitKeys(s: string | undefined): string[] {
  return (s ?? '').split(',').map(k => k.trim()).filter(Boolean);
}

export function panelToExploreHref(
  panel: Panel,
  vars: Record<string, string> | undefined,
  range: TimeRange,
): string | null {
  const state = panelToBuilder(panel, vars);
  if (!state) return null;
  return `/explore?q=${encodeURIComponent(encodeBuilder(state))}&range=${encodeRange(range)}`;
}

// panelToBuilder — the conversion proper; exported for tests so the round-trip
// against queryToPanel can be asserted on the state, not on a URL string.
export function panelToBuilder(
  panel: Panel,
  vars: Record<string, string> | undefined,
): BuilderState | null {
  if (panel.type === 'metric') {
    const cfg = applyVarsToMetric(panel.config as MetricPanelConfig, vars);
    // PromQL-driven metric panels (v0.9.121) bypass the metricName/agg/groupBy
    // builder entirely and BuilderQuery has nowhere to put the expression.
    if (cfg.promql) return null;
    const q: BuilderQuery = {
      ...blankQuery('A', 'metric'),
      metric: cfg.metricName,
      scope: cfg.service ?? '',
      agg: cfg.agg ?? 'avg',
      splitBy: splitKeys(cfg.groupBy),
      filters: decodeFilters(cfg.filters),
    };
    return { queries: [q], formula: '', viz: 'line', step: cfg.step ?? 0 };
  }

  if (panel.type === 'spanmetric') {
    const cfg = applyVarsToSpan(panel.config as SpanMetricPanelConfig, vars);
    // queryToPanel folded the builder's scope pin INTO the flat filter list
    // (a service.name = chip); lift it back out so Explore opens with the
    // service in its scope slot instead of as a redundant chip. Shared with
    // the legacy URL decoder — one definition of "a service pin".
    const { scope, rest } = extractScope(decodeFilters(cfg.filters));
    const q: BuilderQuery = {
      ...blankQuery('A', 'span'),
      metric: cfg.field ?? 'duration_ms',
      agg: cfg.agg,
      scope,
      splitBy: splitKeys(cfg.groupBy),
      filters: rest,
      dsl: cfg.dsl ?? '',
    };
    return { queries: [q], formula: '', viz: 'line', step: cfg.step ?? 0 };
  }

  // stat / gauge / markdown / row / heatmap / promql — no builder form.
  return null;
}
