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
