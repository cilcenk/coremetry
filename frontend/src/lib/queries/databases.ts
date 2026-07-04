import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import type { SlowQueryRow } from '@/lib/types';

// Global slow-query catalog (v0.5.165) for /databases/slow-queries.
// One row per (service, normalised statement) ordered by total
// wall-clock time; optional db_system narrows to one engine.
export function useSlowQueries(params: {
  from?: number; to?: number; db_system?: string; limit?: number;
}) {
  return useQuery<SlowQueryRow[]>({
    queryKey: ['databases', 'slow-queries', params],
    queryFn: async () => (await api.slowQueries(params)) ?? [],
  });
}
