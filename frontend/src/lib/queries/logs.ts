import { keepPreviousData, useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import type { LogsParams } from '@/lib/api';
import type { LogsResponse, LogPatternsResult } from '@/lib/types';

// /api/logs query — keyed on the full filter object so a
// pagination click or filter change caches separately.
//
// v0.8.3 (operator-reported ES incident) — /api/logs is UNCACHED on
// the backend and the Elasticsearch path opens a fresh Point-in-Time
// per call. staleTime:0 + the default refetchOnWindowFocus meant every
// tab focus / reconnect re-fired the list query → another PIT opened
// (and leaked for 2m if the operator didn't page to the end). At ES
// scale this was a measurable amplifier of the api-pod CPU climb.
// staleTime 15s keeps back-nav freshness "good enough" while collapsing
// focus refires; an explicit Next / filter change still refetches
// because the queryKey (params incl. cursor) changes. refetchOnWindowFocus
// off stops the tab-focus PIT churn outright. (No effect on CH backend
// correctness — it just fetches less.)
// v0.9.286 — this hook IS the paging surface (/logs, the only list with
// a Load-more), so it declares paging intent here rather than at each
// call site. Everything else reaches /api/logs through api.logs()
// directly, reads one page and drops the cursor; those callers now cost
// the ES cluster no retained Point-in-Time.
export function useLogs(params: LogsParams) {
  const pagingParams: LogsParams = { ...params, paging: true };
  return useQuery<LogsResponse>({
    queryKey: ['logs', 'list', pagingParams],
    queryFn: ({ signal }) => api.logs(pagingParams, signal),
    staleTime: 15_000,
    refetchOnWindowFocus: false,
    // v0.8.260 — "Load more" accumulation on /logs: a cursor change
    // is a NEW query key; without a placeholder the page flashed the
    // skeleton on every append. Previous data holds the table stable
    // while the next page loads (isPlaceholderData guards the
    // accumulator from ingesting the stand-in).
    placeholderData: keepPreviousData,
  });
}

// v0.10.297 — /logs "Desenler" paneli. YALNIZ panel açıkken (enabled) —
// v0.8.270 ES-maliyet disiplini: liste prefetch'i yok, açılışta fetch.
// staleTime = sunucu TTL (30 s); anahtar TÜM parametreler.
export function useLogsPatterns(params: LogsParams & { limit?: number }, enabled: boolean) {
  return useQuery<LogPatternsResult>({
    queryKey: ['logs', 'patterns', params],
    queryFn: ({ signal }) => api.logsPatterns(params, signal),
    staleTime: 30_000,
    refetchOnWindowFocus: false,
    placeholderData: keepPreviousData,
    enabled,
  });
}
