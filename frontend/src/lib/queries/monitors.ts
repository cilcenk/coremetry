import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api } from '@/lib/api';
import type { Monitor, MonitorRow, MonitorResult } from '@/lib/types';

const MONITORS_KEY = ['monitors'] as const;
const monitorsListKey = ['monitors', 'list'] as const;
const monitorTimelineKey = (id: string, n: number) =>
  ['monitors', 'timeline', id, n] as const;

// Synthetic monitors — list refreshes every 30s alongside
// /problems. Mutations auto-invalidate the list so a save /
// delete shows up immediately.
export function useMonitors() {
  return useQuery<MonitorRow[]>({
    queryKey: monitorsListKey,
    queryFn: async () => (await api.listMonitors()) ?? [],
    // v0.5.325 — Scale-audit polish: aligned staleTime to
    // refetchInterval so a quick tab re-mount inside the
    // 30s poll window doesn't trigger a second duplicate
    // request (was 25_000 → 5s gap, was a double-fetch on
    // fast Cmd-K nav between tabs).
    refetchInterval: 30_000,
    staleTime: 30_000,
  });
}

export function useMonitorTimeline(id: string, count = 60) {
  return useQuery<MonitorResult[]>({
    queryKey: monitorTimelineKey(id, count),
    queryFn: async () => (await api.monitorTimeline(id, count)) ?? [],
    enabled: !!id,
    staleTime: 30_000,
  });
}

function useMonitorMutation<T>(fn: (input: T) => Promise<unknown>) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: fn,
    onSuccess: () => qc.invalidateQueries({ queryKey: MONITORS_KEY }),
  });
}

export function useCreateMonitor() {
  return useMonitorMutation<Partial<Monitor>>(api.createMonitor);
}
export function useUpdateMonitor() {
  return useMonitorMutation<{ id: string; patch: Partial<Monitor> }>(
    ({ id, patch }) => api.updateMonitor(id, patch),
  );
}
export function useDeleteMonitor() {
  return useMonitorMutation<string>(api.deleteMonitor);
}
