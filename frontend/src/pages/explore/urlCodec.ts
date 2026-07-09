// pages/explore/urlCodec.ts — BuilderState ⇄ URL.
//
// Phase-2 (explore-v2). Two surfaces:
//
//  1. ?q= — the canonical compact-JSON codec for the v2 builder (the MQE
//     ?mq= precedent). encodeBuilder/decodeBuilder round-trip losslessly.
//
//  2. seedFromLegacyParams — the PERMANENT decode surface for every legacy
//     /explore param shape (plan: SavedViews store old shapes; inbound links
//     from Services / DependenciesTable / metricExploreHref / question cards
//     must keep working forever). Decode-only: the State→URL writer always
//     emits ?q=.
//
// Pure module (no React) — table-driven tests in urlCodec.test.ts exercise
// every legacy shape.

import { decodeFilters, isFlatAndGroup } from '@/lib/urlState';
import { decodeMetricQuery, type MetricQuery } from '@/lib/metricQuery';
import type { FilterExpr, FilterGroup } from '@/lib/types';
import {
  type BuilderState, type BuilderQuery, type ExploreViz, type QuerySource,
  EXPLORE_VIZ, QUERY_LETTERS, MAX_QUERIES, blankQuery, spanNeedsField,
} from './model';

// ── ?q= codec ───────────────────────────────────────────────────────────────
// Compact keys, defaults omitted, so the URL stays scannable:
//   { v: viz, s: step, f: formula, q: [{l, sr, e, m, u, a, sc, by, fl, d}] }

export function encodeBuilder(st: BuilderState): string {
  return JSON.stringify({
    ...(st.viz !== 'line' ? { v: st.viz } : {}),
    ...(st.step ? { s: st.step } : {}),
    ...(st.topN ? { n: st.topN } : {}),
    ...(st.logY ? { ly: 1 } : {}),
    ...(st.formula.trim() ? { f: st.formula.trim() } : {}),
    q: st.queries.map(q => ({
      l: q.letter,
      ...(q.source === 'metric' ? { sr: 'm' } : {}),
      ...(q.enabled ? {} : { e: 0 }),
      ...(q.metric && q.metric !== (q.source === 'span' ? 'duration_ms' : '') ? { m: q.metric } : {}),
      ...(q.unit ? { u: q.unit } : {}),
      ...(q.agg !== (q.source === 'span' ? 'count' : 'avg') ? { a: q.agg } : {}),
      ...(q.scope ? { sc: q.scope } : {}),
      ...(q.splitBy.length ? { by: q.splitBy } : {}),
      ...(q.filters.length ? { fl: q.filters } : {}),
      // fg — a GENUINE OR / nested filter group only (gap-2 → Explore). A
      // flat-AND / absent group is omitted, so an existing URL with only flat
      // chips is byte-identical to its pre-group form.
      ...(!isFlatAndGroup(q.filterGroup) ? { fg: q.filterGroup } : {}),
      ...(q.dsl.trim() ? { d: q.dsl } : {}),
    })),
  });
}

// metricCatalogueHref — canonical /explore?q= deep-link for a single
// catalogue metric (Phase 5 /metrics collapse). The Metrics catalogue rows,
// DependenciesTable and ServiceInfra all emit THIS so a metric inspection
// opens as a real builder query A (source=metric) instead of the retired
// source=metrics panel. agg should be the classifyMetric default so the
// initial chart is right (e.g. p99 for a histogram) — the bare ?metric=
// legacy branch can't carry it.
export function metricCatalogueHref(
  name: string, opts?: { service?: string; agg?: string },
): string {
  const q: BuilderQuery = {
    ...blankQuery('A', 'metric'),
    metric: name,
    agg: opts?.agg || 'avg',
    scope: opts?.service || '',
  };
  const state: BuilderState = { queries: [q], formula: '', viz: 'line', step: 0 };
  return `/explore?q=${encodeURIComponent(encodeBuilder(state))}`;
}

// decodeFgInline — narrow an already-parsed ?q= `fg` value into a FilterGroup.
// Mirrors lib/urlState.decodeFilterGroup's shape guard but operates on the
// object the ?q= JSON already parsed (no second JSON.parse). Returns undefined
// for a malformed or flat-AND group so it never shadows the flat `fl` path.
function decodeFgInline(v: unknown): FilterGroup | undefined {
  if (!v || typeof v !== 'object') return undefined;
  const o = v as { join?: unknown; filters?: unknown; groups?: unknown };
  if (!Array.isArray(o.filters)) return undefined;
  const g: FilterGroup = {
    join: o.join === 'OR' ? 'OR' : 'AND',
    filters: o.filters as FilterExpr[],
  };
  if (Array.isArray(o.groups) && o.groups.length > 0) {
    g.groups = (o.groups as unknown[])
      .map(sub => {
        const so = sub as { join?: unknown; filters?: unknown };
        return {
          join: so.join === 'OR' ? 'OR' : 'AND',
          filters: Array.isArray(so.filters) ? (so.filters as FilterExpr[]) : [],
        } as FilterGroup;
      })
      .filter(sub => (sub.filters ?? []).length > 0);
  }
  // A flat-AND group is inert — drop it so the flat `fl` path stays the single
  // source of truth and the URL round-trips byte-identically.
  return isFlatAndGroup(g) ? undefined : g;
}

export function decodeBuilder(s: string | null | undefined): BuilderState | null {
  if (!s) return null;
  try {
    const o = JSON.parse(s);
    if (!o || !Array.isArray(o.q) || o.q.length === 0) return null;
    const queries: BuilderQuery[] = (o.q as Record<string, unknown>[])
      .slice(0, MAX_QUERIES)
      .map((q, i) => {
        const source: QuerySource = q.sr === 'm' ? 'metric' : 'span';
        const base = blankQuery(String(q.l ?? QUERY_LETTERS[i] ?? 'A'), source);
        return {
          ...base,
          enabled: q.e !== 0,
          metric: typeof q.m === 'string' ? q.m : base.metric,
          unit: typeof q.u === 'string' ? q.u : '',
          agg: typeof q.a === 'string' ? q.a : base.agg,
          scope: typeof q.sc === 'string' ? q.sc : '',
          splitBy: Array.isArray(q.by) ? (q.by as string[]).filter(x => typeof x === 'string') : [],
          filters: Array.isArray(q.fl) ? (q.fl as FilterExpr[]) : [],
          // fg — grouped AND/OR builder (gap-2 → Explore). Accept only a
          // well-formed group (object with a filters array); a flat-AND one is
          // dropped to undefined so it can't shadow the flat `fl` path.
          filterGroup: decodeFgInline(q.fg),
          dsl: typeof q.d === 'string' ? q.d : '',
        };
      });
    const viz: ExploreViz = EXPLORE_VIZ.includes(o.v as ExploreViz) ? (o.v as ExploreViz) : 'line';
    return {
      queries,
      formula: typeof o.f === 'string' ? o.f : '',
      viz,
      step: typeof o.s === 'number' && o.s > 0 ? o.s : 0,
      topN: typeof o.n === 'number' && o.n > 0 ? o.n : undefined,
      logY: o.ly === 1 || undefined,
    };
  } catch {
    return null;
  }
}

// ── Legacy decode surface ───────────────────────────────────────────────────

// vizFromLegacy — the old spans-workspace Viz set projected onto Phase-2 viz.
// topN/kpi never had a dedicated spans renderer (they drew the same line
// chart), so they map to line until toplist/stat land in Phase 4.
function vizFromLegacy(v: string | null): ExploreViz {
  if (v === 'bar') return 'bars';
  if (v === 'area') return 'area';
  if (v === 'heatmap') return 'heatmap';
  return 'line';
}

// extractScope — pull a single-value service pin out of a chip list into the
// builder's scope slot; the rest stay as chips. effectiveFilters() reverses
// this at fetch time, so the backend sees the identical filter set.
function extractScope(filters: FilterExpr[]): { scope: string; rest: FilterExpr[] } {
  const i = filters.findIndex(f =>
    (f.k === 'service.name' || f.k === 'resource.service.name') &&
    f.op === '=' && f.v.length === 1);
  if (i < 0) return { scope: '', rest: filters };
  return { scope: filters[i].v[0], rest: filters.filter((_, j) => j !== i) };
}

// seedFromMetricDescriptor — ?m= ("every metric is a doorway", Phase B/D).
// Descriptors are spanmetrics/tracemetrics-shaped, so they land on a
// span-source query A (the projection the pre-v2 workspace used).
function seedFromMetricDescriptor(mq: MetricQuery): BuilderState {
  const latencyAgg = ['avg', 'p50', 'p90', 'p95', 'p99'].includes(mq.agg);
  const chips: FilterExpr[] = Object.entries(mq.filters ?? {})
    .filter(([, v]) => v !== '' && v != null)
    .map(([k, v]) => ({ k, op: '=', v: [v] }));
  const { scope, rest } = extractScope(chips);
  const q: BuilderQuery = {
    ...blankQuery('A'),
    agg: mq.agg,
    metric: (latencyAgg || mq.metric.includes('duration')) ? 'duration_ms' : '',
    scope,
    filters: rest,
    splitBy: mq.groupBy ?? [],
  };
  const vizMap: Record<string, ExploreViz> = {
    line: 'line', area: 'area', bar: 'bars', heatmap: 'heatmap', stat: 'line', topN: 'line',
  };
  return {
    queries: [q],
    formula: '',
    viz: vizMap[mq.viz] ?? 'line',
    // parseInt parity with the pre-v2 seed ('30s' → 30; non-numeric → 0).
    step: mq.step ? (parseInt(mq.step, 10) || 0) : 0,
  };
}

// seedFromLegacyParams — returns a BuilderState when the URL is a
// builder-shaped query, null when another surface owns it (traces / repeats
// result modes, metrics / logs source panels, or no meaningful params).
export function seedFromLegacyParams(sp: URLSearchParams): BuilderState | null {
  // Canonical form wins.
  const q = decodeBuilder(sp.get('q'));
  if (q) return q;

  // Other surfaces own these shapes.
  const result = sp.get('result');
  if (result === 'traces' || result === 'repeats') return null;
  const source = sp.get('source');
  if (source === 'metrics' || source === 'logs') return null;

  // ?m= descriptor (metricExploreHref).
  const mq = decodeMetricQuery(sp.get('m'));
  if (mq) return seedFromMetricDescriptor(mq);

  // Bare legacy spans-workspace shape — any metric-mode param present?
  const has = (k: string) => sp.get(k) != null && sp.get(k) !== '';
  if (!['agg', 'field', 'groupBy', 'filters', 'dsl', 'viz', 'step', 'metric', 'topN', 'compare'].some(has)) {
    return null;
  }

  const { scope, rest } = extractScope(decodeFilters(sp.get('filters')));
  const dsl = sp.get('mode') === 'advanced' ? (sp.get('dsl') ?? '') : '';
  const splitBy = (sp.get('groupBy') ?? '').split(',').filter(Boolean);
  const step = parseInt(sp.get('step') ?? '0', 10) || 0;
  const legacyViz = sp.get('viz');

  // DependenciesTable drill: ?metric=<catalogue name>&result=metric — a
  // catalogue-metric query A (the legacy page silently dropped the param).
  const catalogueMetric = sp.get('metric') ?? '';
  if (catalogueMetric) {
    const mqQ: BuilderQuery = {
      ...blankQuery('A', 'metric'),
      metric: catalogueMetric, scope, filters: rest, splitBy, dsl: '',
    };
    return { queries: [mqQ], formula: '', viz: vizFromLegacy(legacyViz), step };
  }

  // D5 (locked): legacy viz=red → the 3-query seed A:rate B:error_rate C:p99.
  if (legacyViz === 'red') {
    const by = splitBy.length ? splitBy : ['service.name'];
    const mk = (letter: string, agg: string): BuilderQuery => ({
      ...blankQuery(letter),
      agg, metric: spanNeedsField(agg) ? 'duration_ms' : '',
      scope, filters: rest, splitBy: by, dsl,
    });
    return {
      queries: [mk('A', 'rate'), mk('B', 'error_rate'), mk('C', 'p99')],
      formula: '', viz: 'line', step,
    };
  }

  const agg = sp.get('agg') || 'count';
  const a: BuilderQuery = {
    ...blankQuery('A'),
    agg,
    metric: sp.get('field') || 'duration_ms',
    scope, filters: rest, splitBy, dsl,
  };
  return { queries: [a], formula: '', viz: vizFromLegacy(legacyViz), step };
}
