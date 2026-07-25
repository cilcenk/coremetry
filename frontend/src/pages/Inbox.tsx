import { useMemo, useRef, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { Button } from '@/components/ui';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { Users, Shield } from 'lucide-react';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { TableSkeleton } from '@/components/Skeleton';
import { useInbox } from '@/lib/queries';
import { tsLong, fmtFixed } from '@/lib/utils';
import { teamOptionsCI } from '@/lib/teamOptions';
import { decodeCsvSet, encodeCsvSet } from '@/lib/inboxUrl';
import { useUrlEnv } from '@/lib/useUrlEnv';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import { InboxTriageDrawer } from '@/components/InboxTriageDrawer';
import { resolveSelectedItem } from '@/lib/inboxDrawer';
import type { DataTableColumn } from '@/lib/dataTable';
import type { InboxItem, InboxKind } from '@/lib/types';

// Facet vocab + defaults (v0.8.291) — the inbox lands on P1+P2 across all
// kinds; both are the "default" the URL codec omits so a fresh link stays clean.
const PRIO_ALL = ['P1', 'P2', 'P3'] as const;
const PRIO_DEFAULT = ['P1', 'P2'] as const;
const KIND_ALL: readonly InboxKind[] = ['problem', 'exception', 'anomaly'];
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
    () => new Set(decodeCsvSet(rawKind, KIND_ALL, KIND_ALL) as InboxKind[]),
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
    setParam('kind', encodeCsvSet(next, KIND_ALL, KIND_ALL));
  };
  const setServiceFilter = (v: string) => setParam('service', v || null);
  const setSearchFilter = (v: string) => setParam('q', v || null);
  const setOwnerFilter = (v: string) => setParam('owner', v || null);
  const setSreFilter = (v: string) => setParam('sre', v || null);
  const openDrawer = (it: InboxItem) => setParam('item', it.id);
  const closeDrawer = () => setParam('item', null);

  // Global env picker (v0.8.387) — service-scoped: the server keeps
  // rows whose service ran in the env in the last hour (+ service-
  // less global alerts), same semantics as /problems. Hint chip in
  // the facet bar spells it out.
  const [env] = useUrlEnv();
  const inboxQ = useInbox({
    status: statusFilter,
    service: serviceFilter || undefined,
    q: searchFilter || undefined,
    ownerTeam: ownerFilter || undefined,
    sreTeam: sreFilter || undefined,
    env: env || undefined,
    limit: 300,
  });
  const data: InboxItem[] | null | undefined =
    inboxQ.isPending ? undefined : inboxQ.isError ? null : inboxQ.data?.items ?? [];
  // v0.9.221 — the server caps the queue; say so rather than letting 300 rows
  // pass for the whole thing. Sorted priority-desc, so what fell off is the
  // low-priority tail — exactly the part an operator would assume was empty.
  const capped = !inboxQ.isPending && !inboxQ.isError && inboxQ.data?.truncated
    ? { shown: inboxQ.data.items.length, total: inboxQ.data.total }
    : null;

  // The drawer's selected row, resolved from ?item= against the loaded list.
  // Uses the full (pre-facet) list so a deep-link to a row hidden by the
  // priority/kind filter still opens; undefined when the id isn't present →
  // the drawer shows a soft fallback, never a blank panel.
  const selected = useMemo(() => resolveSelectedItem(data, selectedId), [data, selectedId]);

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
    }
  };

  // Shared sortable + resizable table. Pre-sort by lastSeen desc so the
  // primitive's stable priority-desc sort reproduces the prior fixed
  // ordering (P1 first, newest within priority). Hook is unconditional.
  // onOpen + searchRef wire the app-wide keyboard nav (j/k select,
  // Enter/o open the source surface, "/" focuses the filter).
  const inboxRows = useMemo(
    () => (filtered ? [...filtered].sort((a, b) => b.lastSeen - a.lastSeen) : []),
    [filtered]);
  const dt = useDataTable<InboxItem>({
    storageKey: 'inbox',
    columns: INBOX_COLS,
    rows: inboxRows,
    initialSort: { id: 'priority', dir: 'desc' },
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
  const ackable = ackProblemIds.length + ackExceptionFps.length;
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
    const [pRes, eRes] = await Promise.allSettled([
      ackProblemIds.length ? api.acknowledgeProblems(ackProblemIds) : Promise.resolve(null),
      ackExceptionFps.length ? api.setExceptionGroupStatesBulk(ackExceptionFps, 'acknowledged') : Promise.resolve(null),
    ]);
    const okProblems = pRes.status === 'fulfilled' ? ackProblemIds.length : 0;
    const okExceptions = eRes.status === 'fulfilled'
      ? ((eRes.value as { applied: number } | null)?.applied ?? 0)
      : 0;
    const errs = [pRes, eRes].filter(r => r.status === 'rejected').length;
    setBulkNote(errs
      ? `${okProblems + okExceptions} onaylandı, ${errs} istek başarısız`
      : `${okProblems + okExceptions} onaylandı`);
    setPicked(new Set());
    // The server already dropped the cached list + badge; refetch so
    // the rows leave the screen instead of lingering a poll cycle.
    qc.invalidateQueries({ queryKey: ['inbox'] });
    setBulkBusy(false);
  };

  const counts = useMemo(() => {
    const out: Record<string, number> = { P1: 0, P2: 0, P3: 0,
      problem: 0, exception: 0, anomaly: 0 };
    for (const it of data ?? []) {
      out[it.priority] = (out[it.priority] ?? 0) + 1;
      out[it.kind] = (out[it.kind] ?? 0) + 1;
    }
    return out;
  }, [data]);

  // Distinct team values from the current result set drive the
  // dropdown options. Server-side filter narrows the list, so
  // selecting a team and then opening the dropdown again still
  // shows the remaining teams — the operator can stack
  // (owner=X then sre=Y) without losing visibility.
  const { ownerOptions, sreOptions } = useMemo(() => ({
    // v0.8.330 — case-insensitive dedup, same rule as the catalog-fed
    // dropdowns (mixed-casing team attrs read as one team).
    ownerOptions: teamOptionsCI((data ?? []).map(it => it.ownerTeam)),
    sreOptions:   teamOptionsCI((data ?? []).map(it => it.sreTeam)),
  }), [data]);

  return (
    <>
      <Topbar title="Inbox" showEnv />
      <div id="content">
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
          {/* Status pivot — single-select */}
          {STATUS_PIVOTS.map(s => (
            <span key={s} onClick={() => setStatusFilter(s)}
              className={`facet${statusFilter === s ? ' on' : ''}`}>
              {s === 'open' ? 'Open / Active' : 'All'}
            </span>
          ))}

          {/* Priority chips — multi-select */}
          {(['P1', 'P2', 'P3'] as const).map(pp => {
            const tint = pp === 'P1' ? ' f-err' : pp === 'P2' ? ' f-warn' : '';
            return (
              <span key={pp} onClick={() => togglePrio(pp)}
                className={`facet${tint}${prioSet.has(pp) ? ' on' : ''}`}>
                {pp} <span className="n">{counts[pp] ?? 0}</span>
              </span>
            );
          })}

          {/* Kind chips — multi-select */}
          {(['problem', 'exception', 'anomaly'] as const).map(k => {
            const label = k === 'problem' ? 'Problems'
                       : k === 'exception' ? 'Exceptions'
                       : 'Anomalies';
            return (
              <span key={k} onClick={() => toggleKind(k)}
                className={`facet${kindSet.has(k) ? ' on' : ''}`}>
                {label} <span className="n">{counts[k] ?? 0}</span>
              </span>
            );
          })}

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
        {data === null && <Empty icon="!" title="Failed to load inbox" />}
        {filtered && filtered.length === 0 && (
          <Empty icon="✓" title="Inbox clear">
            {prioSet.size < 3 || kindSet.size < 3
              ? 'Widen the priority / kind filter to see more.'
              : 'Nothing needs your attention right now.'}
          </Empty>
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
          <div className="table-wrap">
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
                      <div style={{ fontWeight: 600, marginBottom: 2 }}>{it.title}</div>
                      <DetailLine it={it} />
                    </td>
                    <td className="mono" style={{ fontSize: 11 }}>{tsLong(it.lastSeen)}</td>
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
