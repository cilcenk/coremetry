// pages/explore/useExploreQueries.ts — the builder's data layer.
//
// Phase-2 (explore-v2) + D5. One react-query fetch per producing query:
//   span source, resolver-eligible (exemplarDescriptor != null)
//                 → api.resolveMetric — SERIES + exemplars in ONE call off the
//                   spanmetrics 1s/10s/1m rollup tiers (D5). .series is
//                   byte-identical to api.spanMetric.
//   span source, ineligible (off-dim filter / DSL / p999·min·max / non-duration)
//                 → api.spanMetric (scope synthesized as a service.name chip;
//                   splitBy joined — byte-identical to the red-op preset)
//   metric source → api.metricQuery (what Metrics.tsx / MQE issue)
// Exemplars are carried by the eligible resolver path only; every other path
// returns []. The endpoint is a deterministic function of the query, so the
// querySignature cache key stays consistent (no resolver/legacy fragmentation).
//
// Results are memoised on a dataUpdatedAt signature (the MQE pattern):
// useQueries returns a fresh array every render, so depending on it
// directly would re-run consumers per hover/keystroke.

import { useMemo } from 'react';
import { useQueries, useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { keys } from '@/lib/queries/keys';
import { stepForWidth } from '@/lib/chartStep';
import { useContentWidth } from '@/lib/useContentWidth';
import { encodeFilters, encodeFilterGroup } from '@/lib/urlState';
import type { SpanMetricSeries, MetricExemplar, ChartAnnotation, OtlpExemplar } from '@/lib/types';
import { annotationsInWindow } from '@/lib/chartAnnotations';
import {
  type BuilderState, produces, effectiveFilters, querySignature, exemplarDescriptor,
  pinnedService, pinnedOperation, queryUnit, hasGroupedFilter, effectiveFilterGroup,
} from './model';

export interface ExploreQueriesResult {
  // letter → that query's series fan-out. undefined = loading or not
  // producing; [] = ran, no data.
  byLetter: Record<string, SpanMetricSeries[] | undefined>;
  // letter → total series the query produced BEFORE the server's top-N trim.
  // Equals byLetter[letter].length when no trim happened (resolver / metric /
  // small-cardinality span queries). PanelStack uses this for an accurate
  // "+N more" instead of inferring it from the (already-capped) series count.
  totalByLetter: Record<string, number | undefined>;
  // letter → per-bucket slow/error exemplar trace_ids (◆ glyphs). Populated
  // ONLY for resolver-eligible span queries — which now fetch series AND
  // exemplars in the SAME api.resolveMetric call (D5). [] for everything else.
  exemplarsByLetter: Record<string, MetricExemplar[]>;
  // v0.8.332 (pivot Phase 3) — letter → REAL OTLP exemplars (◆, neutral
  // 'otlp' kind) for CATALOGUE-metric queries scoped to a single service.
  // [] for span queries, group-by charts, and unscoped metric queries.
  otlpExemplarsByLetter: Record<string, OtlpExemplar[]>;
  anyLoading: boolean;
  // first error among producing queries, with the letter that raised it
  error: { letter: string; message: string } | null;
}

// QueryData — one producing query's fetched payload. Eligible span queries
// fill both fields from the resolver; ineligible/metric queries fill series
// from the legacy path and leave exemplars [].
interface QueryData {
  series: SpanMetricSeries[];
  exemplars: MetricExemplar[];
  // Pre-trim series count. Only the legacy span path (api.spanMetricTopN) can
  // report a value > series.length; the resolver + metric paths leave it
  // undefined and the consumer defaults to series.length.
  totalSeries?: number;
}

export function useExploreQueries(
  state: BuilderState,
  from: number,
  to: number,
): ExploreQueriesResult {
  // GRAN-A (v0.8.245) — Grafana-style width-aware auto step. step=0 (auto) no
  // longer defers to the backend's ~120-point ladder: we compute an explicit
  // step from the window and the #content width (~2px/point, snapped up to a
  // rung) and send it on all three fetch paths. The backend's min-step clamp
  // (v0.8.243) floors it at the metric's export interval, so asking fine is
  // safe. An operator-picked step (state.step > 0) passes through untouched.
  // The width is quantized into 200px buckets (useContentWidth), and the
  // effective step enters querySignature — so a drag-resize refetches at most
  // once per bucket crossing, exactly the pre-GRAN-A cache-key contract.
  const contentWidth = useContentWidth();
  const rangeSec = Math.max(1, Math.round((to - from) / 1e9)); // from/to are unix ns
  const effStep = state.step > 0 ? state.step : stepForWidth(rangeSec, contentWidth);

  const results = useQueries({
    queries: state.queries.map(q => {
      const filters = effectiveFilters(q);
      // D5 — resolver-eligible span queries (exemplarDescriptor != null: the
      // rollup-servable tier-dim shape) fetch SERIES AND EXEMPLARS in ONE
      // /api/metrics/resolve call instead of api.spanMetric + a separate
      // exemplar fetch. .series is byte-identical to api.spanMetric (verified)
      // and rides the finer 1s/10s/1m tiers. Ineligible span queries keep
      // api.spanMetric; metric queries keep api.metricQuery; both carry no
      // exemplars. The endpoint is a deterministic function of the query, so
      // the querySignature cache key stays consistent.
      const desc = q.source === 'span' ? exemplarDescriptor(q) : null;
      return {
        queryKey: keys.explore.query(querySignature(q, effStep), from, to),
        queryFn: (): Promise<QueryData> =>
          desc
            ? api.resolveMetric(desc, { from, to }, { step: effStep, exemplars: true })
                .then(r => ({ series: r?.series ?? [], exemplars: r?.exemplars ?? [] }))
            : q.source === 'span'
              ? api.spanMetricTopN({
                  agg: q.agg,
                  field: q.metric || undefined,
                  groupBy: q.splitBy.join(',') || undefined,
                  // Grouped (genuine OR / nested) query → send filterGroup and
                  // suppress flat filters (never both; backend prefers
                  // filterGroup). effectiveFilterGroup folds the scope pin in as
                  // a top-level AND leaf so scoping matches the flat path.
                  // Flat / absent group → legacy filters= path, byte-identical
                  // (encodeFilterGroup returns '' for it so the param is
                  // omitted and the query string + cache key are unchanged).
                  filters: hasGroupedFilter(q)
                    ? undefined
                    : (filters.length ? JSON.stringify(filters) : undefined),
                  filterGroup: hasGroupedFilter(q)
                    ? (encodeFilterGroup(effectiveFilterGroup(q)) || undefined)
                    : undefined,
                  dsl: q.dsl.trim() || undefined,
                  from, to,
                  step: effStep,
                }).then(r => ({ series: r?.series ?? [], exemplars: [], totalSeries: r?.totalSeries }))
              : api.metricQuery({
                  name: q.metric,
                  agg: q.agg,
                  groupBy: q.splitBy.length ? q.splitBy.join(',') : undefined,
                  filters: filters.length ? encodeFilters(filters) : undefined,
                  from, to,
                  step: effStep,
                }).then(r => ({ series: r ?? [], exemplars: [] })),
        enabled: produces(q) && from > 0,
        staleTime: 30_000,
      };
    }),
  });

  // v0.8.332 (pivot Phase 3) — REAL OTLP exemplar ◆ for catalogue-metric
  // charts. The span-source paths above already carry span-DERIVED exemplars
  // (resolver rollups); the metric-source path had none because metric_points
  // stored no exemplars until pivot Phase 1. Fetch gate: a picked catalogue
  // metric scoped to ONE service (pinnedService — scope slot or a single
  // service.name= chip) and NO splitBy — a multi-series group-by chart has no
  // per-series service scoping, so it is skipped silently. The window is
  // minute-bucketed into BOTH the key and the request so the client-key
  // cardinality stays bounded and lines up with the server's minute-bucketed
  // 30s cache key (pivot.go pivotExemplarKey); staleTime ≥ that server TTL.
  // Decoration only: errors/loading never gate the panel (data ?? []).
  const otlpResults = useQueries({
    queries: state.queries.map(q => {
      const svc = q.source === 'metric' ? pinnedService(q) : '';
      const fromB = Math.floor(from / 60e9) * 60e9; // minute-bucketed unix ns
      const toB = Math.floor(to / 60e9) * 60e9;
      // v0.8.432 (audit Faz B) — grouped charts stopped being skipped:
      // a splitBy query rides /api/exemplars/by-series (the server maps
      // series → fingerprints and tags items with groupKey; grouped
      // OR-filters stay out — their filter shape can't ride the flat
      // filters param). Ungrouped single-service charts keep the
      // legacy endpoint verbatim.
      const grouped = q.source === 'metric' && produces(q)
        && !!q.metric && q.splitBy.length > 0 && !hasGroupedFilter(q);
      const eligible = q.source === 'metric' && produces(q)
        && !!q.metric && !!svc && q.splitBy.length === 0;
      if (grouped) {
        const filters = effectiveFilters(q);
        const fstr = filters.length ? encodeFilters(filters) : undefined;
        return {
          queryKey: ['explore-otlp-exemplars-grouped', q.metric, svc,
            q.splitBy.join(','), fstr ?? '', fromB, toB, 50],
          queryFn: (): Promise<OtlpExemplar[]> =>
            api.exemplarsBySeries({
              metric: q.metric, service: svc || undefined, groupBy: q.splitBy,
              filters: fstr, from: fromB, to: toB, limit: 50,
            }).then(r => r?.items ?? []),
          enabled: from > 0,
          staleTime: 30_000,
        };
      }
      return {
        queryKey: ['explore-otlp-exemplars', q.metric, svc, fromB, toB, 50],
        queryFn: (): Promise<OtlpExemplar[]> =>
          api.exemplars({ metric: q.metric, service: svc, from: fromB, to: toB, limit: 50 })
            .then(r => r?.items ?? []),
        enabled: eligible && from > 0,
        staleTime: 30_000,
      };
    }),
  });

  // Stabilise on data identity, not array identity (MQE perf pattern).
  const dataSig = results.map(r => (r.data ? r.dataUpdatedAt : r.isError ? -1 : 0)).join('|')
    + '◆' + otlpResults.map(r => (r.data ? r.dataUpdatedAt : 0)).join('|');
  return useMemo(() => {
    const byLetter: Record<string, SpanMetricSeries[] | undefined> = {};
    const totalByLetter: Record<string, number | undefined> = {};
    const exemplarsByLetter: Record<string, MetricExemplar[]> = {};
    const otlpExemplarsByLetter: Record<string, OtlpExemplar[]> = {};
    let anyLoading = false;
    let error: { letter: string; message: string } | null = null;
    state.queries.forEach((q, i) => {
      const r = results[i];
      if (!produces(q)) return;
      if (r.isLoading) anyLoading = true;
      if (r.isError && !error) {
        error = { letter: q.letter, message: r.error instanceof Error ? r.error.message : String(r.error) };
      }
      const series = r.data === undefined ? undefined : (r.data.series ?? []);
      byLetter[q.letter] = series;
      // Default the total to the (possibly capped) series length when the path
      // doesn't report one — resolver / metric queries are never trimmed.
      totalByLetter[q.letter] = series === undefined ? undefined : (r.data?.totalSeries ?? series.length);
      exemplarsByLetter[q.letter] = r.data?.exemplars ?? [];
      otlpExemplarsByLetter[q.letter] = otlpResults[i].data ?? [];
    });
    return { byLetter, totalByLetter, exemplarsByLetter, otlpExemplarsByLetter, anyLoading, error };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [dataSig, state]);
}

// (useExploreExemplars retired in D5 — its per-query resolver fetch folded into
// useExploreQueries above, so an eligible query hits /api/metrics/resolve ONCE
// for series + exemplars instead of twice on two cache keys.)

// explore-v2 Phase 3.3 — per-query context overlays.
export interface ExploreOverlay {
  deploys: number[];                                           // unix ns — ▼ markers
  events: ChartAnnotation[];                                   // v0.8.284 — operator-event annotation lines
  thresholds: { value: number; label?: string; color?: string }[];
}

// useExploreOverlays — deploy markers + SLO threshold lines for queries pinned
// to a single service (pinnedService). A query with no unambiguous service
// gets no overlays — there's no single deploy stream / SLO to draw against an
// OR/multi-service query. SLO latency thresholds land ONLY on a latency (ms)
// panel of that service (and matching operation when the SLO is op-scoped);
// an availability SLO has no single hline so it's skipped.
export function useExploreOverlays(
  state: BuilderState,
  from: number,
  to: number,
): Record<string, ExploreOverlay> {
  // One SLO list for the whole builder (small; filtered per query below).
  const slosQ = useQuery({
    queryKey: ['explore-slos'],
    queryFn: () => api.listSLOs(),
    enabled: from > 0,
    staleTime: 300_000,
  });
  const slos = slosQ.data ?? [];

  // Deploys per query — only for queries pinned to one service.
  const deployResults = useQueries({
    queries: state.queries.map(q => {
      const svc = pinnedService(q);
      return {
        queryKey: ['explore-deploys', svc, from, to],
        queryFn: (): Promise<number[]> =>
          svc
            ? api.serviceDeploys(svc, { from, to }).then(d => (d ?? []).map(x => x.timeUnixNs))
            : Promise.resolve([]),
        enabled: !!svc && from > 0,
        staleTime: 60_000,
      };
    }),
  });

  // Operator events per query — same pinned-service gate as deploys. A query
  // with no unambiguous service gets none (a global/OR query has no single
  // event stream to annotate against). Windowed + deduped + capped by
  // annotationsInWindow before it reaches the draw hook (v0.8.284).
  const eventResults = useQueries({
    queries: state.queries.map(q => {
      const svc = pinnedService(q);
      return {
        queryKey: ['explore-events', svc, from, to],
        queryFn: () => api.listEvents({ from, to, service: svc || undefined, limit: 100 }),
        enabled: !!svc && from > 0,
        staleTime: 60_000,
      };
    }),
  });

  const sig = deployResults.map(r => (r.data ? r.dataUpdatedAt : 0)).join('|')
    + '#' + eventResults.map(r => (r.data ? r.dataUpdatedAt : 0)).join('|')
    + ':' + (slosQ.data ? slosQ.dataUpdatedAt : 0);
  return useMemo(() => {
    const out: Record<string, ExploreOverlay> = {};
    state.queries.forEach((q, i) => {
      const svc = pinnedService(q);
      const op = pinnedOperation(q);
      const deploys = deployResults[i].data ?? [];
      const events = annotationsInWindow(eventResults[i].data, from, to);
      const thresholds: ExploreOverlay['thresholds'] = [];
      if (svc && q.source === 'span' && queryUnit(q) === 'ms') {
        for (const s of slos) {
          if (s.service !== svc || s.sliType !== 'latency' || !(s.thresholdMs > 0)) continue;
          if (s.operation && s.operation !== op) continue; // op-scoped SLO needs a matching op pin
          thresholds.push({ value: s.thresholdMs, label: `SLO ${s.name}`, color: 'var(--warn)' });
        }
      }
      out[q.letter] = { deploys, events, thresholds };
    });
    return out;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sig, state, slos, from, to]);
}
