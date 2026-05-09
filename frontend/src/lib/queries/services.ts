import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { keys } from './keys';
import type { Service, ServiceMap, InfraMetricSeries, NeighborStat, ServiceRuntime } from '@/lib/types';

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
    queryFn: async () => (await api.services(range, opts?.limit, opts?.name)) ?? [],
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

export function useServiceMap(since: string, samples: number) {
  return useQuery<ServiceMap>({
    queryKey: keys.services.map(since, samples),
    queryFn: () => api.serviceMap(since, samples),
    refetchInterval: 30_000,
    staleTime: 25_000,
  });
}

export function useServiceInfra(svc: string, since: string) {
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

export function useServiceNeighbors(svc: string, since: string, samples: number) {
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
