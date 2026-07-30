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
  // v0.9.318 — "there were more CANDIDATES than the server looked at", which
  // is a different statement from `truncated` ("more matches than it
  // returned"). Under a search the second can be false while the first is
  // true, and that combination is exactly when an empty table must not be
  // read as an empty queue. Optional so a pre-upgrade cached body still
  // deserializes.
  scanCapped?: boolean;
  // v0.9.320 — the applied occurrence floor and what it hid. Counted AFTER
  // every other narrow, so it means "rows that passed everything else and
  // failed only the floor" — an inflated hidden count is its own lie.
  minOcc?: number;
  hiddenByMinOcc?: number;
  // v0.9.330 — facet totals computed server-side over the pre-facet, pre-cap
  // set. The chips MUST render from these: counting the returned page is what
  // made prod show "Exceptions 0" on a queue holding thousands of them.
  counts?: Record<string, number>;
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
  sort?: string; dir?: 'asc' | 'desc'; // v0.9.319 — server-side ranking
  minOcc?: number; // v0.9.320 — occurrence floor (0 = show all)
  // v0.9.330 — kind/priority are SERVER filters now: they decide which rows
  // come back, so they must bite before the cap.
  kind?: string; prio?: string;
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
// v0.9.442 — rozet Dynatrace şekline döner: manşet sayı yalnız
// problems+anomalies+incidents; exception grupları AYRI sayıdır ve
// Exceptions menü girişinde sönük rozet olarak görünür. Prod'da 3.1K
// canlı exception grubu manşeti 3607'ye şişiriyordu — sayı triage
// sinyali olmaktan çıkmıştı. Sunucu kırılımı zaten döndürüyor; toplam
// `count` alanı bilerek OKUNMUYOR (dört türün toplamı, eski semantik).
export function useInboxCount(env?: string) {
  return useQuery<
    { count: number; problems: number; exceptions: number; anomalies: number; incidents: number },
    Error,
    { triage: number; exceptions: number }
  >({
    queryKey: keys.inbox.count(env),
    queryFn: async () =>
      (await api.inboxCount(env)) ?? { count: 0, problems: 0, exceptions: 0, anomalies: 0, incidents: 0 },
    select: (r) => ({
      triage: (r.problems ?? 0) + (r.anomalies ?? 0) + (r.incidents ?? 0),
      exceptions: r.exceptions ?? 0,
    }),
    refetchInterval: 30_000,
    staleTime: 25_000,
  });
}
