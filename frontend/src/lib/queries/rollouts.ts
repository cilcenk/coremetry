// queries/rollouts.ts — v0.10.201 (ROLLOUTS Faz 4). Liste/istatistik/koşu
// hook'ları: her anahtar TÜM girdileri taşır (keys.rollouts), staleTime
// sunucu TTL'iyle aynı (15 s / 60 s), refetchInterval ≥ 10 s (poll = SSE
// kayıp toleransı; `event: rollout` invalidation'ı erken tazeler —
// eventInvalidations.ts). Hidden sekmede RQ varsayılanı duraklatır.
import { useQuery } from '@tanstack/react-query';
import { api } from '../api';
import { keys } from './keys';
import type { RolloutListResponse, RolloutStats, RolloutRunsResponse } from '../types';

export interface RolloutListParams {
  from: number; to: number; // ns
  cluster?: string; namespace?: string; workload?: string; status?: string; kind?: string; limit?: number;
}

export function useRollouts(p: RolloutListParams, enabled = true) {
  return useQuery<RolloutListResponse>({
    queryKey: keys.rollouts.list(p),
    queryFn: ({ signal }) => api.rollouts(p, signal).catch((e: unknown) => {
      // 404 {disabled:true} hata değil, kapalı bayrak (entities.ts emsali)
      if (e instanceof Error && /404/.test(e.message)) return { disabled: true } as RolloutListResponse;
      throw e;
    }),
    staleTime: 15_000,
    refetchInterval: 15_000,
    enabled,
  });
}

export function useRolloutStats(p: { from: number; to: number; cluster?: string; namespace?: string; topN?: number }, enabled = true) {
  return useQuery<RolloutStats>({
    queryKey: keys.rollouts.stats(p),
    queryFn: ({ signal }) => api.rolloutStats(p, signal).catch((e: unknown) => {
      if (e instanceof Error && /404/.test(e.message)) return { disabled: true } as RolloutStats;
      throw e;
    }),
    staleTime: 60_000,
    enabled,
  });
}

export function useRolloutRuns(enabled = true) {
  return useQuery<RolloutRunsResponse>({
    queryKey: keys.rollouts.runs(),
    queryFn: ({ signal }) => api.rolloutRuns(signal),
    staleTime: 10_000,
    refetchInterval: 30_000,
    enabled,
  });
}
