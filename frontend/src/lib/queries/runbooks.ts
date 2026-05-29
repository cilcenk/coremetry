import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api } from '@/lib/api';
import type { Runbook } from '@/lib/types';

// Runbooks (v0.7.0) — list + per-id detail + CRUD / enable-disable
// mutations. Mirrors the alerts.ts hook module: one KEY, a list query
// that refetches lazily (runbooks change rarely), and a mutation
// wrapper that invalidates the whole namespace on success so a save /
// enable / delete reflects immediately on both the list and any open
// detail view.
const RUNBOOKS_KEY = ['runbooks'] as const;

export function useRunbooks() {
  return useQuery<Runbook[]>({
    queryKey: RUNBOOKS_KEY,
    queryFn: async () => (await api.runbooks()) ?? [],
    staleTime: 60_000,
    refetchInterval: 60_000,
  });
}

export function useRunbook(id: string) {
  return useQuery<Runbook>({
    queryKey: [...RUNBOOKS_KEY, id],
    queryFn: () => api.runbook(id),
    enabled: !!id,
    staleTime: 30_000,
  });
}

function useRunbookMutation<T>(fn: (input: T) => Promise<unknown>) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: fn,
    onSuccess: () => qc.invalidateQueries({ queryKey: RUNBOOKS_KEY }),
  });
}

export function useCreateRunbook() {
  return useRunbookMutation<Partial<Runbook>>(api.createRunbook);
}

export function useUpdateRunbook() {
  return useRunbookMutation<{ id: string; patch: Partial<Runbook> }>(
    ({ id, patch }) => api.updateRunbook(id, patch),
  );
}

export function useDeleteRunbook() {
  return useRunbookMutation<string>(api.deleteRunbook);
}

export function useEnableRunbook() {
  return useRunbookMutation<string>(api.enableRunbook);
}

export function useDisableRunbook() {
  return useRunbookMutation<string>(api.disableRunbook);
}
