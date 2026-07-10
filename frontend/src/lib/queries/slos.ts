import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api } from '@/lib/api';
import type { SLO, SLORow } from '@/lib/types';

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
