import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api } from '@/lib/api';
import type { AlertRule } from '@/lib/types';

const ALERTS_KEY = ['alerts', 'rules'] as const;

// Alerts — list + CRUD mutations. The list refetches every
// 60s (rules don't change often) but every mutation invalidates
// it eagerly so a save shows up immediately.

export function useAlertRules() {
  return useQuery<AlertRule[]>({
    queryKey: ALERTS_KEY,
    queryFn: async () => (await api.alertRules()) ?? [],
    staleTime: 60_000,
    refetchInterval: 60_000,
  });
}

function useAlertMutation<T>(fn: (input: T) => Promise<unknown>) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: fn,
    onSuccess: () => qc.invalidateQueries({ queryKey: ALERTS_KEY }),
  });
}

export function useCreateAlertRule() {
  return useAlertMutation<Partial<AlertRule>>(api.createAlertRule);
}

export function useUpdateAlertRule() {
  return useAlertMutation<{ id: string; patch: Partial<AlertRule> }>(
    ({ id, patch }) => api.updateAlertRule(id, patch),
  );
}

export function useDeleteAlertRule() {
  return useAlertMutation<string>(api.deleteAlertRule);
}

export function useEnableAlertRule() {
  return useAlertMutation<string>(api.enableAlertRule);
}

export function useDisableAlertRule() {
  return useAlertMutation<string>(api.disableAlertRule);
}
