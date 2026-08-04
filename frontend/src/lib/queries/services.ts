import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { keys } from './keys';
import type { GoDuration } from '@/lib/utils';
import type { Service, ServiceMap, InfraMetricSeries, NeighborStat, ServiceRuntime, Deploy, ServiceMetadata } from '@/lib/types';

// /api/services + related — the topology side of the app.
// `range` carries the time window so two pages with different
// ranges cache separately. Cached longer than problems (10 min
// stale) because the services list shifts on deploys, not
// minute-to-minute.

export function useServices(
  range: { from: number; to: number },
  opts?: { limit?: number; name?: string },
) {
  return useQuery<Service[]>({
    queryKey: keys.services.list(range, opts),
    queryFn: async ({ signal }) => (await api.services(range, opts?.limit, opts?.name, signal)) ?? [],
    staleTime: 60_000,
  });
}

export function useServiceNames(q?: string) {
  return useQuery({
    queryKey: keys.services.names(q),
    queryFn: () => api.serviceNames(q),
    // Picker dropdown — debounced upstream by the input handler;
    // a 5-min stale-time means typing the same prefix again
    // doesn't refetch.
    staleTime: 5 * 60_000,
  });
}

export function useServiceMap(since: GoDuration, samples: number, diff?: string, topN = 0) {
  // `diff` (e.g. "24h", "1h") asks the backend to also return a
  // baseline-vs-current topology delta. Empty string / undefined
  // means "current snapshot only". Encoded as part of the
  // queryKey so different diff windows don't collide in the
  // React Query cache.
  return useQuery<ServiceMap>({
    queryKey: keys.services.map(since, samples, diff, topN),
    queryFn: ({ signal }) => api.serviceMap(since, samples, diff, topN, signal),
    refetchInterval: 30_000,
    // v0.8.462 — 25s < 30s poll penceresi tab dönüşlerinde double-fetch
    // üretiyordu (anomalies.ts v0.4.79 deseni); eşitlendi.
    staleTime: 30_000,
  });
}

export function useServiceInfra(svc: string, since: GoDuration) {
  return useQuery<InfraMetricSeries[]>({
    queryKey: keys.services.infra(svc, since),
    queryFn: async () => (await api.serviceInfraMetrics(svc, since)) ?? [],
    enabled: !!svc,
    staleTime: 30_000,
  });
}

export function useServiceRuntime(svc: string) {
  return useQuery<ServiceRuntime>({
    queryKey: keys.services.runtime(svc),
    queryFn: () => api.serviceRuntime(svc),
    enabled: !!svc,
    // Runtime fingerprint changes only on deploy. 5 min stale
    // matches the server cache; longer would mean a fresh
    // deploy takes a while to surface in the badge.
    staleTime: 5 * 60_000,
  });
}

// Batch variant — fetches every service's runtime in one
// request. Used by the /services listing to show a per-row
// badge without fanning out N requests.
export function useAllServiceRuntimes() {
  return useQuery<Record<string, ServiceRuntime>>({
    queryKey: ['services', 'runtimes', 'all'],
    queryFn: () => api.allServiceRuntimes(),
    staleTime: 5 * 60_000,
  });
}

// useServiceDeploys — distinct service.version timestamps in
// the time window, for the deploy-marker overlay on charts.
// 60s server cache so re-mounts don't hammer CH; 30s client
// stale so a fresh deploy surfaces within ~one chart refresh.
export function useServiceDeploys(svc: string, from: number, to: number) {
  return useQuery<Deploy[]>({
    queryKey: keys.deploys.forService(svc, from, to),
    queryFn: async () => (await api.serviceDeploys(svc, { from, to })) ?? [],
    enabled: !!svc && !!from && !!to,
    staleTime: 30_000,
  });
}

// useServiceRollouts (v0.8.x) — pod-churn rollout events for the
// chart deploy-marker overlay + the "Recent rollouts" panel. Same
// cache posture as useServiceDeploys.
export function useServiceRollouts(svc: string, from: number, to: number) {
  return useQuery({
    queryKey: ['service-rollouts', svc, from, to],
    queryFn: () => api.serviceRollouts(svc, { from, to }),
    enabled: !!svc && !!from && !!to,
    staleTime: 30_000,
  });
}

// useServiceRollouts7d — v0.9.360. The Details tab used to fetch the SAME
// rollouts endpoint three times per open: ServiceCharts (page window, deploy
// markers — stays as-is), DetailsPropsStrip (page window, private key) and
// DeployHistoryPanel (raw effect, 7-day window minted from Date.now() on
// every mount, so its ns-exact key never repeated). The strip and the panel
// now share THIS one 7-day read: one request, one React Query entry.
//
// The window is computed inside queryFn and SNAPPED to the 5-minute rollout
// grid, so the key needs no timestamps at all — staleTime is the freshness
// contract, and the server key (cacheBucket) collapses across viewers too.
// Side effect the low-severity finding asked for: the strip's Version chip
// now looks at 7 days, so it no longer vanishes at the default 30m range.
export function useServiceRollouts7d(svc: string) {
  return useQuery({
    queryKey: ['service-rollouts-7d', svc],
    queryFn: () => {
      const grid = 5 * 60_000;
      const toMs = Math.floor(Date.now() / grid) * grid;
      const to = toMs * 1e6;
      const from = to - 7 * 24 * 3600 * 1e9;
      return api.serviceRollouts(svc, { from, to });
    },
    enabled: !!svc,
    staleTime: 60_000,
  });
}

// Service-catalog metadata — operator-curated owner / SRE team /
// runbook links, joined locally by the consumers. The endpoint is
// server-cached 60s; the matching client stale-time keeps repeat
// mounts within that window free.
export function useServicesMetadata() {
  return useQuery<Record<string, ServiceMetadata>>({
    queryKey: keys.services.metadata,
    queryFn: async () => (await api.servicesMetadata()) ?? {},
    staleTime: 60_000,
  });
}

// Inbound-callers backtrace — the Dynatrace-style consumer view on
// /service-backtrace. Keyed on (service, since/limit) so flipping
// the range preset caches per window.
export function useServiceBacktrace(
  svc: string,
  opts: { since?: string; from?: number; to?: number; limit?: number } = {},
) {
  return useQuery({
    queryKey: keys.services.backtrace(svc, opts),
    queryFn: () => api.serviceBacktrace(svc, opts),
    enabled: !!svc,
  });
}

// Cluster facet options (k8s / openshift cluster resource attr) —
// shared by the /services and /endpoints filter dropdowns. The
// response is cached server-side (60s) so flipping ranges quickly
// is free after the first hit.
export function useClusters(from: number, to: number) {
  return useQuery<string[]>({
    queryKey: ['clusters', from, to],
    queryFn: async () => (await api.clusters(from, to))?.clusters ?? [],
    staleTime: 60_000,
  });
}

export function useServiceNeighbors(svc: string, since: GoDuration, samples: number) {
  return useQuery({
    queryKey: keys.services.neighbors(svc, since, samples),
    queryFn: () => api.serviceNeighbors(svc, since, samples),
    enabled: !!svc,
    staleTime: 60 * 60_000, // neighbours shift on deploys, not seconds
  });
}

// "Use this stat unless we've explicitly asked for a refresh" —
// some pages expose a refresh button that re-runs the query,
// matching the original `?refresh=1` behaviour. With React
// Query that's a `qc.invalidateQueries(...)` instead of a
// special URL param.
export type { NeighborStat };
