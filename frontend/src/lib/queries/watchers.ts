import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';

// /watchers page hooks (v0.9.196).
//
// Summary — ONE bulk rollup for the whole list (rule_id → lastFire /
// fires24h / openNow / disabledReason). 60s poll matches the 60s
// server cache; RQ pauses interval refetches on hidden tabs
// (refetchIntervalInBackground defaults false) so the document.hidden
// rule holds. staleTime trails the interval so a re-mount inside the
// window doesn't double-fetch.
export function useWatchersSummary() {
  return useQuery({
    queryKey: ['watchers', 'summary'],
    queryFn: async () => (await api.watchersSummary()) ?? {},
    staleTime: 55_000,
    refetchInterval: 60_000,
  });
}

// History — one rule's fire/notify/resolve timeline. Fetched on
// drawer OPEN only (enabled gate), never across the list, and never
// polled — the ES-cost UI discipline shape even though this reads CH.
// staleTime ≥ the 30s server cache TTL so re-opening the same drawer
// inside the window is free.
export function useWatcherHistory(id: string | null) {
  return useQuery({
    queryKey: ['watchers', 'history', id],
    queryFn: async () => api.watcherHistory(id as string),
    enabled: !!id,
    staleTime: 30_000,
  });
}
