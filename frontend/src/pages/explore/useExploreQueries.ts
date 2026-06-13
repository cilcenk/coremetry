// pages/explore/useExploreQueries.ts — the builder's data layer.
//
// Phase-2 (explore-v2). One react-query fetch per producing query:
//   span source   → api.spanMetric  (scope synthesized as a service.name
//                   chip; splitBy joined — the combined 'service.name,name'
//                   pair is byte-identical to the red-op preset)
//   metric source → api.metricQuery (what Metrics.tsx / MQE issue)
//
// Results are memoised on a dataUpdatedAt signature (the MQE pattern):
// useQueries returns a fresh array every render, so depending on it
// directly would re-run consumers per hover/keystroke.

import { useMemo } from 'react';
import { useQueries } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { keys } from '@/lib/queries/keys';
import { encodeFilters } from '@/lib/urlState';
import type { SpanMetricSeries, MetricExemplar } from '@/lib/types';
import {
  type BuilderState, produces, effectiveFilters, querySignature, exemplarDescriptor,
} from './model';

export interface ExploreQueriesResult {
  // letter → that query's series fan-out. undefined = loading or not
  // producing; [] = ran, no data.
  byLetter: Record<string, SpanMetricSeries[] | undefined>;
  anyLoading: boolean;
  // first error among producing queries, with the letter that raised it
  error: { letter: string; message: string } | null;
}

export function useExploreQueries(
  state: BuilderState,
  from: number,
  to: number,
): ExploreQueriesResult {
  const results = useQueries({
    queries: state.queries.map(q => {
      const filters = effectiveFilters(q);
      return {
        queryKey: keys.explore.query(querySignature(q, state.step), from, to),
        queryFn: (): Promise<SpanMetricSeries[] | null> =>
          q.source === 'span'
            ? api.spanMetric({
                agg: q.agg,
                field: q.metric || undefined,
                groupBy: q.splitBy.join(',') || undefined,
                filters: filters.length ? JSON.stringify(filters) : undefined,
                dsl: q.dsl.trim() || undefined,
                from, to,
                step: state.step || undefined,
              })
            : api.metricQuery({
                name: q.metric,
                agg: q.agg,
                groupBy: q.splitBy.length ? q.splitBy.join(',') : undefined,
                filters: filters.length ? encodeFilters(filters) : undefined,
                from, to,
                step: state.step || undefined,
              }),
        enabled: produces(q) && from > 0,
        staleTime: 30_000,
      };
    }),
  });

  // Stabilise on data identity, not array identity (MQE perf pattern).
  const dataSig = results.map(r => (r.data ? r.dataUpdatedAt : r.isError ? -1 : 0)).join('|');
  return useMemo(() => {
    const byLetter: Record<string, SpanMetricSeries[] | undefined> = {};
    let anyLoading = false;
    let error: { letter: string; message: string } | null = null;
    state.queries.forEach((q, i) => {
      const r = results[i];
      if (!produces(q)) return;
      if (r.isLoading) anyLoading = true;
      if (r.isError && !error) {
        error = { letter: q.letter, message: r.error instanceof Error ? r.error.message : String(r.error) };
      }
      byLetter[q.letter] = r.data === undefined ? undefined : (r.data ?? []);
    });
    return { byLetter, anyLoading, error };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [dataSig, state]);
}

// useExploreExemplars (explore-v2 Phase 3.2) — per-bucket slow/error trace_ids
// for each exemplar-ELIGIBLE builder query. Eligibility (exemplarDescriptor !=
// null) mirrors the spanmetrics rollup planner: only a span query the rollups
// can serve rides /api/metrics/resolve?exemplars=1; everything else returns []
// (no ◆ glyphs, no wasted query). Separate from the series fetch so the chart's
// data source stays api.spanMetric — the resolver only supplies the trace_ids.
export function useExploreExemplars(
  state: BuilderState,
  from: number,
  to: number,
): Record<string, MetricExemplar[]> {
  const results = useQueries({
    queries: state.queries.map(q => {
      const desc = exemplarDescriptor(q);
      return {
        queryKey: ['explore-exemplars', querySignature(q, state.step), from, to],
        queryFn: (): Promise<MetricExemplar[]> =>
          desc
            ? api.resolveMetric(desc, { from, to }, { step: state.step || undefined, exemplars: true })
                .then(r => r?.exemplars ?? [])
            : Promise.resolve([]),
        enabled: !!desc && produces(q) && from > 0,
        staleTime: 30_000,
      };
    }),
  });

  const sig = results.map(r => (r.data ? r.dataUpdatedAt : 0)).join('|');
  return useMemo(() => {
    const out: Record<string, MetricExemplar[]> = {};
    state.queries.forEach((q, i) => { out[q.letter] = results[i].data ?? []; });
    return out;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sig, state]);
}
