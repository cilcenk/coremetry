import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { keys } from './keys';
import type { Incident, IncidentEvent } from '@/lib/types';

// Incidents — list, detail, events. The list refreshes every
// 30s alongside problems (keys.incidents.all is invalidated
// by the SSE problem.* event listener so a new attached problem
// surfaces immediately).

export function useIncidents(filter: {
  status?: string; service?: string; severity?: string; limit?: number;
} = {}) {
  // v0.9.456 — zarf: {items, counts, truncated}; null gövde boş-dürüst
  // zarfa normalize edilir.
  return useQuery<{ items: Incident[]; counts: Record<string, number> | null; truncated: boolean }>({
    queryKey: keys.incidents.list(filter),
    queryFn: async () => (await api.listIncidents(filter)) ?? { items: [], counts: null, truncated: false },
    refetchInterval: 30_000,
    staleTime: 25_000,
  });
}

export function useIncident(id: string) {
  return useQuery<Incident | null>({
    queryKey: keys.incidents.one(id),
    queryFn: () => api.getIncident(id),
    enabled: !!id,
    staleTime: 30_000,
  });
}

export function useIncidentEvents(id: string) {
  return useQuery<IncidentEvent[]>({
    queryKey: keys.incidents.events(id),
    queryFn: async () => (await api.incidentTimeline(id)) ?? [],
    enabled: !!id,
    staleTime: 30_000,
  });
}

export function useIncidentProblems(id: string) {
  return useQuery<string[]>({
    queryKey: keys.incidents.problems(id),
    queryFn: async () => (await api.incidentProblems(id)) ?? [],
    enabled: !!id,
    staleTime: 30_000,
  });
}

export function useCreateIncident() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.createIncident,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: keys.incidents.all });
    },
  });
}

export function useUpdateIncident() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, patch }: { id: string; patch: Partial<Incident> }) =>
      api.updateIncident(id, patch),
    onSuccess: (_data, vars) => {
      qc.invalidateQueries({ queryKey: keys.incidents.one(vars.id) });
      qc.invalidateQueries({ queryKey: keys.incidents.list({}) });
    },
  });
}
