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

// v0.8.378 — statement detail drawer payload (Stage-2 slice D2).
// Fetch-on-OPEN only (`enabled` gates on the drawer's `?stmt=` param
// decoding) and NEVER polls — a point-in-time drill-down. staleTime
// matches the server's 30s cache TTL so re-opening the same statement
// inside the TTL doesn't re-fire (the useEndpointDetail posture).
export function useDBStmtDetail(
  params: Parameters<typeof api.dbStmtDetail>[0] | null,
) {
  return useQuery({
    queryKey: ['databases', 'stmt-detail', params],
    queryFn: async () => api.dbStmtDetail(params!),
    enabled: params !== null,
    staleTime: 30_000,
  });
}
