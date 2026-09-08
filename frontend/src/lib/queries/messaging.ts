import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';

// v0.10.551 — /messaging çekmecesi "Kafka istemcileri · METRİK" bölümü.
// Yalnız çekmece açıkken (enabled) ve staleTime = sunucu TTL (30 s); polling
// YOK — çekmece kısa ömürlü, VM sorgusu 5 soru × ≤60 adım (ES-cost disiplini:
// fetch on open, prefetch yok).
export function useMessagingClients(p: {
  system: string; cluster: string; destination: string; fromNs: number; toNs: number; enabled?: boolean;
}) {
  return useQuery({
    queryKey: ['messaging', 'clients', p.system, p.cluster, p.destination, p.fromNs, p.toNs],
    queryFn: ({ signal }) => api.messagingClients(p.system, p.cluster, p.destination, p.fromNs, p.toNs, signal),
    enabled: (p.enabled ?? true) && !!p.system && !!p.destination,
    staleTime: 30_000,
  });
}
