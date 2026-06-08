import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { keys } from './keys';
import type {
  CardinalityReport, SystemStats,
  StatusPageConfig, StatusComponent, StatusSubscriber,
} from '@/lib/types';

// Admin-only queries. /admin/cardinality and /admin/system-stats
// are the heaviest read-side endpoints (system.parts + uniqExact
// scans), already cached 60s/5min server-side. The client-side
// cache here just dedups parallel mounts and gives us a free
// "stale while we refetch" UX on tab switches.

export function useSystemStats() {
  return useQuery<SystemStats>({
    queryKey: keys.admin.systemStats,
    queryFn: api.systemStats,
    staleTime: 60_000,
    refetchInterval: 60_000,
  });
}

export function useCardinality() {
  return useQuery<CardinalityReport>({
    queryKey: keys.admin.cardinality,
    queryFn: api.cardinality,
    staleTime: 5 * 60_000,
  });
}

// ── Audit log ─────────────────────────────────────────────

export function useAuditLog(
  since = '24h',
  filters: { actor?: string; action?: string; target?: string } = {},
) {
  return useQuery({
    queryKey: ['admin', 'audit', since, filters],
    queryFn: () => api.auditLog(since, filters),
    staleTime: 30_000,
  });
}

// ── Public status page admin ─────────────────────────────

const STATUS_PAGE_KEY = ['admin', 'status-page'] as const;

export function useStatusPageConfig() {
  return useQuery<StatusPageConfig>({
    queryKey: [...STATUS_PAGE_KEY, 'config'],
    queryFn: api.statusPageGetConfig,
    staleTime: 60_000,
  });
}

export function useUpdateStatusPageConfig() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.statusPagePutConfig,
    onSuccess: () => qc.invalidateQueries({ queryKey: [...STATUS_PAGE_KEY, 'config'] }),
  });
}

export function useStatusPageComponents() {
  return useQuery<StatusComponent[]>({
    queryKey: [...STATUS_PAGE_KEY, 'components'],
    queryFn: async () => (await api.statusPageListComponents()) ?? [],
    staleTime: 30_000,
  });
}

function useComponentMutation<T>(fn: (input: T) => Promise<unknown>) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: fn,
    onSuccess: () => qc.invalidateQueries({ queryKey: [...STATUS_PAGE_KEY, 'components'] }),
  });
}

export function useCreateStatusComponent() {
  return useComponentMutation<Partial<StatusComponent>>(api.statusPageCreateComponent);
}
export function useUpdateStatusComponent() {
  return useComponentMutation<{ id: string; patch: Partial<StatusComponent> }>(
    ({ id, patch }) => api.statusPageUpdateComponent(id, patch),
  );
}
export function useDeleteStatusComponent() {
  return useComponentMutation<string>(api.statusPageDeleteComponent);
}

export function useStatusPageSubscribers() {
  return useQuery<StatusSubscriber[]>({
    queryKey: [...STATUS_PAGE_KEY, 'subscribers'],
    queryFn: async () => (await api.statusPageListSubscribers()) ?? [],
    staleTime: 60_000,
  });
}

export function useDeleteStatusSubscriber() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (email: string) => api.statusPageDeleteSubscriber(email),
    onSuccess: () => qc.invalidateQueries({ queryKey: [...STATUS_PAGE_KEY, 'subscribers'] }),
  });
}
