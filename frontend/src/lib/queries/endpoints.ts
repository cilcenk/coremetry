import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';

// /endpoints listing — server-aggregated RED rows per (service ×
// http.route). Every fetch-relevant control (window, service,
// path substring, cluster, limit, compare, groupBy, sort, dir) is
// part of the key so each combination caches separately.
//
// v0.8.356 — 30s polling (matches useServices; the audit found the
// page never refreshed without a manual reload). React Query pauses
// refetchInterval on hidden tabs by default, satisfying the
// document.hidden house rule.
export function useEndpoints(params: Parameters<typeof api.endpoints>[0]) {
  return useQuery({
    queryKey: ['endpoints', 'list', params],
    queryFn: async () => (await api.endpoints(params)) ?? [],
    refetchInterval: 30_000,
  });
}

// v0.8.360 — detail drawer payload (Stage-2 slice E2). Fetch-on-OPEN
// only (`enabled` gates on the drawer's URL param being decoded) and
// NEVER polls — the drawer is a point-in-time drill-down. staleTime
// matches the server's 30s cache TTL so re-opening the same endpoint
// inside the TTL doesn't re-fire (the ES-cost discipline applied to
// a five-section CH read).
export function useEndpointDetail(
  params: Parameters<typeof api.endpointDetail>[0] | null,
) {
  return useQuery({
    queryKey: ['endpoints', 'detail', params],
    queryFn: async () => api.endpointDetail(params!),
    enabled: params !== null,
    staleTime: 30_000,
  });
}

// v0.8.360 — split-by section. Gated on the operator PICKING a
// dimension (by !== '') so opening the drawer never fires it.
export function useEndpointSplit(
  params: Parameters<typeof api.endpointSplit>[0] | null,
) {
  return useQuery({
    queryKey: ['endpoints', 'split', params],
    queryFn: async () => api.endpointSplit(params!),
    enabled: params !== null && params.by !== '',
    staleTime: 30_000,
  });
}
