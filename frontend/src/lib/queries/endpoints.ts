import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';

// /endpoints listing — server-aggregated RED rows per (service ×
// http.route). Every fetch-relevant control (window, service,
// path substring, cluster, limit, compare, groupBy) is part of the
// key so each combination caches separately.
export function useEndpoints(params: Parameters<typeof api.endpoints>[0]) {
  return useQuery({
    queryKey: ['endpoints', 'list', params],
    queryFn: async () => (await api.endpoints(params)) ?? [],
  });
}
