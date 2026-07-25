import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import type { InboxItem } from '@/lib/types';

// Unified triage inbox (v0.5.211) — Problems + Exception groups +
// Anomaly events merged server-side with the P1/P2/P3 priority
// blend. Priority/kind chips filter client-side on the page; only
// the server-side filters participate in the key.
export function useInbox(filter: {
  status?: 'open' | 'all'; service?: string;
  ownerTeam?: string; sreTeam?: string;
  env?: string; // v0.8.387 — global picker, service-scoped (matches /problems)
  limit?: number;
}) {
  return useQuery<InboxItem[]>({
    queryKey: ['inbox', 'list', filter],
    queryFn: async () => (await api.inbox(filter)) ?? [],
  });
}

// v0.8.288 (Option B Slice 1b) — the sidebar triage badge total across all
// three inbox sources. Cheap COUNT endpoint; 30s poll (React Query pauses it
// on a hidden tab), 25s stale to match. `select` narrows to the number so the
// badge consumer stays a plain count.
// v0.9.219 — env joins the key AND the request. useInbox() already took env;
// this one didn't, so with an env picked the sidebar badge counted every
// environment while the page behind it showed one.
export function useInboxCount(env?: string) {
  return useQuery<{ count: number; problems: number; exceptions: number; anomalies: number }, Error, number>({
    queryKey: ['inbox', 'count', env ?? ''],
    queryFn: async () => (await api.inboxCount(env)) ?? { count: 0, problems: 0, exceptions: 0, anomalies: 0 },
    select: (r) => r.count,
    refetchInterval: 30_000,
    staleTime: 25_000,
  });
}
