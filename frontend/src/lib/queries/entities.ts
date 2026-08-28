import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { keys } from './keys';
import type {
  EntityClustersResponse, EntityDetailResponse, EntityListResponse, EntityMetricsResponse, EntityContainersResponse, EntityClusterInfo,
  EntityServicesResponse, EntitySettings, EntitySettingsResponse, EntitySyncResponse, ServicePodsResponse,
} from '@/lib/types';

// K8s entity katmanı (v0.10.131) — staleTime = sunucu serveCached TTL'i
// (entities.go: clusters/list/get 15 s, services/service-pods 30 s, metrics
// 60 s). Bayrak kapalıyken uçlar 404 {disabled:true} → `useEntityEnabled`
// viewer-güvenli kapı (admin ayar ucu değil). Pivotlar yalnız açık
// sekmede/sayfada fetch eder (ES-cost disiplini: `enabled`).

/** Viewer-safe "katman açık mı" — /api/entities/clusters 404 {disabled} döner. */
export function useEntityClusters(enabled = true) {
  return useQuery<EntityClustersResponse>({
    queryKey: keys.entities.clusters(),
    queryFn: ({ signal }) => api.entityClusters(signal).catch((e: unknown) => {
      // 404 {disabled:true} bir hata değil, kapalı bayrak.
      if (e instanceof Error && /404/.test(e.message)) return { disabled: true } as EntityClustersResponse;
      throw e;
    }),
    staleTime: 15_000,
    enabled,
  });
}

// v0.10.137 (inceleme) — NONE modül sabiti: `?? []` her render'da yeni dizi
// üretip tüketicilerin memo'larını boşa düşürüyordu; `enabled` parametresi
// public trace görüntüleyicide (kimliksiz) auth kapılı isteği HİÇ atmamak için.
const NONE: EntityClusterInfo[] = [];
export function useEntityEnabled(enabled = true) {
  const q = useEntityClusters(enabled);
  return { enabled: enabled && !!q.data && !q.data.disabled, loading: enabled && q.isPending, clusters: q.data?.clusters ?? NONE };
}

export function useEntities(q: { cluster: string; type?: string; namespace?: string; q?: string; at?: number; limit?: number }, enabled = true) {
  return useQuery<EntityListResponse>({
    queryKey: keys.entities.list(q),
    queryFn: ({ signal }) => api.entities(q, signal),
    staleTime: 15_000,
    enabled: enabled && !!q.cluster,
  });
}

export function useEntity(id: string, at?: number, enabled = true) {
  return useQuery<EntityDetailResponse>({
    queryKey: keys.entities.get(id, at),
    queryFn: ({ signal }) => api.entity(id, at, signal),
    staleTime: 15_000,
    enabled: enabled && !!id,
  });
}

export function useEntityServices(id: string, range: { from: number; to: number }, enabled = true) {
  return useQuery<EntityServicesResponse>({
    queryKey: keys.entities.services(id, range),
    queryFn: ({ signal }) => api.entityServices(id, range.from, range.to, signal),
    staleTime: 30_000,
    enabled: enabled && !!id,
  });
}

export function useEntityMetrics(id: string, range: { from: number; to: number }, enabled = true) {
  return useQuery<EntityMetricsResponse>({
    queryKey: keys.entities.metrics(id, range),
    queryFn: ({ signal }) => api.entityMetrics(id, range.from, range.to, signal),
    staleTime: 60_000,
    enabled: enabled && !!id,
  });
}

// v0.10.135 — pod konteyner durumları; yalnız canlı pod'da (ölü pod'da KSM
// serisi zaten yok) ve panel açıkken (enabled).
export function useEntityContainers(id: string, enabled = true) {
  return useQuery<EntityContainersResponse>({
    queryKey: keys.entities.containers(id),
    queryFn: ({ signal }) => api.entityContainers(id, signal),
    staleTime: 30_000,
    enabled: enabled && !!id,
  });
}

export function useServicePods(service: string, cluster: string, range: { from: number; to: number }, enabled = true) {
  return useQuery<ServicePodsResponse>({
    queryKey: keys.entities.servicePods(service, cluster, range),
    queryFn: ({ signal }) => api.servicePods(service, cluster, range.from, range.to, signal),
    staleTime: 30_000,
    enabled: enabled && !!service,
  });
}

// ── admin ──
export function useEntitySettings() {
  return useQuery<EntitySettingsResponse>({ queryKey: keys.entities.settings(), queryFn: () => api.entitySettings(), staleTime: 10_000 });
}

export function useSaveEntitySettings() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (s: EntitySettings) => api.putEntitySettings(s),
    onSuccess: () => { void qc.invalidateQueries({ queryKey: keys.entities.all }); },
  });
}

export function useEntitySync(enabled = true) {
  return useQuery<EntitySyncResponse>({
    queryKey: keys.entities.sync(),
    queryFn: () => api.entitySync(),
    staleTime: 10_000,
    refetchInterval: 15_000, // ≥10 s kuralı; RQ gizli sekmede durur
    enabled,
  });
}

export function useRunEntitySync() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => api.runEntitySync(),
    onSuccess: () => { setTimeout(() => { void qc.invalidateQueries({ queryKey: keys.entities.sync() }); }, 3000); },
  });
}
