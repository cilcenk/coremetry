import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api } from '@/lib/api';
import type { Runbook, RunbookExecution } from '@/lib/types';

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

function useRunbookMutation<T, R = unknown>(fn: (input: T) => Promise<R>) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: fn,
    onSuccess: () => qc.invalidateQueries({ queryKey: RUNBOOKS_KEY }),
  });
}

export function useCreateRunbook() {
  return useRunbookMutation<Partial<Runbook>, Runbook>(api.createRunbook);
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

// ── Executions (v0.7.0) — a run is the durable audit record ──────────────
const EXEC_KEY = ['runbook-executions'] as const;

export function useRunbookExecutions(params?: { runbookId?: string; status?: string; problemId?: string }) {
  return useQuery<RunbookExecution[]>({
    queryKey: [...EXEC_KEY, params ?? {}],
    queryFn: async () => (await api.runbookExecutions(params)) ?? [],
    staleTime: 10_000,
  });
}

// useRunbookExecution drives the runner. Polls every 10s while the run is
// live and stops once it reaches a terminal status — React Query also
// pauses background refetch on blur by default, so this respects the
// document.hidden polling rule without extra wiring.
const EXEC_TERMINAL = ['completed', 'failed', 'cancelled'];
export function useRunbookExecution(execId: string) {
  return useQuery<RunbookExecution>({
    queryKey: [...EXEC_KEY, 'one', execId],
    queryFn: () => api.runbookExecution(execId),
    enabled: !!execId,
    staleTime: 5_000,
    refetchInterval: (q) => {
      const d = q.state.data as RunbookExecution | undefined;
      return d && EXEC_TERMINAL.includes(d.status) ? false : 10_000;
    },
  });
}

function useExecMutation<T, R = unknown>(fn: (input: T) => Promise<R>) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: fn,
    onSuccess: () => qc.invalidateQueries({ queryKey: EXEC_KEY }),
  });
}

export function useExecuteRunbook() {
  return useExecMutation<{ id: string; problemId?: string }, RunbookExecution>(({ id, problemId }) => api.executeRunbook(id, problemId));
}

export function useRunbookStepAction() {
  return useExecMutation<{ execId: string; stepId: string; action: 'complete' | 'skip' | 'fail'; note?: string }>(
    ({ execId, stepId, action, note }) => api.runbookStepAction(execId, stepId, action, note),
  );
}

export function useCancelRunbookExecution() {
  return useExecMutation<string>(api.cancelRunbookExecution);
}
