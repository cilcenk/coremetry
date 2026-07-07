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

// ── Admin infrastructure reads ────────────────────────────

// ClickHouse self-stats for /admin/clickhouse. 10s poll matches the
// prior page-level setInterval; React Query pauses the interval
// while the tab is hidden (refetchIntervalInBackground defaults to
// false), preserving the document.hidden gate.
export function useClickhouseHealth() {
  return useQuery({
    queryKey: ['admin', 'clickhouse-health'],
    queryFn: api.clickhouseHealth,
    refetchInterval: 10_000,
    staleTime: 10_000,
  });
}

// Multi-pod HA roster for /admin/cluster. 10s poll matches the
// heartbeat interval so a freshly-rolled pod appears within one
// tick; hidden tabs pause automatically.
export function useClusterMembers() {
  return useQuery({
    queryKey: ['admin', 'cluster-members'],
    queryFn: api.listClusterMembers,
    refetchInterval: 10_000,
    staleTime: 10_000,
  });
}

// ES index inventory + ILM lifecycle for /admin/elastic. One shot
// per mount (no poll) — _cat/indices + _ilm/explain cost ~1-3s on
// big clusters.
export function useElasticIndices() {
  return useQuery({
    queryKey: ['admin', 'elastic', 'indices'],
    queryFn: api.adminElasticIndices,
  });
}

// Recent failed ES queries (v0.8.230). Polls every 30s so an error
// the operator just triggered on /logs shows up without a manual
// refresh; pauses on document.hidden via the interval default.
export function useElasticErrors() {
  return useQuery({
    queryKey: ['admin', 'elastic', 'errors'],
    queryFn: api.adminElasticErrors,
    refetchInterval: 30_000,
    staleTime: 30_000,
  });
}

// Trace-context self-discovery (v0.8.348, pivot Phase 1c). One shot per
// mount + a staleTime matching the server's 5m cache so tab switches
// don't re-trigger the ES field_caps + 24h coverage aggregation (the
// ES-cost UI discipline — no polling, no list prefetch).
export function useTraceContext() {
  return useQuery({
    queryKey: ['admin', 'logstore', 'trace-context'],
    queryFn: api.adminLogstoreTraceContext,
    staleTime: 5 * 60_000,
  });
}

// SQL playground schema browser. Schema shifts on migrations, not
// minute-to-minute — cache it for the session's practical length.
export function useSqlSchema(enabled = true) {
  return useQuery({
    queryKey: ['admin', 'sql-schema'],
    queryFn: api.sqlSchema,
    enabled,
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
