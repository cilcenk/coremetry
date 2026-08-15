import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api } from '@/lib/api';
import type { FailureSLOConfig, SLO, SLORow } from '@/lib/types';

const SLOS_KEY = ['slos'] as const;
const slosListKey = ['slos', 'list'] as const;

// SLOs — list + create/delete. Burn rate runs server-side
// every minute; the list reflects the latest computed status.
export function useSLOs() {
  return useQuery<SLORow[]>({
    queryKey: slosListKey,
    queryFn: async () => (await api.listSLOs()) ?? [],
    refetchInterval: 60_000,
    // v0.8.462 — 50s < 60s double-fetch aralığı kapandı (v0.4.79 deseni).
    staleTime: 60_000,
  });
}

// v0.9.1036 — hata-oranı (%) eşiği blob'u. Bir AYAR, bir zaman serisi
// değil: refetchInterval YOK (poller bütçesine girmez) ve staleTime 5 dk
// — operatör Settings'ten değiştirirse zaten kendi sekmesi yenilenir,
// diğer sekmeler bir sonraki mount'ta yakalar.
//
// Hata hâlinde varsayılana düşer, THROW ETMEZ: bir ayar okuması
// başarısız diye servis grafiği "hata" göstermemeli — çizgi kaybolur,
// grafik yaşar (blob'un kendisi zaten sunucuda da soft-fail'dir).
export function useFailureSLO() {
  return useQuery<FailureSLOConfig>({
    queryKey: ['settings', 'failure-slo'],
    queryFn: () => api.getFailureSLO().catch(() => ({ defaultPct: 0 })),
    staleTime: 300_000,
  });
}

function useSLOMutation<T, R = unknown>(fn: (input: T) => Promise<R>) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: fn,
    onSuccess: () => qc.invalidateQueries({ queryKey: SLOS_KEY }),
  });
}

// api.createSLO has a more specific input/return type than
// Partial<SLO>; reuse its inferred Parameters/Return so the
// hook stays in sync if api.ts changes.
export function useCreateSLO() {
  return useSLOMutation<Parameters<typeof api.createSLO>[0], SLO>(api.createSLO);
}
export function useDeleteSLO() {
  return useSLOMutation<string>(api.deleteSLO);
}
