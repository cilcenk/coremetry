import { keepPreviousData, useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { keys } from './keys';
import type { InboxItem } from '@/lib/types';

// v0.9.221 — one page of the triage queue. `total` is the pre-cap count, so
// the UI can say "200 of 912" instead of implying it showed everything.
export type InboxPage = {
  items: InboxItem[];
  total: number;
  limit: number;
  truncated: boolean;
};

// Unified triage inbox (v0.5.211) — Problems + Exception groups +
// Anomaly events merged server-side with the P1/P2/P3 priority
// blend. Priority/kind chips filter client-side on the page; only
// the server-side filters participate in the key.
export function useInbox(filter: {
  status?: 'open' | 'all' | 'ignored'; service?: string;
  q?: string; // v0.9.251 — free-text search (service + title + source)
  ownerTeam?: string; sreTeam?: string;
  env?: string; // v0.8.387 — global picker, service-scoped (matches /problems)
  limit?: number;
}) {
  return useQuery<InboxPage>({
    queryKey: ['inbox', 'list', filter],
    // v0.9.221 — the endpoint returns { items, total, truncated } now; a null
    // body normalises to an empty, non-truncated page so callers never branch
    // on undefined.
    queryFn: async () => (await api.inbox(filter))
      ?? { items: [], total: 0, limit: 0, truncated: false },
    // v0.9.220 — the list had NO polling while the sidebar badge above it
    // refreshed every 30s, so a new problem bumped the badge to 48 while the
    // rows underneath stayed at 47 until a manual reload: the one surface
    // that is supposed to be the operator's live queue was the stalest thing
    // on screen. 30s/25s matches the badge and /problems exactly, so the two
    // can't drift apart again. React Query pauses refetchInterval on hidden
    // tabs, satisfying the document.hidden house rule.
    refetchInterval: 30_000,
    staleTime: 25_000,
    // Keep the previous page on screen while a filter change refetches —
    // a triage list that blinks to a skeleton on every chip click reads as
    // slower than it is (the v0.8.478/479 keep-data pattern).
    placeholderData: keepPreviousData,
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
    queryKey: keys.inbox.count(env),
    queryFn: async () => (await api.inboxCount(env)) ?? { count: 0, problems: 0, exceptions: 0, anomalies: 0 },
    select: (r) => r.count,
    refetchInterval: 30_000,
    staleTime: 25_000,
  });
}
