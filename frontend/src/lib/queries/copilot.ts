import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import type { CopilotStarter } from '@/lib/types';

// queries/copilot.ts — v0.10.702. Boş sohbetin veri çipleri: YALNIZ çekmece
// açık + sohbet boşken (aç-üzerine-getir; liste prefetch'i yok, poll yok).
// staleTime sunucu cache'iyle aynı (60 s). Hata → boş liste: statik çipler
// zaten var, veri çipi bir ek.
export function useCopilotStarters(enabled: boolean, rangeS = 3600): CopilotStarter[] {
  const q = useQuery({
    queryKey: ['copilot', 'starters', rangeS],
    queryFn: ({ signal }) => api.copilotStarters(rangeS, signal),
    enabled,
    staleTime: 60_000,
    retry: false,
  });
  return q.data?.starters ?? [];
}
