import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { keys } from './keys';
import type {
  LogPatternAnomaly, TraceOpAnomaly, AnomalyEvent, AnomalySilence, Problem,
} from '@/lib/types';

// /anomalies is the heaviest fan-out page in the app — five
// independent feeds (log patterns, trace ops, metric anomalies,
// history, silences) all polled together. With React Query each
// gets its own background poll + own cache key + own retry
// state, so a transient failure in one feed doesn't blank the
// page; the others keep rendering their cached data.

export function useLogPatternAnomalies() {
  return useQuery<LogPatternAnomaly[]>({
    queryKey: keys.anomalies.logPatterns,
    queryFn: async () => (await api.logPatternAnomalies()) ?? [],
    refetchInterval: 60_000,
    // staleTime matches refetchInterval so a re-mount inside the
    // poll window doesn't fire a duplicate refetch on top of the
    // already-scheduled background poll. Pre-v0.4.79 we had
    // staleTime=50s < refetchInterval=60s which double-fetched
    // any time the operator switched tabs near the 50-60s mark.
    staleTime: 60_000,
  });
}

export function useTraceOpAnomalies() {
  return useQuery<TraceOpAnomaly[]>({
    queryKey: keys.anomalies.traceOps,
    queryFn: async () => (await api.traceOpAnomalies()) ?? [],
    refetchInterval: 60_000,
    // staleTime matches refetchInterval so a re-mount inside the
    // poll window doesn't fire a duplicate refetch on top of the
    // already-scheduled background poll. Pre-v0.4.79 we had
    // staleTime=50s < refetchInterval=60s which double-fetched
    // any time the operator switched tabs near the 50-60s mark.
    staleTime: 60_000,
  });
}

export function useMetricAnomalies() {
  return useQuery<Problem[]>({
    queryKey: keys.anomalies.metrics,
    queryFn: async () => (await api.metricAnomalies()) ?? [],
    refetchInterval: 60_000,
    // staleTime matches refetchInterval so a re-mount inside the
    // poll window doesn't fire a duplicate refetch on top of the
    // already-scheduled background poll. Pre-v0.4.79 we had
    // staleTime=50s < refetchInterval=60s which double-fetched
    // any time the operator switched tabs near the 50-60s mark.
    staleTime: 60_000,
  });
}

// v0.10.162 — `enabled`: bant çizmeyen sayfa (servissiz pod) 60 s'lik
// zenginleştirilmiş /events sorgusunu HİÇ atmasın (fetch-on-need disiplini).
export function useAnomalyEvents(enabled = true) {
  // v0.9.465 — zarf; null gövde boş-dürüst zarfa normalize edilir.
  return useQuery<{ items: AnomalyEvent[]; activeTotal: number; clearedTotal: number; truncated: boolean }>({
    queryKey: keys.anomalies.events,
    queryFn: async () => (await api.anomalyEvents()) ?? { items: [], activeTotal: 0, clearedTotal: 0, truncated: false },
    enabled,
    refetchInterval: 60_000,
    // staleTime matches refetchInterval so a re-mount inside the
    // poll window doesn't fire a duplicate refetch on top of the
    // already-scheduled background poll. Pre-v0.4.79 we had
    // staleTime=50s < refetchInterval=60s which double-fetched
    // any time the operator switched tabs near the 50-60s mark.
    staleTime: 60_000,
  });
}

export function useAnomalySilences(enabled = true) {
  return useQuery<AnomalySilence[]>({
    queryKey: keys.anomalies.silences,
    queryFn: async () => (await api.anomalySilences()) ?? [],
    enabled,
    refetchInterval: 60_000,
    // staleTime matches refetchInterval so a re-mount inside the
    // poll window doesn't fire a duplicate refetch on top of the
    // already-scheduled background poll. Pre-v0.4.79 we had
    // staleTime=50s < refetchInterval=60s which double-fetched
    // any time the operator switched tabs near the 50-60s mark.
    staleTime: 60_000,
  });
}

// Mutations — the create / delete silence calls. Both
// invalidate the anomaly feed cache so the muted item drops
// out of the live sections on the next refresh, without us
// having to manage the optimistic state by hand.
export function useCreateAnomalySilence() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.createAnomalySilence,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: keys.anomalies.all });
    },
  });
}

// v0.10.181 — karar mutasyonu; events listesi kararı taşıdığı için anomali
// sorguları tazelenir (önbellek sunucuda da düşer).
export function usePutAnomalyVerdict() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { id: string } & Parameters<typeof api.putAnomalyVerdict>[1]) => api.putAnomalyVerdict(v.id, v),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: keys.anomalies.all });
    },
  });
}

export function useDeleteAnomalySilence() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.deleteAnomalySilence,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: keys.anomalies.all });
    },
  });
}

export function useBulkDeleteAnomalySilences() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.bulkDeleteAnomalySilences,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: keys.anomalies.all });
    },
  });
}
