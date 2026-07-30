import { useMemo, useRef, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { Button } from '@/components/ui';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { Users, Shield } from 'lucide-react';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { TableSkeleton } from '@/components/Skeleton';
import { useInbox, useServicesMetadata } from '@/lib/queries';
import { tsLong, fmtFixed, fmtAgoNs } from '@/lib/utils';
import { teamOptionsCI } from '@/lib/teamOptions';
import { decodeCsvSet, encodeCsvSet } from '@/lib/inboxUrl';
import { useUrlEnv } from '@/lib/useUrlEnv';
import { useDataTable, DataTableHead, DataTableColgroup, resolveInitialSort } from '@/components/DataTable';
import { FacetMultiSelect } from '@/components/ui/FacetMultiSelect';
import { InboxTriageDrawer } from '@/components/InboxTriageDrawer';
import { SavedViewsBar } from '@/components/SavedViewsBar';
import { resolveSelectedItem } from '@/lib/inboxDrawer';
import type { DataTableColumn } from '@/lib/dataTable';
import type { InboxItem, InboxKind } from '@/lib/types';

// Facet vocab + defaults (v0.8.291) — the inbox lands on P1+P2 across all
// kinds; both are the "default" the URL codec omits so a fresh link stays clean.
const PRIO_ALL = ['P1', 'P2', 'P3'] as const;
const PRIO_DEFAULT = ['P1', 'P2'] as const;
const KIND_ALL: readonly InboxKind[] = ['problem', 'exception', 'anomaly', 'incident'];
// v0.9.328 — operator: "Problems ilk açtığında exception görsün, kullanıcılar
// ona göre tasarlar." Exceptions are the signal operators trust: a thrown
// exception is a fact from the code, not an inference from a threshold.
//
// This is a DEFAULT, not a restriction. All four chips stay on screen with
// their full counts, so what's excluded is visible and one click away, and
// ?kind= carries the choice into every shared link. The P1 guard below makes
// sure a landing default can never hide something urgent.
const KIND_DEFAULT: readonly InboxKind[] = ['exception'];
const KIND_LABEL: Record<InboxKind, string> = {
  problem: 'Problems', exception: 'Exceptions',
  anomaly: 'Anomalies', incident: 'Incidents',
};
// v0.9.254 — 'ignored' yalnızca exception gruplarını gösterir (problem'ler
// MUTE'lanır, anomaliler SILENCE'lanır — farklı fiiller, farklı state).
// Ayrı bir pivot olması bilinçli: susturmak kasıtlı bir eylem, o satırları
// günlük 'all' görünümüne geri dökmek operatörün sustuğu gürültüyü geri
// getirirdi.
type InboxStatus = 'open' | 'all' | 'ignored';
const STATUS_PIVOTS: readonly InboxStatus[] = ['open', 'all', 'ignored'];

// /inbox — unified triage view (v0.5.211). Merges Problems +
// Exception groups + Anomaly events server-side with a normalised
// P1/P2/P3 priority blend so operators get "everything needing a
// human" in one place instead of tab-hopping between three pages.
//
// A row click opens an in-place triage drawer (v0.8.292, Option B
// slice 3): root-cause ribbon + inline ack/assign/mute, without
// leaving /inbox. The drawer keeps an "Open source →" deep-link
// escape hatch to the per-source workspaces, which still exist.

const PRIO_RANK: Record<string, number> = { P1: 3, P2: 2, P3: 1 };

// Columns for the shared sortable + resizable DataTable. Default sort is
// priority desc (P1 first); rows are pre-sorted by lastSeen desc so the
// stable sort yields "P1 first, newest within priority" — the prior
// fixed ordering, now re-sortable + resizable per column.
const INBOX_COLS: DataTableColumn<InboxItem>[] = [
  { id: 'priority', label: 'Priority', sortValue: it => PRIO_RANK[it.priority] ?? 0, naturalDir: 'desc', width: 80 },
  { id: 'source',   label: 'Source',   sortValue: it => it.source,           naturalDir: 'asc', width: 100 },
  { id: 'service',  label: 'Service',  sortValue: it => it.service,          naturalDir: 'asc', width: 190 },
  { id: 'detail',   label: 'Detail',   sortValue: it => it.title,            naturalDir: 'asc', width: 380 },
  // v0.9.331 — Occurrences is back. The operator insisted on keeping this
  // column on /problems (v0.9.315: "ben occurences kolonu kalkmasını
  // istemedim"); when the merged queue took over the Problems name in
  // v0.9.323 the column did not come with it. On a triage list the number
  // answers the question the row cannot: is this sustained, or did it fire
  // once. It sits next to Detail because that is the comparison the eye makes.
  { id: 'occurrences', label: 'Occurrences',
    sortValue: it => it.exception?.occurrences ?? 0,
    naturalDir: 'desc', numeric: true, width: 110 },
  // v0.9.333 — First seen is its own column again (operator: "First seen ayrı
  // kolon olabilir, öyleydi"). It was folded into the Last seen cell as a
  // conditional "· ilk …" suffix, which meant it vanished whenever the two
  // timestamps matched and could never be sorted on. "What started first" is
  // the question that orders a cascade; "what fired last" is a different one.
  { id: 'firstSeen', label: 'First seen', sortValue: it => it.startedAt, naturalDir: 'desc', width: 150 },
  { id: 'lastSeen', label: 'Last seen', sortValue: it => it.lastSeen,        naturalDir: 'desc', width: 170 },
  { id: 'assignee', label: 'Assignee', sortValue: it => it.assignee ?? '',   naturalDir: 'asc', width: 150 },
];

export default function InboxPage() {
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  // "/" focuses this filter via the shared table keyboard-nav.
  const searchRef = useRef<HTMLInputElement>(null);

  // URL = source of truth (v0.8.291, Option B Slice 2). Every facet/filter is
  // derived straight from the query string — no useState mirror, so the
  // one-way-read bug class (v0.8.256/265/267) can't exist here, and Copy link
  // reproduces the exact triage view. Defaults (P1+P2, all kinds) are the codec
  // fallback for an absent param, so a bare /inbox lands on the intended view.
  // v0.9.254 — 'ignored' pivotu eklendi. Susturulmuş exception grupları
  // BAŞKA hiçbir pivotta görünmüyordu (store varsayılanı da 'all' da onları
  // eliyor), ve bunları geri açan tek yüzey /problems'ın Ignored sekmesiydi.
  // O sayfa kalkarsa susturma kalıcı ve geri alınamaz hale gelirdi.
  const statusFilter: InboxStatus =
    searchParams.get('status') === 'all' ? 'all'
      : searchParams.get('status') === 'ignored' ? 'ignored'
        : 'open';
  const rawPrio = searchParams.get('prio');
  const rawKind = searchParams.get('kind');
  const prioSet = useMemo(() => new Set(decodeCsvSet(rawPrio, PRIO_ALL, PRIO_DEFAULT)), [rawPrio]);
  const kindSet = useMemo(
    () => new Set(decodeCsvSet(rawKind, KIND_ALL, KIND_DEFAULT) as InboxKind[]),
    [rawKind]);
  const serviceFilter = searchParams.get('service') ?? '';
  // v0.9.251 — the search box drives `q` (service + title + source),
  // not `service`. `service` is still read so an older shared link
  // keeps narrowing exactly as it did; the two AND together
  // server-side.
  const searchFilter = searchParams.get('q') ?? '';
  // v0.9.252 — bulk triage selection. Deliberately NOT in the URL: a
  // shared link should reproduce the VIEW, not a half-finished
  // multi-select someone else is about to act on. Named `picked` to
  // stay clear of `selected`, which is the DRAWER's row (?item=).
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [bulkBusy, setBulkBusy] = useState(false);
  const [bulkNote, setBulkNote] = useState<string | null>(null);
  const qc = useQueryClient();
  const ownerFilter = searchParams.get('owner') ?? '';
  const sreFilter = searchParams.get('sre') ?? '';
  // v0.9.320 — occurrence floor, same semantics and same default as
  // /problems (v0.9.315) so the two surfaces can't disagree about what
  // counts as noise. URL-borne like every other facet here, so a saved view
  // and a shared link carry the threshold too.
  //
  // NOT silent: the strip below states the floor, says how many rows it hid
  // (the server counts them AFTER every other narrow, so the number is the
  // honest one), and "show all" is a single click. A rare exception that
  // fired once can still be the important one.
  const minOcc = (() => {
    const raw = searchParams.get('minOcc');
    if (raw === null) return 5;
    const n = Number(raw);
    return Number.isFinite(n) && n >= 0 ? n : 5;
  })();
  const setMinOcc = (v: number) => setSearchParams(prev => {
    const next = new URLSearchParams(prev);
    if (v === 5) next.delete('minOcc'); else next.set('minOcc', String(v));
    return next;
  }, { replace: true });
  // Drawer selection is one more URL-backed facet (v0.8.292): ?item=<inboxId>.
  // Deep-linking /inbox?item=<id> opens the drawer; closing deletes the key.
  const selectedId = searchParams.get('item');

  // Single URL writer — replace:true + copy prev so foreign params (a future
  // ?problem= drawer, range, etc.) survive. Empty/null deletes the key.
  const setParam = (k: string, v: string | null) => {
    setSearchParams(prev => {
      const p = new URLSearchParams(prev);
      if (v === null || v === '') p.delete(k); else p.set(k, v);
      return p;
    }, { replace: true });
  };
  const setStatusFilter = (s: InboxStatus) => setParam('status', s === 'open' ? null : s);
  // Multi-select toggles keep the min-1 invariant (can't deselect everything),
  // then serialise the set back to the URL (default selection → param removed).
  const togglePrio = (p: string) => {
    const next = new Set(prioSet);
    if (next.has(p)) { if (next.size === 1) return; next.delete(p); } else next.add(p);
    setParam('prio', encodeCsvSet(next, PRIO_ALL, PRIO_DEFAULT));
  };
  const toggleKind = (k: InboxKind) => {
    const next = new Set(kindSet);
    if (next.has(k)) { if (next.size === 1) return; next.delete(k); } else next.add(k);
    setParam('kind', encodeCsvSet(next, KIND_ALL, KIND_DEFAULT));
  };
  // v0.9.356 — "sadece" (1 tıkla izole; lejantların isolate jestiyle aynı)
  // ve grup başına "tümü". Solo, toggle'ın aksine tek değere İNER; tümü,
  // grubun tamamını seçer. İkisi de URL'e aynı codec'le yazar.
  const soloPrio = (p: string) => setParam('prio', encodeCsvSet(new Set([p]), PRIO_ALL, PRIO_DEFAULT));
  const allPrio = () => setParam('prio', encodeCsvSet(new Set(PRIO_ALL), PRIO_ALL, PRIO_DEFAULT));
  const soloKind = (k: InboxKind) => setParam('kind', encodeCsvSet(new Set([k]), KIND_ALL, KIND_DEFAULT));
  const allKind = () => setParam('kind', encodeCsvSet(new Set(KIND_ALL), KIND_ALL, KIND_DEFAULT));
  const setServiceFilter = (v: string) => setParam('service', v || null);
  const setSearchFilter = (v: string) => setParam('q', v || null);
  const setOwnerFilter = (v: string) => setParam('owner', v || null);
  const setSreFilter = (v: string) => setParam('sre', v || null);
  // v0.9.341 — an EXCEPTION row opens its full detail page, not the drawer.
  //
  // Operator: "Eskiden occurrences görürdüm, şimdi göremiyorum exception'a
  // tıkladığımda. Ayrı sayfa açılıyordu, hatta onu istiyorum."
  //
  // This is a rejected pattern coming back by the side door. A drawer for
  // exception detail was tried in v0.9.508 and REVERTED in v0.9.510: the
  // standing decision is that exception detail is a FULL PAGE in the classic
  // v0.9.425 layout. The merged triage queue (v0.9.323) then routed every
  // row — exceptions included — through the in-place drawer, which shows a
  // count and three buttons. The occurrences-over-time chart, the stack trace
  // and the sample traces all live on the page, so clicking the row lost
  // exactly what the operator opens an exception to see.
  //
  // Other kinds keep the drawer: it exists so a Problem or an anomaly can be
  // acked without leaving the queue (v0.8.292), and neither has a richer
  // destination that the drawer is hiding.
  const openDrawer = (it: InboxItem) => {
    if (it.kind === 'exception' && it.exception) {
      navigate(`/problems?exc=${encodeURIComponent(it.exception.fingerprint)}`);
      return;
    }
    setParam('item', it.id);
  };
  const closeDrawer = () => setParam('item', null);

  // Global env picker (v0.8.387) — service-scoped: the server keeps
  // rows whose service ran in the env in the last hour (+ service-
  // less global alerts), same semantics as /problems. Hint chip in
  // the facet bar spells it out.
  const [env] = useUrlEnv();
  // v0.9.319 — the sort now runs on the SERVER, before the cap. Seeded from
  // the same precedence useDataTable restores with (resolveInitialSort), so
  // the first fetch and the header arrow agree at mount instead of the page
  // showing a lastSeen arrow over a priority-ranked response. onSortChange
  // below keeps them in step afterwards.
  const [srvSort, setSrvSort] = useState(() =>
    resolveInitialSort('inbox', new URLSearchParams(window.location.search).get('s_inbox'),
      { id: 'priority', dir: 'desc' as const }));

  const inboxQ = useInbox({
    status: statusFilter,
    service: serviceFilter || undefined,
    q: searchFilter || undefined,
    ownerTeam: ownerFilter || undefined,
    sreTeam: sreFilter || undefined,
    env: env || undefined,
    limit: 300,
    sort: srvSort.id ?? 'priority',
    dir: srvSort.dir,
    minOcc,
    // v0.9.330 — facets go to the SERVER now. As client-side filters they ran
    // over a page the server had already capped at 300 by priority; on prod
    // those 300 were all Incidents, so the exception-first default rendered
    // "Queue clear" over 2,144 real items.
    kind: [...kindSet].join(','),
    prio: [...prioSet].join(','),
  });
  const data: InboxItem[] | null | undefined =
    inboxQ.isPending ? undefined : inboxQ.isError ? null : inboxQ.data?.items ?? [];
  // v0.9.221 — the server caps the queue; say so rather than letting 300 rows
  // pass for the whole thing. Sorted priority-desc, so what fell off is the
  // low-priority tail — exactly the part an operator would assume was empty.
  const capped = !inboxQ.isPending && !inboxQ.isError && inboxQ.data?.truncated
    ? { shown: inboxQ.data.items.length, total: inboxQ.data.total }
    : null;
  // v0.9.318 — the scan itself hit its ceiling, so the filters above ran over
  // a slice of the candidates rather than all of them. Worth saying ONLY when
  // a filter is actually active: unfiltered, the priority-ranked page is the
  // honest top of the queue and `capped` already covers it. Filtered, silence
  // would let "no results" mean "none exist".
  const anyFilter = !!(serviceFilter || searchFilter || ownerFilter || sreFilter || env);
  const scanCapped = !inboxQ.isPending && !inboxQ.isError
    && !!inboxQ.data?.scanCapped && anyFilter;
  const hiddenByMinOcc = inboxQ.data?.hiddenByMinOcc ?? 0;
  // v0.9.353 (operator-reported) — the rows on screen belong to the PREVIOUS
  // filter while the new one fetches. useInbox keeps previous data
  // (placeholderData: keepPreviousData) so a filter change doesn't blank the
  // page — but nothing SAID so. On prod the owner-filtered request was slow
  // enough that the old, unfiltered rows just sat there and the operator
  // reasonably concluded "the owner filter does not work". Stale data may be
  // shown; it may not masquerade as the answer.
  const showingStale = inboxQ.isPlaceholderData && inboxQ.isFetching;
  // v0.9.328 — the landing default shows Exceptions only, so this is the
  // guard that keeps a DEFAULT from becoming a blindfold: if a kind the
  // operator isn't looking at is carrying a P1, say so on the page instead of
  // trusting them to notice a chip count. A triage surface may narrow by
  // default; it may not hide an emergency by default.
  // v0.9.330 — derived from the SERVER counts. The page no longer carries the
  // deselected kinds at all (the server filters before the cap), so scanning
  // `data` for them would always find zero and the guard would go quiet
  // exactly when it is needed. The server reports per-kind totals but not
  // per-kind-per-priority, so this reads "kinds you are not looking at that
  // have rows at all" — deliberately conservative: over-warning is survivable,
  // staying silent about an unattended kind is not.
  const hiddenP1 = useMemo(() => {
    const c = inboxQ.data?.counts;
    if (!c) return {} as Record<string, number>;
    const out: Record<string, number> = {};
    for (const k of KIND_ALL) {
      if (kindSet.has(k)) continue;
      const n = c[k] ?? 0;
      if (n > 0) out[k] = n;
    }
    return out;
  }, [inboxQ.data, kindSet]);
  const hiddenP1Total = Object.values(hiddenP1).reduce((a, b) => a + b, 0);
  // Naming note (v0.9.330): this counts ROWS in unselected kinds, not P1s
  // specifically — the server reports per-kind totals, not a kind×priority
  // matrix. The label says "kalem" for that reason; claiming "P1" would be a
  // number that means something other than what it says.

  // The drawer's selected row, resolved from ?item= against the loaded list.
  // Uses the full (pre-facet) list so a deep-link to a row hidden by the
  // priority/kind filter still opens; undefined when the id isn't present →
  // the drawer shows a soft fallback, never a blank panel.
  const selected = useMemo(() => resolveSelectedItem(data, selectedId), [data, selectedId]);

  // v0.9.330 — the server already applied these (before the cap, which is the
  // whole point). Kept as a belt-and-braces pass so a stale cached page from a
  // pre-upgrade pod can't render rows the operator deselected — it must never
  // be the ONLY place the facet is applied again.
  const filtered = useMemo(() => {
    if (!data) return data;
    return data.filter(it =>
      prioSet.has(it.priority) &&
      kindSet.has(it.kind));
  }, [data, prioSet, kindSet]);

  // Deep-link into the source surface with the specific row
  // focused — Problems drawer for problems, expanded exception
  // group, scrolled-to anomaly history row. Each destination
  // page reads its respective query param on mount.
  const goToSource = (it: InboxItem) => {
    if (it.kind === 'problem' && it.problem) {
      navigate(`/problems?problem=${encodeURIComponent(it.problem.id)}`);
    } else if (it.kind === 'exception' && it.exception) {
      navigate(`/problems?tab=open&exception=${encodeURIComponent(it.exception.fingerprint)}`);
    } else if (it.kind === 'anomaly' && it.anomaly) {
      navigate(`/anomalies?event=${encodeURIComponent(it.anomaly.id)}`);
    } else if (it.kind === 'incident' && it.incident) {
      // The incident DETAIL route (/incident?id=), not the list — the row
      // already told the operator the incident exists; the click is a request
      // for the response timeline.
      navigate(`/incident?id=${encodeURIComponent(it.incident.id)}`);
    }
  };

  // Shared sortable + resizable table. Pre-sort by lastSeen desc so the
  // primitive's stable priority-desc sort reproduces the prior fixed
  // ordering (P1 first, newest within priority). Hook is unconditional.
  // onOpen + searchRef wire the app-wide keyboard nav (j/k select,
  // Enter/o open the source surface, "/" focuses the filter).
  // v0.9.319 — the lastSeen pre-sort is GONE. It existed to make the
  // primitive's client-side stable sort reproduce "P1 first, newest within
  // priority"; the server now ranks the whole candidate set before capping,
  // so re-ordering here would corrupt the order the cap was applied in.
  const inboxRows = filtered ?? [];
  const dt = useDataTable<InboxItem>({
    storageKey: 'inbox',
    columns: INBOX_COLS,
    rows: inboxRows,
    initialSort: { id: 'priority', dir: 'desc' },
    // serverSort: the ORDER BY happens in the handler over the FULL candidate
    // set, so sortedRows is the response verbatim. Client-side sorting would
    // rank the page — the same lie the filters told before v0.9.318.
    serverSort: true,
    onSortChange: setSrvSort,
    onOpen: openDrawer,
    searchRef,
  });

  // Selection resolves against the rows actually on screen, so a row
  // that got resolved out from under the operator stops being counted
  // as acknowledgeable.
  const pickedRows = useMemo(
    () => (filtered ?? []).filter((r: InboxItem) => picked.has(r.id)),
    [filtered, picked]);
  const selCount = pickedRows.length;
  const ackProblemIds = pickedRows
    .filter((r: InboxItem) => r.kind === 'problem' && r.problem)
    .map((r: InboxItem) => r.problem!.id);
  const ackExceptionFps = pickedRows
    .filter((r: InboxItem) => r.kind === 'exception' && r.exception)
    .map((r: InboxItem) => r.exception!.fingerprint);
  // v0.9.323 — incidents joined the queue in v0.9.321 but not the bulk
  // action: their rows got a checkbox, select-all included them, and then
  // Onayla silently did nothing for them while the counter reported them as
  // "anomali atlanacak" — naming the wrong kind. Acknowledging an incident is
  // exactly what bulk ack means ("I'm on it"), and the endpoint already
  // exists, so they are ackable rather than excluded.
  const ackIncidentIds = pickedRows
    .filter((r: InboxItem) => r.kind === 'incident' && r.incident)
    .map((r: InboxItem) => r.incident!.id);
  const ackable = ackProblemIds.length + ackExceptionFps.length + ackIncidentIds.length;
  const skipped = selCount - ackable;
  const allPickedOnScreen = (filtered?.length ?? 0) > 0
    && (filtered ?? []).every((r: InboxItem) => picked.has(r.id));

  const toggleRow = (id: string) => setPicked(prev => {
    const next = new Set(prev);
    if (next.has(id)) next.delete(id); else next.add(id);
    return next;
  });

  const bulkAcknowledge = async () => {
    setBulkBusy(true); setBulkNote(null);
    // Both calls go out before either result is inspected: they hit
    // different stores, and a failure in one must not cancel the other
    // half of the operator's gesture.
    const [pRes, eRes, iRes] = await Promise.allSettled([
      ackProblemIds.length ? api.acknowledgeProblems(ackProblemIds) : Promise.resolve(null),
      ackExceptionFps.length ? api.setExceptionGroupStatesBulk(ackExceptionFps, 'acknowledged') : Promise.resolve(null),
      // No bulk incident endpoint — one POST each, settled together so a
      // single failure doesn't discard the rest of the gesture.
      ackIncidentIds.length
        ? Promise.allSettled(ackIncidentIds.map(id => api.ackIncident(id)))
        : Promise.resolve(null),
    ]);
    const okProblems = pRes.status === 'fulfilled' ? ackProblemIds.length : 0;
    const okExceptions = eRes.status === 'fulfilled'
      ? ((eRes.value as { applied: number } | null)?.applied ?? 0)
      : 0;
    const incResults = iRes.status === 'fulfilled'
      ? ((iRes.value as PromiseSettledResult<unknown>[] | null) ?? [])
      : [];
    const okIncidents = incResults.filter(r => r.status === 'fulfilled').length;
    const failedIncidents = incResults.length - okIncidents;
    const errs = [pRes, eRes].filter(r => r.status === 'rejected').length + failedIncidents;
    const ok = okProblems + okExceptions + okIncidents;
    setBulkNote(errs
      ? `${ok} onaylandı, ${errs} istek başarısız`
      : `${ok} onaylandı`);
    setPicked(new Set());
    // The server already dropped the cached list + badge; refetch so
    // the rows leave the screen instead of lingering a poll cycle.
    qc.invalidateQueries({ queryKey: ['inbox'] });
    setBulkBusy(false);
  };

  // v0.9.330 — chips render SERVER counts, computed over the pre-facet,
  // pre-cap set. Counting the returned page is exactly how prod displayed
  // "Exceptions 0" on a queue that held thousands: the page was 300 incidents,
  // so every other chip read zero and the numbers argued the queue was empty.
  // Falls back to page-derived counts only while no server payload exists yet.
  const counts = useMemo(() => {
    const fromServer = inboxQ.data?.counts;
    if (fromServer) return fromServer;
    const out: Record<string, number> = { P1: 0, P2: 0, P3: 0,
      problem: 0, exception: 0, anomaly: 0, incident: 0 };
    for (const it of data ?? []) {
      out[it.priority] = (out[it.priority] ?? 0) + 1;
      out[it.kind] = (out[it.kind] ?? 0) + 1;
    }
    return out;
  }, [data, inboxQ.data]);

  // v0.9.355 (operator-reported) — dropdown options come from the CATALOG,
  // not from the filtered rows.
  //
  // They used to be derived from `data` — the response of a request that
  // already carried the team filter. Pick Looki and the response holds only
  // Looki's rows, so the dropdown collapsed to "all + Looki": switching to
  // another team meant going back to "all" first, waiting for the (big)
  // unfiltered list, then picking again. The old comment here claimed the
  // opposite ("opening the dropdown again still shows the remaining teams"),
  // which stopped being true the moment the filter moved server-side — and
  // v0.9.353's SQL narrow made the collapse total.
  //
  // useServicesMetadata is the same catalog read the /services and
  // exceptions pages feed their team dropdowns from (React Query dedups it,
  // 60s staleTime) — no new endpoint, and all three surfaces now agree on
  // what teams exist.
  const catalogQ = useServicesMetadata();
  const { ownerOptions, sreOptions } = useMemo(() => {
    const mds = Object.values(catalogQ.data ?? {}) as import('@/lib/types').ServiceMetadata[];
    return {
      // v0.8.330 — case-insensitive dedup, same rule everywhere.
      ownerOptions: teamOptionsCI(mds.map(md => md.ownerTeam)),
      sreOptions:   teamOptionsCI(mds.map(md => md.sreTeam)),
    };
  }, [catalogQ.data]);

  return (
    <>
      {/* v0.9.323 — triage merge: this page IS the queue now, so it carries
          the name. The route stays /inbox so saved views, notification deep
          links and dashboard markdown keep resolving — only the label moved. */}
      <Topbar title="Problems" showEnv />
      <div id="content">
        {/* v0.9.255 — kayıtlı görünümler. Backend `page`'i serbest string alıyor,
            yani bu tek satır; birleşik triage yüzeyinin /problems'ın yerini
            tutabilmesi için gereken paritenin en ucuz parçası.
            #content'in İÇİNDE — Traces/Explore ile aynı yerleşim; dışarıda
            konteyner padding'i almaz. */}
        <SavedViewsBar page="inbox" />
        <p style={{ color: 'var(--text2)', fontSize: 13, marginBottom: 14 }}>
          Everything needing a human — Problems (alert rules), open Exception
          groups, and active Anomaly detections. Default view: <b>P1 + P2</b>
          across all kinds. Click any row to triage it in place.
        </p>

        {/* One grouped facet bar (v0.8.38) — status pivot + priority + kind
            chips share the shared .facet primitive (repo equivalent of the
            design's .logbar/.facet). Handlers + state are unchanged: status
            pivot single-select via setStatusFilter, priority/kind multi-
            select via togglePrio/toggleKind. Team selects + service filter
            stay pushed right with margin-left:auto. */}
        <div className="facetbar">
          {/* Status pivot — single-select. v0.9.356: 'ignored' "All" diye
              etiketleniyordu (ternary yalnız 'open'ı ayırıyordu), yani
              çubukta İKİ "All" vardı ve biri susturulmuş exception'ları
              açıyordu. Operatörün "iki anlamı belirsiz All" gözlemi düpedüz
              etiket hatasıydı. */}
          <span className="facet-grp">
            <span className="gl">Durum</span>
            {STATUS_PIVOTS.map(s => (
              <span key={s} onClick={() => setStatusFilter(s)}
                className={`facet${statusFilter === s ? ' on' : ''}`}>
                {s === 'open' ? 'Open / Active' : s === 'all' ? 'All' : 'Ignored'}
              </span>
            ))}
          </span>

          {/* v0.9.357 (mockup C, operatör: "C daha uygun bizim kuruma") —
              çip grupları sayılı multi-select dropdown'lara döndü; Owner/SRE
              select'leriyle tek dil. v0.9.356'nın "sadece"/"tümü" jestleri
              panelin içinde yaşıyor; sayılar da orada — yoğunluk bir tık
              arkaya taşındı, kaybolmadı. */}
          <FacetMultiSelect label="Öncelik"
            options={(['P1', 'P2', 'P3'] as const).map(pp => ({
              value: pp, label: pp, count: counts[pp] ?? 0 }))}
            selected={prioSet}
            onToggle={togglePrio} onSolo={soloPrio} onAll={allPrio} />

          <FacetMultiSelect label="Tür"
            options={KIND_ALL.map(k => ({
              value: k, label: KIND_LABEL[k], count: counts[k] ?? 0 }))}
            selected={kindSet as Set<string>}
            onToggle={k => toggleKind(k as InboxKind)}
            onSolo={k => soloKind(k as InboxKind)} onAll={allKind} />

          {/* Env hint chip (v0.8.387) — non-interactive; the pick lives
              in the Topbar EnvPicker. Surfaces the service-scoped
              semantics so the operator doesn't read rows as per-env. */}
          {env && (
            <span className="badge b-info" style={{ cursor: 'help' }}
              title={`Showing items on services seen in "${env}" during the last hour (global environment picker). Triage rows carry no environment of their own — a row on a multi-env service still shows, and service-less (global) alerts always show.`}>
              env: {env} — service-scoped
            </span>
          )}

          <span style={{ marginLeft: 'auto' }} />

          {/* Team filters (v0.5.234). Distinct values come from
              the current result set so an operator can stack
              owner + SRE narrows without losing the option list.
              Empty option = "all". Server-side filter (no client
              re-fetch shaping) so the result count drops
              accurately as the operator narrows. */}
          <select value={ownerFilter}
            onChange={e => setOwnerFilter(e.target.value)}
            title="Filter by service.ownerTeam"
            style={{ fontSize: 12, padding: '4px 8px', minWidth: 130 }}>
            <option value="">Owner: all</option>
            {ownerOptions.map(o => <option key={o} value={o}>{o}</option>)}
          </select>
          <select value={sreFilter}
            onChange={e => setSreFilter(e.target.value)}
            title="Filter by service.sreTeam"
            style={{ fontSize: 12, padding: '4px 8px', minWidth: 130 }}>
            <option value="">SRE: all</option>
            {sreOptions.map(o => <option key={o} value={o}>{o}</option>)}
          </select>

          {/* v0.9.251 — one box, matched SERVER-side across service +
              title + source. Title was the gap: with thousands of open
              items an operator could narrow to a service but never to
              "timeout" or "OOMKilled". Client-side filtering wouldn't
              have worked either — the list is a server-capped top slice,
              so a term present only on row 900 would never arrive. */}
          <input ref={searchRef} value={searchFilter}
            onChange={e => setSearchFilter(e.target.value)}
            placeholder="Search service, title, source…"
            title="Case-insensitive substring match across the service, the title and the source label."
            style={{ fontSize: 12, padding: '4px 8px', minWidth: 220 }} />
          {serviceFilter && (
            <span style={{ display: 'inline-flex', alignItems: 'center', gap: 5, fontSize: 11, padding: '3px 8px', borderRadius: 11, background: 'var(--bg3)', border: '1px solid var(--border)' }}
              title="Service filter carried in the URL (?service=). Narrows on top of the search box.">
              service: {serviceFilter}
              <button type="button" title="Remove" onClick={() => setServiceFilter('')}
                style={{ background: 'transparent', border: 'none', color: 'var(--text3)', cursor: 'pointer', padding: 0, fontSize: 12, lineHeight: 1 }}>×</button>
            </span>
          )}
        </div>

        {/* v0.9.252 — bulk acknowledge. Appears only with a selection so
            the toolbar above stays quiet during normal triage.

            Scope is explicit rather than silent: problems and exception
            groups both have an "acknowledged" state and are sent to
            their respective BULK endpoints (one request, one audit row
            each); anomalies have no ack — they are silenced, which is a
            different decision — so they are counted out loud instead of
            being dropped without a word. */}
        {selCount > 0 && (
          <div style={{
            display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap',
            padding: '8px 12px', marginBottom: 8, borderRadius: 6,
            background: 'var(--bg2)', border: '1px solid var(--accent)',
          }}>
            <b style={{ fontSize: 12 }}>{selCount} seçili</b>
            <span style={{ fontSize: 11, color: 'var(--text3)' }}>
              {ackable} onaylanabilir
              {/* Only anomalies are unackable now (v0.9.323) — problems,
                  exception groups and incidents all have an ack path. */}
              {skipped > 0 && ` · ${skipped} anomali atlanacak (onay değil, susturma gerektirir)`}
            </span>
            <span style={{ marginLeft: 'auto', display: 'flex', gap: 8, alignItems: 'center' }}>
              {bulkNote && <span style={{ fontSize: 11, color: 'var(--text2)' }}>{bulkNote}</span>}
              <Button variant="primary" size="sm" disabled={bulkBusy || ackable === 0}
                onClick={bulkAcknowledge}>
                {bulkBusy ? 'Onaylanıyor…' : `Onayla (${ackable})`}
              </Button>
              <Button variant="secondary" size="sm" disabled={bulkBusy}
                onClick={() => { setPicked(new Set()); setBulkNote(null); }}>
                Seçimi temizle
              </Button>
            </span>
          </div>
        )}

        {data === undefined && <TableSkeleton cols={6} wideFirst />}
        {data === null && <Empty icon="!" title="Failed to load the queue" />}
        {filtered && filtered.length === 0 && (
          <Empty icon="✓" title="Queue clear">
            {/* v0.9.323 — compare against the vocabulary SIZE, not a literal.
                The kind list grew to four in v0.9.321 while this still read
                `< 3`, so hiding exactly one kind produced "nothing needs your
                attention" while a filter was actively narrowing the queue. */}
            {prioSet.size < PRIO_ALL.length || kindSet.size < KIND_ALL.length
              ? 'Widen the priority / kind filter to see more.'
              : 'Nothing needs your attention right now.'}
          </Empty>
        )}
        {showingStale && (
          <div style={{ marginBottom: 8 }}>
            <span className="badge b-warn">
              ⏳ Filtre uygulanıyor — aşağıdaki satırlar ÖNCEKİ filtrenin sonucu
            </span>
          </div>
        )}
        {hiddenP1Total > 0 && (
          <div style={{ marginBottom: 8 }}>
            <span className="badge b-err"
              title="Bakmadığın türlerde satır var. Varsayılan görünüm daraltabilir, ama bir şeyi sessizce saklayamaz.">
              ⚠ Bakmadığın türlerde <b>{hiddenP1Total}</b> kalem
              {' — '}
              {Object.entries(hiddenP1)
                .map(([k, n]) => `${KIND_LABEL[k as InboxKind] ?? k}: ${n}`)
                .join(' · ')}
            </span>{' '}
            <Button variant="ghost" size="sm"
              onClick={() => setParam('kind', encodeCsvSet([...KIND_ALL], KIND_ALL, KIND_DEFAULT))}>
              hepsini göster
            </Button>
          </div>
        )}
        {!inboxQ.isPending && !inboxQ.isError && (
          <div style={{
            display: 'flex', alignItems: 'center', gap: 8,
            fontSize: 11.5, color: 'var(--text3)', marginBottom: 8,
          }}>
            {minOcc > 0 ? (
              <>
                <span title="Bir kez düşen bir exception bu pencere hakkında bir olgudur, triage edilecek bir problem değil. Eşiğin altındakiler gizli — hepsini görmek için tıkla.">
                  Showing groups with <b style={{ color: 'var(--text2)' }}>{minOcc}+</b> occurrences
                  {hiddenByMinOcc > 0 && <> — <b style={{ color: 'var(--text2)' }}>{hiddenByMinOcc}</b> hidden</>}
                </span>
                <Button variant="ghost" size="sm" onClick={() => setMinOcc(0)}>show all</Button>
                {minOcc !== 10 && (
                  <Button variant="ghost" size="sm" onClick={() => setMinOcc(10)}>10+</Button>
                )}
              </>
            ) : (
              <>
                <span>Showing <b style={{ color: 'var(--text2)' }}>every</b> row, including one-off exceptions</span>
                <Button variant="ghost" size="sm" onClick={() => setMinOcc(5)}>5+ only</Button>
              </>
            )}
          </div>
        )}
        {scanCapped && (
          <div style={{ marginBottom: 8 }}>
            <span className="badge b-warn"
              title="Her kaynaktan sınırlı sayıda aday okunuyor. Filtre bu adaylar üzerinde çalıştı — sonuç boşsa 'hiç yok' demek değil, 'taranan adaylar içinde yok' demek. Servis, takım veya ortam ile daralt.">
              ⚠ Filtre taranan adaylar üzerinde çalıştı — kuyruk bundan
              büyük olabilir
            </span>
          </div>
        )}
        {capped && (
          <div style={{ marginBottom: 8 }}>
            <span className="badge b-warn"
              title="The server ranks the whole queue by priority and returns the top slice. The rows beyond the cap are the LOWEST priority ones — narrow by service, team or environment to reach them.">
              ⚠ {capped.total} kalemden ilk {capped.shown}'i gösteriliyor —
              öncelik sırasına göre kırpıldı
            </span>
          </div>
        )}
        {filtered && filtered.length > 0 && (
          // NOT VirtualTable: rows are variable-height (DetailLine renders a
          // multi-line exception message + team chips), which breaks the
          // VirtualTable uniform-row assumption. content-visibility keeps the
          // >100-row paint cheap while letting each row size to its content.
          <div className="table-wrap"
            style={{ opacity: showingStale ? 0.45 : 1, transition: 'opacity 120ms' }}
            aria-busy={showingStale}>
            <table style={{ tableLayout: 'fixed', width: '100%' }}>
              {/* leading={[34]} — the primitive owns the <colgroup>; a
                  second one alongside it is invalid markup and the
                  browser silently keeps only the first. */}
              <DataTableColgroup dt={dt} leading={[34]} />
              <DataTableHead dt={dt} leading={
                <th style={{ width: 34, textAlign: 'center' }}>
                  {/* Select-all covers the rows CURRENTLY on screen, not the
                      whole server-side queue — the list is a capped top
                      slice, so "all" can only honestly mean what is
                      visible. */}
                  <input type="checkbox" aria-label="Görünen satırların hepsini seç"
                    title="Görünen satırların hepsini seç"
                    checked={allPickedOnScreen}
                    ref={el => { if (el) el.indeterminate = selCount > 0 && !allPickedOnScreen; }}
                    onChange={() => setPicked(prev => {
                      if (allPickedOnScreen) return new Set();
                      const next = new Set(prev);
                      for (const r of (filtered ?? [])) next.add(r.id);
                      return next;
                    })} />
                </th>
              } />
              <tbody>
                {dt.sortedRows.map((it, i) => (
                  <tr key={it.id}
                    {...dt.rowProps(i)}
                    onClick={() => openDrawer(it)}
                    onMouseEnter={() => dt.nav.setSelected(i)}
                    style={{
                      cursor: 'pointer',
                      contentVisibility: 'auto',
                      containIntrinsicSize: 'auto 44px',
                    }}>
                    <td style={{ textAlign: 'center' }}
                      onClick={e => { e.stopPropagation(); }}>
                      {/* stopPropagation: the row itself opens the triage
                          drawer, and ticking a box must not also navigate. */}
                      <input type="checkbox" checked={picked.has(it.id)}
                        aria-label={`Seç: ${it.title}`}
                        onChange={() => toggleRow(it.id)} />
                    </td>
                    <td>
                      <PriorityBadge p={it.priority} reason={it.priorityReason} />
                    </td>
                    <td style={{ fontSize: 11, color: 'var(--text3)' }}>{it.source}</td>
                    <td>
                      <Link to={`/service?name=${encodeURIComponent(it.service)}`}
                        onClick={e => e.stopPropagation()}
                        style={{ fontWeight: 600 }}>
                        {it.service || <span style={{ color: 'var(--text3)' }}>(none)</span>}
                      </Link>
                      {(it.ownerTeam || it.sreTeam) && (
                        <div style={{ marginTop: 2, display: 'flex', gap: 4, flexWrap: 'wrap' }}>
                          {it.ownerTeam && (
                            <button type="button"
                              onClick={e => {
                                e.stopPropagation();
                                setOwnerFilter(ownerFilter === it.ownerTeam ? '' : (it.ownerTeam ?? ''));
                              }}
                              title={ownerFilter === it.ownerTeam
                                ? `Clear owner filter`
                                : `Filter inbox to owner ${it.ownerTeam}`}
                              style={{ all: 'unset', cursor: 'pointer' }}>
                              <span className={`badge ${ownerFilter === it.ownerTeam ? 'b-info' : 'b-gray'}`}>
                                <Users size={11} strokeWidth={1.75} /> {it.ownerTeam}
                              </span>
                            </button>
                          )}
                          {it.sreTeam && (
                            <button type="button"
                              onClick={e => {
                                e.stopPropagation();
                                setSreFilter(sreFilter === it.sreTeam ? '' : (it.sreTeam ?? ''));
                              }}
                              title={sreFilter === it.sreTeam
                                ? `Clear SRE filter`
                                : `Filter inbox to SRE ${it.sreTeam}`}
                              style={{ all: 'unset', cursor: 'pointer' }}>
                              <span className={`badge ${sreFilter === it.sreTeam ? 'b-info' : 'b-gray'}`}>
                                <Shield size={11} strokeWidth={1.75} /> {it.sreTeam}
                              </span>
                            </button>
                          )}
                        </div>
                      )}
                    </td>
                    <td>
                      <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap', marginBottom: 2 }}>
                        <span style={{ fontWeight: 600 }}>{it.title}</span>
                        {/* v0.9.255 — durum rozeti. `status` alanı telde vardı ama
                            hiç çizilmiyordu: "all" pivotunda çözülmüş bir satır
                            yenisiyle birebir aynı görünüyordu. */}
                        <StatusBadge s={it.status} />
                        {/* Bunlar backend'in ZATEN hesaplayıp attığı iki bilgi
                            (v0.9.255). Operatörün bir satırda ilk aradığı şeyler:
                            "runbook var mı" ve "az önce deploy oldu mu". */}
                        {it.runbookUrl && (
                          <a href={it.runbookUrl} target="_blank" rel="noreferrer"
                            onClick={e => e.stopPropagation()}
                            className="badge b-info" title="Runbook aç">📕 runbook</a>
                        )}
                        {it.recentDeploy && (
                          <span className="badge b-warn"
                            title={`${it.recentDeploy.service} ${it.recentDeploy.version} — ${tsLong(it.recentDeploy.timeUnixNs)}`}>
                            ⟳ deploy {it.recentDeploy.version}
                          </span>
                        )}
                      </div>
                      <DetailLine it={it} />
                    </td>
                    <td className="mono"
                      style={{ textAlign: 'right', fontVariantNumeric: 'tabular-nums' }}>
                      {it.exception
                        ? it.exception.occurrences.toLocaleString()
                        : <span style={{ color: 'var(--text3)' }}>—</span>}
                    </td>
                    <td className="mono" style={{ fontSize: 11 }}>
                      {it.startedAt
                        ? <>
                            {tsLong(it.startedAt)}
                            {/* Age belongs to first-seen: "how long has this
                                been going on" is read off the start, not the
                                last hit. */}
                            <div style={{ color: 'var(--text3)', marginTop: 2 }}>
                              {fmtAgoNs(it.startedAt)}
                            </div>
                          </>
                        : <span style={{ color: 'var(--text3)' }}>—</span>}
                    </td>
                    <td className="mono" style={{ fontSize: 11 }}>
                      {/* v0.9.333 — yaş ve ilk-görülme artık kendi kolonunda.
                          v0.9.255'te ikisi bu hücreye sıkıştırılmıştı ve ilk
                          görülme yalnız iki damga FARKLIYSA çiziliyordu: yani
                          exception satırlarında çoğu zaman hiç görünmüyordu ve
                          hiçbir zaman sıralanamıyordu. */}
                      {tsLong(it.lastSeen)}
                    </td>
                    <td>
                      {it.assignee
                        ? <AssigneePill v={it.assignee} />
                        : <span style={{ color: 'var(--text3)' }}>—</span>}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>

      {/* In-place triage drawer (v0.8.292). Rendered only once the list has
          settled (Array.isArray) so a deep-linked ?item= doesn't flash the
          soft-fallback during the initial load. `selected` is undefined when
          the id isn't in the current list → the drawer's own fallback shows. */}
      {selectedId && Array.isArray(data) && (
        <InboxTriageDrawer item={selected} onClose={closeDrawer} onOpenSource={goToSource} />
      )}
    </>
  );
}

// PriorityBadge — shared .badge tokens so operators read the same
// colour code as the Problems page (P1 err / P2 warn / P3 gray).
function PriorityBadge({ p, reason }: { p: 'P1' | 'P2' | 'P3'; reason?: string }) {
  const cls = p === 'P1' ? 'b-err' : p === 'P2' ? 'b-warn' : 'b-gray';
  return (
    <span className={`badge ${cls}`} title={reason ? `${p} — ${reason}` : p}>
      {p}
    </span>
  );
}

// v0.9.255 — durum rozeti. Üç kaynağın state sözlükleri farklı (problem:
// open/acknowledged/resolved, exception: new/acknowledged/resolved/ignored/
// regressed, anomaly: active/resolved), o yüzden eşleme tonlara göre yapılıyor,
// kaynağa göre değil. Bilinmeyen bir durum GİZLENMEZ — ham hâliyle gri basılır,
// çünkü tanımadığımız bir durumu yok saymak operatöre satırın durumu yokmuş
// gibi gösterirdi.
function StatusBadge({ s }: { s?: string }) {
  if (!s) return null;
  const k = s.toLowerCase();
  const tone =
    k === 'resolved' ? 'b-ok'
      : k === 'ignored' ? 'b-gray'
        : k === 'acknowledged' ? 'b-info'
          : k === 'regressed' ? 'b-err'
            : k === 'open' || k === 'active' || k === 'new' ? 'b-warn'
              : 'b-gray';
  return <span className={`badge ${tone}`} title={`Durum: ${s}`}>{k}</span>;
}

function AssigneePill({ v }: { v: string }) {
  const isTeam = !v.includes('@');
  return (
    <span className="badge b-info">
      {isTeam && <Users size={11} strokeWidth={1.75} />}{v}
    </span>
  );
}

// DetailLine — kind-specific subtitle. Surfaces the single most
// useful number per source: the breach ratio for Problems, the
// occurrence count for Exceptions, the peak ratio for Anomalies.
function DetailLine({ it }: { it: InboxItem }) {
  if (it.kind === 'problem' && it.problem) {
    return (
      <div style={{ fontSize: 11, color: 'var(--text3)' }}>
        <span className="mono">{it.problem.metric}</span>
        {' = '}
        <span className="mono"><b style={{ color: 'var(--err)' }}>{fmtFixed(it.problem.value, 2)}</b></span>
        <span className="mono" style={{ color: 'var(--text3)' }}> / {fmtFixed(it.problem.threshold, 2)}</span>
        {it.priorityReason && <span> · {it.priorityReason}</span>}
      </div>
    );
  }
  if (it.kind === 'exception' && it.exception) {
    return (
      <div style={{ fontSize: 11, color: 'var(--text3)' }}>
        <span className="mono">{it.exception.occurrences.toLocaleString()}</span>
        {' occurrences'}
        {it.priorityReason && <span> · {it.priorityReason}</span>}
        {it.exception.message && (
          <div style={{ marginTop: 2, color: 'var(--text2)' }}>
            {it.exception.message.length > 160
              ? `${it.exception.message.slice(0, 160)}…`
              : it.exception.message}
          </div>
        )}
      </div>
    );
  }
  if (it.kind === 'incident' && it.incident) {
    return (
      <div style={{ fontSize: 11, color: 'var(--text3)' }}>
        <span className={`badge ${it.incident.status === 'acknowledged' ? 'b-warn' : 'b-err'}`}>
          {it.incident.status}
        </span>
        {it.priorityReason && <span> · {it.priorityReason}</span>}
        {it.description && <div style={{ marginTop: 2, color: 'var(--text2)' }}>{it.description}</div>}
      </div>
    );
  }
  if (it.kind === 'anomaly' && it.anomaly) {
    return (
      <div style={{ fontSize: 11, color: 'var(--text3)' }}>
        peak <span className="mono">{fmtFixed(it.anomaly.peakRatio, 1)}x</span>
        {' · '}now <span className="mono">{fmtFixed(it.anomaly.currentRatio, 1)}x</span>
        {it.priorityReason && <span> · {it.priorityReason}</span>}
      </div>
    );
  }
  return (
    <div style={{ fontSize: 11, color: 'var(--text3)' }}>
      {it.priorityReason || it.description}
    </div>
  );
}
