import { Fragment, useEffect, useMemo, useRef, useState } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { SavedViewsBar } from '@/components/SavedViewsBar';
import { TriageCrumb } from '@/components/TriageCrumb';
import { Spinner, Empty } from '@/components/Spinner';
import { ServicePicker } from '@/components/ServicePicker';
import { useAuth } from '@/components/AuthProvider';
import { CopilotExplain } from '@/components/CopilotExplain';
import { ClusterChips } from '@/components/ClusterChips';
import { ProblemRunbookPanel } from '@/components/ProblemRunbookPanel';
import { RootCausePanel } from '@/components/RootCausePanel';
import { RootCauseRibbon } from '@/components/RootCauseRibbon';
import { ArrowDownToLine, Users, ChevronRight, ChevronDown, CornerDownRight } from 'lucide-react';
import { Button } from '@/components/ui/Button';
import { IconBell, IconSparkles } from '@/components/icons';
import { useProblems, useServicesMetadata, keys } from '@/lib/queries';
import { useQueryClient } from '@tanstack/react-query';
import { api, type UserRow } from '@/lib/api';
import { fmtNum, fmtFixed, tsLong } from '@/lib/utils';
import { teamOptionsCI } from '@/lib/teamOptions';
import { getItem, setItem, STORAGE_KEYS } from '@/lib/storage';
import { useDataTable, DataTableColgroup, DataTableHead } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import type {
  ExceptionGroup, ExceptionGroupState, ExceptionSample, Problem,
} from '@/lib/types';
import { ProblemDetail } from './ProblemDetail';
import { withProblemParam } from './problemLink';

// State buckets shown as tabs along the top of the page.
const TABS: { key: string; label: string; hint: string }[] = [
  { key: 'open',         label: 'Inbox',        hint: 'New + acknowledged + regressed' },
  { key: 'new',          label: 'Open',         hint: 'Untouched since first occurrence' }, // v0.8.382: NEW is the first-seen badge
  { key: 'acknowledged', label: 'Acknowledged', hint: 'Someone is on it' },
  { key: 'regressed',    label: 'Regressed',    hint: 'Resolved but happening again' },
  { key: 'resolved',     label: 'Resolved',     hint: 'Closed out' },
  { key: 'ignored',      label: 'Ignored',      hint: 'Permanently silenced' },
];

// Exception-inbox sort keys the SERVER understands (v0.8.318 — the
// ORDER BY runs in ClickHouse across the whole paginated set). The
// DataTable column ids below are exactly these values, so dt.sort.id
// forwards straight to the fetch after a sanitize.
type SortKey = 'state' | 'type' | 'service' | 'occurrences' | 'firstSeen' | 'lastSeen' | 'assignee';
const SORT_KEYS: readonly SortKey[] =
  ['state', 'type', 'service', 'occurrences', 'firstSeen', 'lastSeen', 'assignee'];
const DEFAULT_EXC_SORT = { id: 'lastSeen' as SortKey, dir: 'desc' as const };

// Exception inbox columns — shared DataTable primitive in serverSort
// mode (Services template, v0.8.251): the hook owns the sort UX, the
// `s_exception-inbox` URL param and the persisted widths; the backend
// owns the ordering, so `sortValue` is never invoked here (it is the
// sortable marker + naturalDir carrier). State's severity-style
// ordering (worst at top) stays server-side — see
// exceptionGroupsOrderBy's multiIf.
const EXC_COLS: DataTableColumn<ExceptionGroup>[] = [
  { id: 'state',       label: 'State',       sortValue: g => g.state,       naturalDir: 'desc', width: 100 },
  { id: 'type',        label: 'Exception',   sortValue: g => g.type,        naturalDir: 'asc',  width: 400 },
  { id: 'service',     label: 'Service',     sortValue: g => g.service,     naturalDir: 'asc',  width: 150 },
  { id: 'occurrences', label: 'Occurrences', sortValue: g => g.occurrences, numeric: true,      width: 100 },
  { id: 'firstSeen',   label: 'First seen',  sortValue: g => g.firstSeen,   width: 150 },
  { id: 'lastSeen',    label: 'Last seen',   sortValue: g => g.lastSeen,    width: 150 },
  { id: 'assignee',    label: 'Assignee',    sortValue: g => g.assignee,    naturalDir: 'asc',  width: 160 },
];

// Problems-specific severity + priority ordering.
const SEV_RANK: Record<string, number> = { critical: 3, warning: 2, info: 1 };
// P1 ranks above P2 ranks above P3 (lower number = more urgent).
const PRIO_RANK: Record<string, number> = { P1: 3, P2: 2, P3: 1 };

// Severity + priority filter chips render via the shared .facet
// primitive (globals.css, v0.8.38) — active = --accent-bg/--accent-
// border; the f-err/f-warn tints keep the urgency cue at rest. The
// old per-chip color-mix palette (v0.5.469) was replaced when these
// moved onto the shared facetbar in v0.8.39.

// Alert-rules columns — shared DataTable primitive, CLIENT sort: the
// section is a single capped fetch (limit 200, no pager), so the rows
// being sorted are the fully loaded set. The priority accessor keeps
// the old cmp's composite ordering — priority bucket, then severity,
// then start time — with each term scaled so it can't cross into the
// next (startedAt/1e9 stays < 1e10 until year 2286).
const PROBLEM_COLS: DataTableColumn<Problem>[] = [
  { id: 'priority', label: 'Priority',
    sortValue: p => (PRIO_RANK[p.priority ?? 'P3'] ?? 0) * 1e11
                  + (SEV_RANK[p.severity] ?? 0) * 1e10
                  + p.startedAt / 1e9,
    width: 90 },
  { id: 'severity', label: 'Severity', sortValue: p => SEV_RANK[p.severity] ?? 0, width: 90 },
  { id: 'service',  label: 'Service',  sortValue: p => p.service,   naturalDir: 'asc', width: 170 },
  { id: 'metric',   label: 'Metric',   sortValue: p => p.metric,    naturalDir: 'asc', width: 150 },
  { id: 'value',    label: 'Value',    sortValue: p => p.value,     numeric: true,     width: 110 },
  { id: 'rule',     label: 'Rule',     sortValue: p => p.ruleName,  naturalDir: 'asc', width: 420 },
  { id: 'started',  label: 'Started',  sortValue: p => p.startedAt, width: 150 },
  { id: 'status',   label: 'Status',   sortValue: p => p.status,    naturalDir: 'asc', width: 100 },
];

export default function ProblemsPage() {
  const { user } = useAuth();
  const isAdmin = user?.role === 'admin' || user?.role === 'editor';
  // URL is the source of truth for tab + service + page so a
  // pasted link reproduces the exact triage view. ?service= was
  // already URL-driven (driven by the service-detail "Errors"
  // pill); v0.5.98 adds ?tab=…&page=N so the inbox is fully
  // shareable. Filter changes always reset page back to 0 so a
  // teammate's link can't land them on "page 4 of 2".
  const [searchParams, setSearchParams] = useSearchParams();
  const tab     = searchParams.get('tab') || 'open';
  const service = searchParams.get('service') || '';
  // Owner (ug-team) / SRE (sy-team) team filter — URL-backed so a
  // triage link reproduces the exact team slice, mirroring /inbox's
  // ?owner=/?sre= (v0.8.310). Resolved server-side to member services
  // so the narrowing is correct across the whole paginated set.
  const ownerTeam = searchParams.get('owner') || '';
  const sreTeam   = searchParams.get('sre')   || '';
  const page    = Math.max(0, parseInt(searchParams.get('page') || '0', 10) || 0);
  const setTab = (v: string) => setSearchParams(prev => {
    const p = new URLSearchParams(prev);
    p.set('tab', v); p.delete('page');
    return p;
  }, { replace: true });
  const setService = (v: string) => setSearchParams(prev => {
    const p = new URLSearchParams(prev);
    if (v) p.set('service', v); else p.delete('service');
    p.delete('page');
    return p;
  }, { replace: true });
  const setTeam = (key: 'owner' | 'sre', v: string) => setSearchParams(prev => {
    const p = new URLSearchParams(prev);
    if (v) p.set(key, v); else p.delete(key);
    p.delete('page'); // a filter change can't leave you on a now-missing page
    return p;
  }, { replace: true });
  const setPage = (next: number | ((p: number) => number)) =>
    setSearchParams(prev => {
      const p = new URLSearchParams(prev);
      const v = typeof next === 'function' ? next(page) : next;
      if (v > 0) p.set('page', String(v)); else p.delete('page');
      return p;
    }, { replace: true });
  const [search, setSearch] = useState('');
  // v0.8.318 — search commits debounced and runs SERVER-side (q=): the old
  // client filter only searched the loaded 50-row page, so matches on
  // other pages read as "no results".
  const [committedSearch, setCommittedSearch] = useState('');
  useEffect(() => {
    const t = window.setTimeout(() => {
      setCommittedSearch(prev => {
        const next = search.trim();
        if (next !== prev) setPage(0);
        return next;
      });
    }, 300);
    return () => clearTimeout(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [search]);
  const [, setServices] = useState<string[]>([]);
  const [users, setUsers] = useState<UserRow[]>([]);
  const [data, setData] = useState<ExceptionGroup[] | null | undefined>(undefined);
  const [total, setTotal] = useState(0);
  const PAGE_SIZE = 50;
  // Expanded fingerprint(s) — multiple groups can be open at once for compare-and-contrast.
  // Seed with any ?exception=<fingerprint> the URL carries so a
  // deep-link (e.g. from /inbox) lands with the right row open.
  const [expanded, setExpanded] = useState<Set<string>>(() => {
    const fp = searchParams.get('exception');
    return new Set(fp ? [fp] : []);
  });
  // Selected group for the full in-page detail view (null = list).
  const [detail, setDetail] = useState<ExceptionGroup | null>(null);

  // Sort lives in the shared DataTable hook, serverSort mode
  // (v0.8.251 Services template): the hook owns the header UX, the
  // `s_exception-inbox` URL param and localStorage persistence; the
  // fetch below forwards the sanitized pair so a stale persisted id
  // (old column schema, hand-edited URL) never reaches the backend
  // ORDER BY. Must precede the fetch effect that consumes it.
  const dt = useDataTable<ExceptionGroup>({
    storageKey: 'exception-inbox',
    columns: EXC_COLS,
    rows: data ?? [],
    serverSort: true,
    initialSort: DEFAULT_EXC_SORT,
  });
  const sortOk = (SORT_KEYS as readonly string[]).includes(dt.sort.id ?? '');
  const sortBy: SortKey = sortOk ? dt.sort.id as SortKey : DEFAULT_EXC_SORT.id;
  const sortDir = sortOk ? dt.sort.dir : DEFAULT_EXC_SORT.dir;

  // Exception groups inbox — separate query because it depends
  // on tab + service filter; couldn't be folded into the shared
  // anomaly hooks above.
  const qc = useQueryClient();
  const refreshExceptionGroups = () => {
    setData(undefined);
    api.exceptionGroups({
      state: tab, service: service || undefined,
      ownerTeam: ownerTeam || undefined, sreTeam: sreTeam || undefined,
      // v0.8.318 — sort + search are server-side across the whole set;
      // the client-side sort of one server page mis-prioritized ("top by
      // occurrences" was really "most-recent 50, reordered").
      sort: sortBy, dir: sortDir,
      q: committedSearch || undefined,
      limit: PAGE_SIZE, offset: page * PAGE_SIZE,
    })
      .then(d => { setData(d.items ?? []); setTotal(d.total ?? 0); })
      .catch(() => setData(null));
  };
  // Page reset on filter change is owned by setTab/setService/setTeam
  // (they delete ?page=); search resets it itself. A SORT change also
  // invalidates the page offset — that reset lives INSIDE the fetch
  // effect, guarded by a sig ref, so (a) a deep-linked ?page= survives
  // mount, and (b) sorting while on page N doesn't fire a wasted fetch
  // for the stale offset first (refreshExceptionGroups has no
  // cancellation — two in-flight fetches would race). It can't ride
  // the hook's onSortChange either: the hook's URL write and setPage
  // are separate same-tick setSearchParams calls, and the second would
  // clobber the first (the v0.8.253 URL-overwrite class).
  const sortSig = `${sortBy}.${sortDir}`;
  const lastSortSigRef = useRef(sortSig);
  useEffect(() => {
    if (lastSortSigRef.current !== sortSig) {
      lastSortSigRef.current = sortSig;
      if (page !== 0) { setPage(0); return; } // effect re-runs with page 0
    }
    refreshExceptionGroups();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tab, service, ownerTeam, sreTeam, page, sortSig, committedSearch]);

  useEffect(() => {
    api.services({ from: 0, to: 0 })
      .then(s => setServices((s ?? []).map(x => x.name))).catch(() => {});
  }, []);

  useEffect(() => {
    if (!isAdmin) return;
    api.listUsers().then(u => setUsers(u ?? [])).catch(() => {});
  }, [isAdmin]);

  // Team dropdown options come from the service catalog (not the loaded
  // page), so a pick never collapses the list of teams to choose from —
  // same source the alert-rules section + Services page use.
  const catalogQ = useServicesMetadata();
  // v0.8.330 — case-insensitive dedup: mixed-casing team attrs
  // ("avengerSY"/"Avengersy") listed the same team twice.
  const ownerTeamOptions = useMemo(
    () => teamOptionsCI(Object.values(catalogQ.data ?? {}).map(m => m.ownerTeam)),
    [catalogQ.data]);
  const sreTeamOptions = useMemo(
    () => teamOptionsCI(Object.values(catalogQ.data ?? {}).map(m => m.sreTeam)),
    [catalogQ.data]);

  // v0.8.318 — the server owns filtering AND ordering now (q=/sort=/dir=
  // across the whole paginated set); the page renders rows verbatim. The
  // old client-side sort of one 50-row server page mis-prioritized, and
  // the client search read as "no results" for matches on other pages.
  const filtered = data ?? [];

  const userById = useMemo(() => {
    const m = new Map<string, UserRow>();
    users.forEach(u => m.set(u.id, u));
    return m;
  }, [users]);

  const setState = async (g: ExceptionGroup, next: ExceptionGroupState) => {
    try {
      await api.setExceptionGroupState(g.fingerprint, next);
      // Refresh the exception inbox + every anomaly feed so a
      // state change percolates everywhere it might appear
      // (the /anomalies page consumes the same cache).
      refreshExceptionGroups();
      qc.invalidateQueries({ queryKey: keys.anomalies.all });
    } catch (err) { alert(humanize(err)); }
  };
  const setAssignee = async (g: ExceptionGroup, userId: string) => {
    try {
      await api.assignExceptionGroup(g.fingerprint, userId);
      refreshExceptionGroups();
    } catch (err) { alert(humanize(err)); }
  };
  const toggleExpand = (fp: string) => {
    setExpanded(prev => {
      const next = new Set(prev);
      if (next.has(fp)) next.delete(fp);
      else next.add(fp);
      return next;
    });
  };

  // Full in-page exception-group detail (prototype design-parity). Clicking a
  // row opens it; back returns to this list. Caret still toggles the inline
  // quick-peek (SamplesPanel) so both affordances coexist.
  if (detail) {
    return (
      <>
        <Topbar title="Problems" />
        <ProblemDetail
          group={detail}
          isAdmin={isAdmin}
          onBack={() => setDetail(null)}
          onChanged={() => { refreshExceptionGroups(); qc.invalidateQueries({ queryKey: keys.anomalies.all }); }}
        />
      </>
    );
  }

  return (
    <>
      <Topbar title="Problems" />
      <div id="content">
        <TriageCrumb label="Problems" />
        <SavedViewsBar page="problems" />

        {/* ── 1. Exception inbox (top of page) ─────────────────
            Per-group state machine the operator triages: New →
            Ack → Resolved/Ignored. Sits at the very top because
            it's the most actionable signal in the product —
            this is the assignable queue an SRE works through. */}
        <div className="tab-strip">
          {TABS.map(t => (
            <button key={t.key} onClick={() => setTab(t.key)} title={t.hint}
              className={tab === t.key ? 'active' : ''}>
              {t.label}
            </button>
          ))}
        </div>

        <div className="controls">
          <ServicePicker value={service} onChange={setService}
            placeholder="Service…" width={170} />
          <input value={search} onChange={e => setSearch(e.target.value)}
            placeholder="Search type/message…" style={{ width: 260 }} />
          {/* Owner (ug-team) / SRE (sy-team) team filter — plain <select>
              for these small catalog-derived sets (frontend-conventions
              §3), resolved server-side so the narrowing is correct across
              the whole paginated inbox, not just the loaded page. */}
          <select value={ownerTeam} onChange={e => setTeam('owner', e.target.value)}
            aria-label="Filter by owner team" style={{ minWidth: 130 }}>
            <option value="">All owner teams</option>
            {ownerTeamOptions.map(t => <option key={t} value={t}>{t}</option>)}
          </select>
          <select value={sreTeam} onChange={e => setTeam('sre', e.target.value)}
            aria-label="Filter by SRE team" style={{ minWidth: 130 }}>
            <option value="">All SRE teams</option>
            {sreTeamOptions.map(t => <option key={t} value={t}>{t}</option>)}
          </select>
          <span style={{ color: 'var(--text3)', fontSize: 12, marginLeft: 'auto' }}>
            {total > 0 && (
              <>
                {page * PAGE_SIZE + 1}–{Math.min((page + 1) * PAGE_SIZE, total)} of {fmtNum(total)} groups
                {search.trim() && <> · {filtered.length} on this page match</>}
              </>
            )}
          </span>
        </div>

        {data === undefined && <Spinner />}
        {data && filtered.length === 0 && (
          <Empty icon="✓" title={tab === 'open'
            ? 'Inbox is clear — no untriaged exceptions'
            : `No groups in "${tab}"`}>
            {/* v0.6.24 — explain why each tab might legitimately
                be empty so operators don't think the page broke. */}
            {tab === 'resolved' && (
              <>Groups land here when you click <b>Resolve</b> on a row in the Inbox,
              or automatically after 14 days without a new occurrence.</>
            )}
            {tab === 'ignored' && (
              <>Groups land here when you click <b>Ignore</b> on a row. Ignored groups
              stay silent even if they fire again.</>
            )}
            {tab === 'acknowledged' && (
              <>Groups land here when you <b>Ack</b> a row — you've seen it but haven't
              fixed it yet. Click <b>Resolve</b> to move out of ack.</>
            )}
            {tab !== 'resolved' && tab !== 'ignored' && tab !== 'acknowledged' && (
              <>Click a row to inspect recent occurrences. Use Ack / Resolve / Ignore to manage state.</>
            )}
          </Empty>
        )}
        {data && filtered.length > 0 && (
          <div className="table-wrap">
            <table style={{ tableLayout: 'fixed', width: '100%' }}>
              <DataTableColgroup dt={dt} leading={[24]} trailing={isAdmin ? [240] : undefined} />
              <DataTableHead dt={dt}
                leading={<th style={{ width: 24 }}></th>}
                trailing={isAdmin ? <th style={{ width: 240 }}>Actions</th> : undefined} />
              <tbody>
                {filtered.map(g => {
                  const open = expanded.has(g.fingerprint);
                  return (
                    <Fragment key={g.fingerprint}>
                      <tr onClick={() => setDetail(g)}
                        onKeyDown={(e) => {
                          // Enter/Space opens the full detail (keyboard parity
                          // with the click). The caret cell handles the inline
                          // quick-peek separately.
                          if (e.key === 'Enter' || e.key === ' ') {
                            e.preventDefault();
                            setDetail(g);
                          }
                        }}
                        tabIndex={0}
                        role="button"
                        aria-expanded={open}
                        style={{ cursor: 'pointer' }}>
                        <td style={{ color: 'var(--text3)', textAlign: 'center', cursor: 'pointer' }}
                          title={open ? 'Hide occurrences' : 'Peek occurrences'}
                          onClick={e => { e.stopPropagation(); toggleExpand(g.fingerprint); }}>
                          {open
                            ? <ChevronDown size={13} strokeWidth={1.75} style={{ verticalAlign: 'middle' }} />
                            : <ChevronRight size={13} strokeWidth={1.75} style={{ verticalAlign: 'middle' }} />}
                        </td>
                        <td><StateBadge s={g.state} /></td>
                        <td>
                          <div className="mono" style={{ display: 'flex', alignItems: 'center', gap: 6, fontWeight: 600, fontSize: 11.5, color: 'var(--err)' }}>
                            {g.type}
                            {/* "NEW" badge: first observed in the
                                last hour. Highest-signal column
                                for an SRE scanning the inbox in
                                the morning — these are the ones
                                that didn't exist yesterday. */}
                            {Date.now() - g.firstSeen / 1e6 < 60 * 60 * 1000 && (
                              <span className="badge b-warn" style={{ fontSize: 9, padding: '0 5px' }}>
                                NEW
                              </span>
                            )}
                          </div>
                          <div className="mono" style={{ fontSize: 10.5, color: 'var(--text3)',
                                        maxWidth: 480, overflow: 'hidden',
                                        textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}
                               title={g.message}>
                            {g.message || '—'}
                          </div>
                        </td>
                        <td>
                          <Link to={`/service?name=${encodeURIComponent(g.service)}`}
                            onClick={e => e.stopPropagation()}
                            style={{ fontFamily: 'monospace', fontSize: 11 }}>
                            {g.service}
                          </Link>
                        </td>
                        <td className="mono" style={{ textAlign: 'right', fontWeight: 600, color: 'var(--err)' }}>
                          {fmtNum(Number(g.occurrences))}
                        </td>
                        <td className="mono" style={{ fontSize: 11, color: 'var(--text3)' }}>{tsLong(g.firstSeen)}</td>
                        <td className="mono" style={{ fontSize: 11, color: 'var(--text3)' }}>{tsLong(g.lastSeen)}</td>
                        <td onClick={e => e.stopPropagation()}>
                          {isAdmin ? (
                            <select value={g.assignee} onChange={e => setAssignee(g, e.target.value)}
                              style={{ fontSize: 11, maxWidth: 160 }}>
                              <option value="">— unassigned —</option>
                              {users.map(u => (
                                <option key={u.id} value={u.id}>{u.email}</option>
                              ))}
                            </select>
                          ) : (
                            <span style={{ fontSize: 11, color: 'var(--text2)' }}>
                              {g.assignee ? (userById.get(g.assignee)?.email ?? g.assignee) : '—'}
                            </span>
                          )}
                        </td>
                        {isAdmin && (
                          <td onClick={e => e.stopPropagation()}>
                            <ActionButtons g={g} onSet={setState} />
                          </td>
                        )}
                      </tr>
                      {open && (
                        <tr>
                          <td colSpan={isAdmin ? 9 : 8} style={{
                            background: 'var(--bg1)', padding: '10px 16px',
                            borderTop: '1px solid var(--border)',
                          }}>
                            <SamplesPanel fingerprint={g.fingerprint} />
                          </td>
                        </tr>
                      )}
                    </Fragment>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
        {data && total > PAGE_SIZE && (
          <div style={{
            marginTop: 8, display: 'flex', alignItems: 'center', gap: 8,
            justifyContent: 'flex-end', fontSize: 12,
          }}>
            <Button variant="secondary" size="sm"
              disabled={page === 0}
              onClick={() => setPage(p => Math.max(0, p - 1))}>
              ← Prev
            </Button>
            <span style={{ color: 'var(--text3)' }}>
              Page {page + 1} of {Math.max(1, Math.ceil(total / PAGE_SIZE))}
            </span>
            <Button variant="secondary" size="sm"
              disabled={(page + 1) * PAGE_SIZE >= total}
              onClick={() => setPage(p => p + 1)}>
              Next →
            </Button>
          </div>
        )}

        {/* ── 2. Alert rules (firing thresholds + SLO burn) ───
            Distinct from the exception inbox above: these are
            threshold/SLO burn / anomaly-detector alerts that the
            evaluator has opened. Live anomaly streams (log
            patterns, trace ops, metric z-score) live on the
            separate /anomalies page now — they're observation-
            only signals, the inbox above is the actionable queue. */}
        <ProblemsSection serviceFilter={service} />
      </div>
    </>
  );
}

// ProblemsSection — embeds the former /problems page table inline.
// Polls via useProblems (30s default), supports status filter +
// column sort + j/k row nav. Single section per the merged
// Exceptions page UX.
function ProblemsSection({ serviceFilter }: { serviceFilter: string }) {
  const navigate = useNavigate();
  const { user } = useAuth();
  const currentUserEmail = user?.email ?? '';
  const [searchParams, setSearchParams] = useSearchParams();
  // When arriving via ?problem=<id> deep link, broaden the
  // status pivot so the drawer can resolve the row even when
  // it's acknowledged / resolved. Default 'open' otherwise.
  const [statusFilter, setStatusFilter] = useState<'open' | 'all' | 'resolved'>(
    searchParams.get('problem') ? 'all' : 'open');
  // Triage drawer state — id of the problem currently shown
  // in the right-side panel. Replaces the v0.5.x inline "Why?"
  // expansion; the same causal-correlation panel now lives
  // inside the drawer alongside the rule details + deploy
  // chip + AI buttons in one consolidated triage surface.
  // Seed from ?problem=<id> so a deep-link (e.g. from /inbox)
  // lands with the right drawer open.
  const [drawerProblemId, setDrawerProblemId] = useState<string | null>(
    () => searchParams.get('problem'));
  // v0.8.256 (operator-reported): the drawer only READ ?problem= —
  // opening a problem never wrote it back, so the address bar
  // stayed /problems and a copied link lost the selection. Every
  // open/close now routes through here: state + URL move together
  // (replace:true — triage clicks shouldn't pile history entries).
  const openDrawer = (id: string | null) => {
    setDrawerProblemId(id);
    setSearchParams(prev => withProblemParam(prev, id), { replace: true });
  };
  // Bulk-select state (v0.5.83). Operators can multi-select
  // problems and acknowledge them in one POST — typical
  // workflow during a fan-out incident where 20 alerts fire
  // from the same root cause and the oncall wants to mute
  // them all once they've started fixing.
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set());
  const [bulkBusy, setBulkBusy] = useState(false);
  // Esc closes the drawer — standard incident-triage muscle
  // memory across other APM tools.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && drawerProblemId) {
        openDrawer(null);
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [drawerProblemId]);
  // Severity filter — multi-select chip row above the table.
  // Persisted to localStorage so an operator who keeps the
  // "critical only" filter stays at that scope across page
  // reloads (typical incident workflow). Default: all three on.
  const [sevSet, setSevSet] = useState<Set<string>>(() => {
    const arr = getItem<string[] | null>(STORAGE_KEYS.problemsSev, null);
    if (Array.isArray(arr) && arr.length > 0) return new Set(arr);
    return new Set(['critical', 'warning', 'info']);
  });
  // Priority filter — defaults to P1+P2 so the operator's inbox
  // surfaces signal first. P3 (steady warnings) is one click
  // away. Persisted alongside the severity set.
  const [prioSet, setPrioSet] = useState<Set<string>>(() => {
    const arr = getItem<string[] | null>(STORAGE_KEYS.problemsPrio, null);
    if (Array.isArray(arr) && arr.length > 0) return new Set(arr);
    return new Set(['P1', 'P2']);
  });
  const togglePrio = (p: string) => {
    setPrioSet(prev => {
      const next = new Set(prev);
      if (next.has(p)) {
        if (next.size === 1) return prev;
        next.delete(p);
      } else {
        next.add(p);
      }
      setItem(STORAGE_KEYS.problemsPrio, [...next]);
      return next;
    });
  };
  const toggleSev = (s: string) => {
    setSevSet(prev => {
      const next = new Set(prev);
      if (next.has(s)) {
        // Don't let the operator clear all three — that
        // empties the table and looks broken. Last
        // selected stays on.
        if (next.size === 1) return prev;
        next.delete(s);
      } else {
        next.add(s);
      }
      setItem(STORAGE_KEYS.problemsSev, [...next]);
      return next;
    });
  };
  // Owner-team / SRE-team filters (v0.8.290) — mirror the Services
  // page. Plain local state (NOT URL-backed): the page's other
  // filter axes (status / severity / priority) are all local too, so
  // URL-backing only these two would diverge; only the triage drawer
  // (?problem=) is URL-backed here. Empty value = "all", passed to
  // the server which filters with the same EqualFold / empty-means-
  // all semantics as /inbox. Options come from the service catalog
  // (like Services) so the dropdown stays stable when a selection
  // narrows the server-filtered rows — deriving them from the
  // already-filtered result would collapse the list to the pick.
  const [ownerTeam, setOwnerTeam] = useState('');
  const [sreTeam, setSreTeam] = useState('');
  const catalogQ = useServicesMetadata();
  // v0.8.330 — case-insensitive team options (see teamOptionsCI).
  const ownerTeamOptions = useMemo(
    () => teamOptionsCI(Object.values(catalogQ.data ?? {}).map(m => m.ownerTeam)),
    [catalogQ.data]);
  const sreTeamOptions = useMemo(
    () => teamOptionsCI(Object.values(catalogQ.data ?? {}).map(m => m.sreTeam)),
    [catalogQ.data]);

  // (Pre-v0.5.80 inline "Why?" expansion lived here; the
  // same correlation panel is now embedded inside the
  // triage drawer.)

  // Sort the priority set before handing to the query so the React
  // Query key hash stays stable regardless of toggle order, and the
  // backend cache key (sorted+FNV digest) matches.
  const prioParam = useMemo(() => [...prioSet].sort(), [prioSet]);
  const problemsQ = useProblems({
    status: statusFilter === 'all' ? undefined : statusFilter,
    service: serviceFilter || undefined,
    priority: prioParam,
    ownerTeam: ownerTeam || undefined,
    sreTeam: sreTeam || undefined,
    limit: 200,
  });
  const data: Problem[] | null | undefined = problemsQ.isLoading
    ? undefined
    : problemsQ.isError
      ? null
      : (problemsQ.data ?? []);

  const open = data?.filter(p => p.status === 'open').length ?? 0;
  const resolved = data?.filter(p => p.status === 'resolved').length ?? 0;

  // Severity chip filter stays client-side; priority is filtered
  // server-side via the priority query param so the limit cap
  // bites the right bucket. Keeping the severity client-side
  // filter avoids a refetch on every chip toggle for that axis.
  const rows = useMemo(
    () => (data ?? []).filter(p => sevSet.has(p.severity)),
    [data, sevSet]);
  // Shared DataTable primitive, client sort (PROBLEM_COLS carries the
  // per-column accessors + natural directions the old cmp/toggleSort
  // pair encoded). Sort + widths persist under 'alert-rules'; the URL
  // param is `s_alert-rules`, namespaced so it can't collide with the
  // exception inbox's `s_exception-inbox` on the same page.
  const dt = useDataTable<Problem>({
    storageKey: 'alert-rules',
    columns: PROBLEM_COLS,
    rows,
    initialSort: { id: 'priority', dir: 'desc' },
  });
  // Preserve the tri-state contract (undefined loading / null error /
  // rows) the render below branches on.
  const sorted = data == null ? data : dt.sortedRows;

  // Counts per severity for the chip labels — operator sees
  // "critical (3)" instead of guessing how many would land.
  const sevCounts = useMemo(() => {
    const counts = { critical: 0, warning: 0, info: 0 } as Record<string, number>;
    for (const p of data ?? []) counts[p.severity] = (counts[p.severity] ?? 0) + 1;
    return counts;
  }, [data]);

  // Whole section collapses when there's nothing AND filter is
  // 'open' — no point in dead space when the operator's most-
  // common scan finds zero firing rules.
  if (statusFilter === 'open' && data && data.length === 0) {
    return (
      <div style={{ marginTop: 22, marginBottom: 12 }}>
        <SectionHeader title="Alert rules" subtitle="Threshold + SLO burn detectors" />
        <Empty icon="✓" title="No open alerts — all clear!">
          The evaluator runs once per minute. Built-in rules cover error rate and P99 latency.
        </Empty>
      </div>
    );
  }

  return (
    <div style={{ marginTop: 22, marginBottom: 12 }}>
      <SectionHeader title="Alert rules" subtitle="Threshold + SLO burn detectors" />
      {/* One grouped facet bar (v0.8.39) — status pivot + severity +
          priority chips share the shared .facet primitive (the repo
          equivalent of the design's filter bar), replacing the old
          per-row ad-hoc inline-styled chips so the Alert-rules filters
          read with the same visual language as the rest of the app.
          Handlers + state are unchanged: status pivot single-select via
          setStatusFilter; severity/priority multi-select via toggleSev/
          togglePrio. Count + manage-rules link stay pushed right. */}
      <div className="facetbar" style={{ marginBottom: 14 }}>
        {/* Status pivot — single-select */}
        {(['open', 'resolved', 'all'] as const).map(s => (
          <span key={s} onClick={() => setStatusFilter(s)}
            className={`facet${statusFilter === s ? ' on' : ''}`}>
            {s.charAt(0).toUpperCase() + s.slice(1)}
          </span>
        ))}
        {/* Severity chip filter — multi-select toggle. Counts reflect
            the unfiltered status-tab result so the operator sees how
            many would land if they toggle a chip back on. At least one
            chip stays on at all times (toggleSev guard) — empty table
            looks broken. Severity tint (f-err/f-warn) keeps the urgency
            cue even when the chip is off. */}
        {(['critical', 'warning', 'info'] as const).map(s => {
          const on = sevSet.has(s);
          const tint = s === 'critical' ? ' f-err' : s === 'warning' ? ' f-warn' : '';
          return (
            <span key={s} onClick={() => toggleSev(s)}
              title={on ? `Hide ${s}` : `Show ${s} only — click again to add`}
              className={`facet${tint}${on ? ' on' : ''}`}>
              {s} <span className="n">{sevCounts[s] ?? 0}</span>
            </span>
          );
        })}
        {/* Priority chip filter (v0.5.210) — defaults to P1+P2 so the
            operator's first paint is signal, not noise. Click P3 to
            widen. Counts reflect the unfiltered set. */}
        {(['P1', 'P2', 'P3'] as const).map(pp => {
          const on = prioSet.has(pp);
          const tint = pp === 'P1' ? ' f-err' : pp === 'P2' ? ' f-warn' : '';
          const count = data?.filter(d => (d.priority ?? 'P3') === pp).length ?? 0;
          return (
            <span key={pp} onClick={() => togglePrio(pp)}
              title={on ? `Hide ${pp}` : `Show ${pp}`}
              className={`facet${tint}${on ? ' on' : ''}`}>
              {pp} <span className="n">{count}</span>
            </span>
          );
        })}
        {/* Owner / SRE team filters (v0.8.290) — mirror the Services
            page. Plain <select> for these small catalog-derived sets
            (frontend-conventions §3 permits <select> for ≤~10 fixed
            values). Server resolves the pick with the same EqualFold
            / empty-means-all filter the inbox uses, so the narrowing
            is correct across the whole result, not just the loaded
            rows. Options come from the catalog so they stay stable
            when a pick narrows the list. */}
        <select value={ownerTeam}
          onChange={e => setOwnerTeam(e.target.value)}
          aria-label="Filter by owner team"
          style={{ minWidth: 130 }}>
          <option value="">All owner teams</option>
          {ownerTeamOptions.map(t => (
            <option key={t} value={t}>{t}</option>
          ))}
        </select>
        <select value={sreTeam}
          onChange={e => setSreTeam(e.target.value)}
          aria-label="Filter by SRE team"
          style={{ minWidth: 130 }}>
          <option value="">All SRE teams</option>
          {sreTeamOptions.map(t => (
            <option key={t} value={t}>{t}</option>
          ))}
        </select>
        <span style={{ marginLeft: 'auto', color: 'var(--text3)', fontSize: 12 }}>
          {open} open · {resolved} resolved
        </span>
        <Link to="/alerts" className="sec" style={{
          textDecoration: 'none', padding: '5px 12px',
          border: '1px solid var(--border)', borderRadius: 6, fontSize: 12, color: 'var(--text)',
          display: 'inline-flex', alignItems: 'center', gap: 6,
        }}><IconBell /> <span>Manage alert rules</span></Link>
      </div>

      {data === undefined && <Spinner />}
      {data && sorted && sorted.length === 0 && (
        <Empty icon="✓" title={`No problems in "${statusFilter}"`}>
          Switch the filter above to see other states.
        </Empty>
      )}
      {sorted && sorted.length > 0 && selectedIds.size > 0 && (
        <div style={{
          padding: '8px 12px', marginBottom: 8,
          borderRadius: 6, background: 'var(--bg2)',
          border: '1px solid var(--accent2)',
          display: 'flex', alignItems: 'center', gap: 10,
          fontSize: 12,
        }}>
          <span style={{ color: 'var(--accent2)', fontWeight: 600 }}>
            {selectedIds.size} selected
          </span>
          <Button variant="secondary" size="sm" onClick={() => setSelectedIds(new Set())}>
            Clear
          </Button>
          <span style={{ flex: 1 }} />
          <Button variant="primary" disabled={bulkBusy}
            onClick={async () => {
              if (bulkBusy) return;
              setBulkBusy(true);
              try {
                await api.acknowledgeProblems([...selectedIds]);
                setSelectedIds(new Set());
                problemsQ.refetch();
              } catch {
                // toast surface lives globally; swallow here
              } finally {
                setBulkBusy(false);
              }
            }}>
            {bulkBusy ? 'Acknowledging…' : 'Acknowledge'}
          </Button>
        </div>
      )}
      {sorted && sorted.length > 0 && (
        <div className="table-wrap">
          <table style={{ tableLayout: 'fixed', width: '100%' }}>
            <DataTableColgroup dt={dt} leading={[28]} trailing={[170, 90]} />
            <DataTableHead dt={dt}
              leading={
                <th style={{ width: 28 }}>
                  <input type="checkbox"
                    checked={sorted.length > 0 && sorted.every(p => selectedIds.has(p.id))}
                    onChange={e => {
                      if (e.target.checked) {
                        setSelectedIds(new Set(sorted.map(p => p.id)));
                      } else {
                        setSelectedIds(new Set());
                      }
                    }}
                    onClick={e => e.stopPropagation()}
                    title="Select all visible" />
                </th>
              }
              trailing={<><th>Assignee</th><th>Triage</th></>} />
            <tbody>
              {sorted.map(p => {
                const isAnomaly = p.ruleId?.startsWith('anomaly:');
                return (
                  <tr key={p.id}
                      onClick={() => navigate(`/service?name=${encodeURIComponent(p.service)}`)}
                      onKeyDown={(e) => {
                        // Keyboard accessibility — mirror the exception-row
                        // pattern so screen-reader + keyboard users can open
                        // a Problem's service the same way a click does.
                        if (e.key === 'Enter' || e.key === ' ') {
                          e.preventDefault();
                          navigate(`/service?name=${encodeURIComponent(p.service)}`);
                        }
                      }}
                      tabIndex={0}
                      role="button"
                      style={{
                        cursor: 'pointer', contentVisibility: 'auto', containIntrinsicSize: 'auto 44px',
                        // Subtle err tint on open critical firings (prototype cue).
                        background: p.status === 'open' && p.severity === 'critical'
                          ? 'color-mix(in srgb, var(--err) 7%, transparent)'
                          : undefined,
                      }}>
                      <td onClick={e => e.stopPropagation()}>
                        <input type="checkbox"
                          checked={selectedIds.has(p.id)}
                          onChange={e => {
                            setSelectedIds(prev => {
                              const next = new Set(prev);
                              if (e.target.checked) next.add(p.id);
                              else next.delete(p.id);
                              return next;
                            });
                          }} />
                      </td>
                      <td><PriorityBadge p={p.priority} reason={p.priorityReason} /></td>
                      <td><SeverityBadge s={p.severity} /></td>
                      <td>
                        <Link to={`/service?name=${encodeURIComponent(p.service)}`}
                          onClick={e => e.stopPropagation()}
                          style={{ fontWeight: 600 }}>
                          {p.service}
                        </Link>
                        <ClusterChips clusters={p.clusters} />
                      </td>
                      <td className="mono">{p.metric}</td>
                      <td className="mono" style={{ textAlign: 'right' }}>
                        <b style={{ color: 'var(--err)' }}>{fmtFixed(p.value, 2)}</b>
                        <span style={{ color: 'var(--text3)' }}> / {fmtFixed(p.threshold, 2)}</span>
                      </td>
                      <td style={{ fontSize: 12 }}>
                        {isAnomaly && (
                          <span className="badge b-info" style={{ marginRight: 6 }}>ANOMALY</span>
                        )}
                        {p.ruleName}
                        {p.runbookUrl && (
                          <a href={p.runbookUrl} target="_blank" rel="noopener"
                            onClick={e => e.stopPropagation()}
                            title="Open team runbook"
                            className="badge b-info"
                            style={{ marginLeft: 8, textDecoration: 'none' }}>
                            Runbook ↗
                          </a>
                        )}
                        {p.recentDeploy && (
                          // Deploy correlation tag — shows the
                          // service.version that landed in the 30 min
                          // before the problem fired. The classic
                          // "regression coincided with deploy" signal
                          // in a single chip. Amber so it visually
                          // codes as "warning, look here".
                          <span className="badge b-warn"
                            onClick={e => e.stopPropagation()}
                            title={`service.version=${p.recentDeploy.version} first seen ${fmtAge(p.recentDeploy.ageSeconds)} before this problem opened`}
                            style={{ marginLeft: 8 }}>
                            <ArrowDownToLine size={11} strokeWidth={1.75} /> {p.recentDeploy.version} · {fmtAge(p.recentDeploy.ageSeconds)} before
                          </span>
                        )}
                        {p.aiSummary && (
                          // AI auto-explain chip (v0.5.254). The
                          // background problemExplainer fills this
                          // within ~30s of a critical fire; tooltip
                          // shows the full blurb so the operator
                          // gets first-look context without
                          // clicking through. The IconSparkles glyph is
                          // the "Copilot output" visual anchor, matching
                          // the existing operator-clicked Explain affordances.
                          <span className="badge b-info"
                            onClick={e => e.stopPropagation()}
                            title={p.aiSummary}
                            style={{ marginLeft: 8, cursor: 'help' }}>
                            <IconSparkles size={11} /> AI insight
                          </span>
                        )}
                        {/* v0.6.29 — blast radius chip for open
                            problems. Lazy-fetches when the row
                            renders; only shows when callers > 0
                            so the chip is silent on a service
                            with no upstream callers. Cascade
                            count surfaces in amber. */}
                        {p.status === 'open' && p.service && (
                          <BlastRadiusChip service={p.service} />
                        )}
                        {isAnomaly && (
                          <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 2 }}>
                            {p.description}
                          </div>
                        )}
                        {p.aiSummary && (
                          <div style={{
                            fontSize: 11, color: 'var(--text2)', marginTop: 4,
                            padding: 6, borderRadius: 4,
                            background: 'var(--accent-soft)',
                            borderLeft: '2px solid var(--accent)',
                            whiteSpace: 'pre-wrap',
                          }}>
                            {p.aiSummary}
                          </div>
                        )}
                        {/* rc #3 — in-page root-cause ribbon. Collapsed chip
                            renders from the row's persisted summary
                            (p.rootCause, joined by the /problems handler — no
                            fetch); expand reads the full /rootcause fan-out.
                            The chip's own stopPropagation keeps the row's
                            navigate-on-click intact. */}
                        <div style={{ marginTop: p.aiSummary ? 6 : 2 }}>
                          <RootCauseRibbon anchor="problem" id={p.id} summary={p.rootCause} />
                        </div>
                      </td>
                      <td className="mono">{tsLong(p.startedAt)}</td>
                      <td>
                        {p.status === 'open' && <span className="badge b-err">OPEN</span>}
                        {p.status === 'acknowledged' && <span className="badge b-warn">ACK</span>}
                        {p.status === 'resolved' && <span className="badge b-ok">RESOLVED</span>}
                      </td>
                      <td onClick={e => e.stopPropagation()} style={{ fontSize: 12 }}>
                        <AssigneeCell problem={p}
                          currentUserEmail={currentUserEmail}
                          onChanged={() => problemsQ.refetch()} />
                      </td>
                      <td onClick={e => e.stopPropagation()}>
                        {/* Triage — opens the right-side drawer
                            consolidating rule details + causal
                            correlation + AI explain + runbook
                            AI in one panel. Replaces the v0.5.x
                            inline "Why?" expansion and the
                            scattered per-cell AI buttons. */}
                        <Button variant="secondary" size="sm"
                          onClick={() => openDrawer(p.id)}>
                          Triage ▶
                        </Button>
                      </td>
                    </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
      {drawerProblemId && data && (() => {
        const p = data.find(x => x.id === drawerProblemId);
        if (!p) return null;
        return <TriageDrawer problem={p} onClose={() => openDrawer(null)} />;
      })()}
    </div>
  );
}

// TriageDrawer — right-side slide-in panel consolidating
// everything an oncall needs to triage one problem: severity
// + service + cluster + recent deploy + rule details +
// causal correlation + AI explain + runbook AI. Replaces the
// pre-v0.5.80 inline "Why?" expansion and the per-cell AI
// buttons; the operator stays on the list page, the drawer
// owns the focused view. Click backdrop or hit Escape to
// close.
function TriageDrawer({ problem, onClose }: {
  problem: Problem;
  onClose: () => void;
}) {
  const isAnomaly = problem.ruleId?.startsWith('anomaly:');
  return (
    <>
      <div onClick={onClose}
        style={{
          position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.35)',
          zIndex: 30, animation: 'fadeIn 120ms ease-out',
        }} />
      <div style={{
        position: 'fixed', right: 0, top: 0, bottom: 0,
        width: 'min(560px, 100vw)',
        background: 'var(--bg)', borderLeft: '1px solid var(--border)',
        boxShadow: '-4px 0 24px rgba(0,0,0,0.3)',
        zIndex: 31, overflowY: 'auto',
        animation: 'slideInRight 180ms ease-out',
      }}>
        <div style={{
          padding: '14px 18px', borderBottom: '1px solid var(--border)',
          display: 'flex', alignItems: 'center', gap: 10,
        }}>
          <SeverityBadge s={problem.severity} />
          <Link to={`/service?name=${encodeURIComponent(problem.service)}`}
            style={{ fontWeight: 700, fontSize: 14 }}>
            {problem.service}
          </Link>
          <ClusterChips clusters={problem.clusters} />
          <span style={{ flex: 1 }} />
          <button onClick={onClose} className="sec"
            title="Close (Esc)"
            style={{ fontSize: 14, padding: '2px 10px' }}>×</button>
        </div>

        <div style={{ padding: '14px 18px', display: 'flex', flexDirection: 'column', gap: 12 }}>
          <div>
            <div style={{ fontSize: 11, color: 'var(--text3)', textTransform: 'uppercase', letterSpacing: 0.4 }}>Rule</div>
            <div style={{ fontSize: 13 }}>
              {isAnomaly && <span className="badge b-info" style={{ marginRight: 6 }}>ANOMALY</span>}
              {problem.ruleName}
            </div>
          </div>

          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 10, fontSize: 12 }}>
            <div>
              <div style={{ color: 'var(--text3)' }}>Metric</div>
              <div className="mono">{problem.metric}</div>
            </div>
            <div>
              <div style={{ color: 'var(--text3)' }}>Value / Threshold</div>
              <div className="mono">
                <b style={{ color: 'var(--err)' }}>{problem.value.toFixed(2)}</b>
                <span style={{ color: 'var(--text3)' }}> / {fmtFixed(problem.threshold, 2)}</span>
              </div>
            </div>
            <div>
              <div style={{ color: 'var(--text3)' }}>Started</div>
              <div className="mono">{tsLong(problem.startedAt)}</div>
            </div>
            <div>
              <div style={{ color: 'var(--text3)' }}>Status</div>
              <div>
                {problem.status === 'open'
                  ? <span className="badge b-err">OPEN</span>
                  : <span className="badge b-ok">RESOLVED</span>}
              </div>
            </div>
          </div>

          {problem.recentDeploy && (
            <div style={{
              padding: '8px 12px', borderRadius: 6,
              background: 'color-mix(in srgb, var(--warn) 10%, transparent)',
              border: '1px solid color-mix(in srgb, var(--warn) 40%, transparent)',
              fontSize: 12, color: 'var(--text)',
            }}>
              <div style={{ fontWeight: 600, marginBottom: 2, display: 'flex', alignItems: 'center', gap: 6 }}>
                <ArrowDownToLine size={13} strokeWidth={1.75} /> Recent deploy correlation
              </div>
              <div>
                service.version=<code>{problem.recentDeploy.version}</code> first seen{' '}
                <b>{fmtAge(problem.recentDeploy.ageSeconds)}</b> before this problem opened.
              </div>
            </div>
          )}

          {problem.description && !isAnomaly && (
            <div style={{ fontSize: 12, color: 'var(--text2)', lineHeight: 1.5 }}>
              {problem.description}
            </div>
          )}

          <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
            {problem.runbookUrl && (
              <a href={problem.runbookUrl} target="_blank" rel="noopener"
                style={{
                  fontSize: 12, padding: '4px 12px', borderRadius: 4,
                  background: 'var(--accent-soft)',
                  border: '1px solid var(--accent)',
                  color: 'var(--accent2)', textDecoration: 'none',
                }}>
                Runbook ↗
              </a>
            )}
            <CopilotExplain kind="problem" id={problem.id}
              label={<><IconSparkles /> <span>Explain</span></>} />
            <CopilotExplain kind="runbook" id={problem.id}
              label={<><IconSparkles /> <span>Runbook AI</span></>} />
          </div>

          {/* Problem→Runbook bridge: run an operational runbook against this
              fire (tagged with problemId) + the runs already attached. */}
          <ProblemRunbookPanel problemId={problem.id} />

          <div style={{ marginTop: 4 }}>
            <div style={{
              fontSize: 11, color: 'var(--text3)',
              textTransform: 'uppercase', letterSpacing: 0.4,
              marginBottom: 6,
            }}>Root cause analysis</div>
            <RootCausePanel problemId={problem.id} service={problem.service} />
          </div>
        </div>
      </div>
    </>
  );
}

function SectionHeader({ title, subtitle }: { title: string; subtitle?: string }) {
  return (
    <div style={{
      display: 'flex', alignItems: 'baseline', gap: 10,
      marginBottom: 10, paddingBottom: 6,
      borderBottom: '1px solid var(--border)',
    }}>
      <span style={{ fontSize: 14, fontWeight: 700 }}>{title}</span>
      {subtitle && (
        <span style={{ fontSize: 11, color: 'var(--text3)' }}>{subtitle}</span>
      )}
    </div>
  );
}

function SeverityBadge({ s }: { s: string }) {
  const cls = s === 'critical' ? 'b-err' : s === 'warning' ? 'b-warn' : 'b-info';
  return <span className={`badge ${cls}`}>{s.toUpperCase()}</span>;
}

// PriorityBadge — v0.5.210 triage column. P1 / P2 / P3 pill with
// a colour that matches the urgency stack (red/amber/grey).
// `reason` flows into the title attribute so an operator can
// hover and see WHY the bucket was picked ("critical + deploy
// 4m before") — the blend formula is transparent, not magic.
function PriorityBadge({ p, reason }: { p?: 'P1' | 'P2' | 'P3'; reason?: string }) {
  if (!p) return <span style={{ color: 'var(--text3)' }}>—</span>;
  const cls = p === 'P1' ? 'b-err' : p === 'P2' ? 'b-warn' : 'b-gray';
  return (
    <span className={`badge ${cls}`} title={reason ? `${p} — ${reason}` : p}>
      {p}
    </span>
  );
}

// SamplesPanel — fetches recent occurrences for the group and lists them
// as collapsible cards. Stacktraces are folded by default; trace/span IDs
// link out to the waterfall.
function SamplesPanel({ fingerprint }: { fingerprint: string }) {
  const [samples, setSamples] = useState<ExceptionSample[] | null | undefined>(undefined);
  const [limit, setLimit] = useState(10);

  useEffect(() => {
    setSamples(undefined);
    api.exceptionGroupSamples(fingerprint, limit)
      .then(s => setSamples(s ?? [])).catch(() => setSamples(null));
  }, [fingerprint, limit]);

  if (samples === undefined) return <Spinner />;
  if (!samples || samples.length === 0) {
    return <div style={{ color: 'var(--text3)', fontSize: 12 }}>No sample occurrences found.</div>;
  }

  const distinct = new Set(samples.map(s => s.message)).size;

  return (
    <div>
      <div style={{ display: 'flex', alignItems: 'center', marginBottom: 8 }}>
        <span style={{ fontSize: 12, color: 'var(--text2)', fontWeight: 600 }}>
          Recent occurrences
        </span>
        {distinct > 1 && (
          <span style={{ marginLeft: 8, fontSize: 11, color: 'var(--text3)' }}>
            · {distinct} distinct messages observed in this group
          </span>
        )}
        <span style={{ flex: 1 }} />
        <label style={{ fontSize: 11, color: 'var(--text3)', marginRight: 6 }}>Show</label>
        <select value={limit} onChange={e => setLimit(parseInt(e.target.value))}
          style={{ fontSize: 11 }}>
          <option value={5}>5</option>
          <option value={10}>10</option>
          <option value={25}>25</option>
          <option value={50}>50</option>
        </select>
      </div>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
        {samples.map((s, i) => <SampleCard key={`${s.spanId}-${i}`} sample={s} index={i + 1} />)}
      </div>
    </div>
  );
}

function SampleCard({ sample, index }: { sample: ExceptionSample; index: number }) {
  const [showTrace, setShowTrace] = useState(false);
  const hasTrace = sample.stacktrace.trim().length > 0;
  return (
    <div style={{
      background: 'var(--bg2)', border: '1px solid var(--border)',
      borderRadius: 6, padding: 10,
    }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, fontSize: 12 }}>
        <span style={{ color: 'var(--text3)', fontFamily: 'monospace' }}>#{index}</span>
        <Link to={`/trace?id=${sample.traceId}`} style={{ fontFamily: 'monospace' }}>
          {sample.traceId.slice(0, 12)}…
        </Link>
        <span style={{ color: 'var(--text2)', fontFamily: 'monospace', fontSize: 11 }}>
          span <code>{sample.spanId.slice(0, 8)}</code>
          {sample.spanName && <> · <b>{sample.spanName}</b></>}
        </span>
        <span style={{ flex: 1 }} />
        <span style={{ color: 'var(--text3)', fontSize: 11 }}>{tsLong(sample.time)}</span>
      </div>
      {sample.message && (
        <div style={{ fontSize: 12, color: 'var(--text)', marginTop: 6,
                      fontFamily: 'monospace', wordBreak: 'break-word' }}>
          {sample.message}
        </div>
      )}
      {sample.statusMsg && sample.statusMsg !== sample.message && (
        <div style={{ fontSize: 11, color: 'var(--text2)', marginTop: 4, fontFamily: 'monospace' }}>
          status: {sample.statusMsg}
        </div>
      )}
      {hasTrace && (
        <>
          <Button variant="secondary" size="sm" style={{ marginTop: 8 }}
            onClick={() => setShowTrace(t => !t)}>
            {showTrace
              ? <><ChevronDown size={12} strokeWidth={1.75} /> Hide stacktrace</>
              : <><ChevronRight size={12} strokeWidth={1.75} /> Show stacktrace</>}
          </Button>
          {showTrace && (
            <pre style={{
              marginTop: 6, padding: 10, background: 'var(--bg)',
              border: '1px solid var(--border)', borderRadius: 4,
              fontSize: 11, lineHeight: 1.45, overflow: 'auto', maxHeight: 280,
              whiteSpace: 'pre', fontFamily: 'monospace',
            }}>{sample.stacktrace}</pre>
          )}
        </>
      )}
    </div>
  );
}

function ActionButtons({ g, onSet }: {
  g: ExceptionGroup; onSet: (g: ExceptionGroup, s: ExceptionGroupState) => void;
}) {
  // Compact secondary actions in a flex row — uniform gap instead of
  // per-button margins, all on the canonical secondary/sm Button.
  const row = (...kids: React.ReactNode[]) => (
    <span style={{ display: 'inline-flex', gap: 4 }}>{kids}</span>
  );
  switch (g.state) {
    case 'new':
    case 'regressed':
      return row(
        <Button key="ack" variant="secondary" size="sm" onClick={() => onSet(g, 'acknowledged')}>Ack</Button>,
        <Button key="res" variant="secondary" size="sm" onClick={() => onSet(g, 'resolved')}>Resolve</Button>,
        <Button key="ign" variant="secondary" size="sm" onClick={() => onSet(g, 'ignored')}>Ignore</Button>,
      );
    case 'acknowledged':
      return row(
        <Button key="res" variant="secondary" size="sm" onClick={() => onSet(g, 'resolved')}>Resolve</Button>,
        <Button key="reo" variant="secondary" size="sm" onClick={() => onSet(g, 'new')}>Reopen</Button>,
        <Button key="ign" variant="secondary" size="sm" onClick={() => onSet(g, 'ignored')}>Ignore</Button>,
      );
    case 'resolved':
      return <Button variant="secondary" size="sm" onClick={() => onSet(g, 'new')}>Reopen</Button>;
    case 'ignored':
      return <Button variant="secondary" size="sm" onClick={() => onSet(g, 'new')}>Unignore</Button>;
  }
  return null;
}

function StateBadge({ s }: { s: ExceptionGroupState }) {
  const cls =
    s === 'new'          ? 'b-err'  :
    s === 'regressed'    ? 'b-warn' :
    s === 'acknowledged' ? 'b-info' :
    s === 'resolved'     ? 'b-ok'   :
                           'b-gray';
  // v0.8.382 (operator-reported): the untriaged state used to render
  // "NEW" — the same word as the yellow first-seen-recently badge next
  // to the exception type, so genuinely-new arrivals were
  // indistinguishable from merely-untriaged weeks-old groups. The
  // STATE is "nobody triaged this yet" → OPEN; NEW stays reserved for
  // the first-seen marker.
  const label = s === 'new' ? 'OPEN' : s.toUpperCase();
  return <span className={`badge ${cls}`} title={s === 'new' ? 'Untriaged (state: new)' : undefined}>{label}</span>;
}

function humanize(err: unknown): string {
  const msg = err instanceof Error ? err.message : String(err);
  const body = msg.replace(/^HTTP \d+:\s*/, '');
  try {
    const j = JSON.parse(body);
    if (j && typeof j.error === 'string') return j.error;
  } catch {}
  return body || msg;
}

// AssigneeCell — v0.5.209 triage column. Renders the current
// assignee (team name auto-set on open from service_metadata.
// ownerTeam, OR an operator email after manual claim), with two
// inline affordances:
//   • "Take it" — PATCH self-email when the problem is
//     unassigned or assigned to a team. One click, no modal.
//   • Click-to-edit — prompt() lets the operator type any
//     value (reassign to a teammate / change team / clear).
// Kept dependency-light: no inline picker component, no
// suggestions list. v2 can promote this to a typeahead against
// the users table if the prompt() ergonomics annoy operators.
function AssigneeCell({ problem, currentUserEmail, onChanged }: {
  problem: Problem;
  currentUserEmail: string;
  onChanged: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const assignee = problem.assignee ?? '';
  const isSelf = currentUserEmail !== '' && assignee === currentUserEmail;
  const isTeam = assignee !== '' && !assignee.includes('@');

  const set = async (next: string) => {
    if (busy || next === assignee) return;
    setBusy(true);
    try { await api.setProblemAssignee(problem.id, next); onChanged(); }
    finally { setBusy(false); }
  };
  const editPrompt = () => {
    const v = window.prompt('Assignee (email or team name; empty = unassign):', assignee);
    if (v === null) return;
    void set(v.trim());
  };

  return (
    <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
      {assignee
        ? (
          <span onClick={editPrompt}
            title="Click to reassign or clear"
            className={`badge ${isSelf ? 'b-ok' : 'b-info'}`}
            style={{ cursor: 'pointer' }}>
            {isTeam && <Users size={11} strokeWidth={1.75} />}{assignee}
          </span>
        )
        : <span style={{ color: 'var(--text3)' }}>—</span>}
      {currentUserEmail !== '' && !isSelf && (
        <Button variant="secondary" size="sm" disabled={busy}
          onClick={() => void set(currentUserEmail)}
          title="Claim this problem for yourself">
          Take it
        </Button>
      )}
    </span>
  );
}

// fmtAge — compact "Nm" / "Nh" / "Ns" formatter for the deploy
// correlation tag. ageSeconds is always positive (deploy was
// before problem) but be defensive.
function fmtAge(sec: number): string {
  const s = Math.max(0, Math.round(sec));
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.round(s / 60)}m`;
  return `${Math.round(s / 3600)}h`;
}

// v0.6.29 — inline blast-radius chip for the /problems row.
// Lazy-fetches the per-service summary so a 100-row inbox
// doesn't fan out 100 parallel requests on first paint —
// individual chips load as their row renders. Hidden when the
// service has no upstream callers (silent on standalone
// services) so the row layout stays clean.
//
// Cascade callers (services with their own open problem)
// shift the chip to amber + the count surfaces in the tooltip
// so the operator sees "this isn't isolated — 3 downstream
// services are already firing too" without expanding the row.
function BlastRadiusChip({ service }: { service: string }) {
  const [data, setData] = useState<import('@/lib/types').BlastRadius | null>(null);
  useEffect(() => {
    let cancelled = false;
    api.serviceBlastRadius(service)
      .then(d => { if (!cancelled) setData(d); })
      .catch(() => { /* silent — chip just doesn't render */ });
    return () => { cancelled = true; };
  }, [service]);
  if (!data || data.totalCallers === 0) return null;
  const cascading = data.cascadingCallers > 0;
  const tooltipLines = [
    `Blast radius: ${data.totalCallers} caller service${data.totalCallers === 1 ? '' : 's'}, ${data.totalRps.toFixed(1)} rps`,
    cascading && `${data.cascadingCallers} caller${data.cascadingCallers === 1 ? '' : 's'} ALSO have an open problem (cascading failure)`,
    '',
    'Top callers:',
    ...data.callers.slice(0, 5).map(c =>
      `  ${c.hasOpenProblem ? '⚠ ' : '  '}${c.service} — ${c.rps.toFixed(1)} rps${c.errorRate > 1 ? ` · ${c.errorRate.toFixed(1)}% err` : ''}`,
    ),
  ].filter(Boolean).join('\n');
  return (
    <span
      title={tooltipLines}
      onClick={e => e.stopPropagation()}
      className={`badge ${cascading ? 'b-warn' : 'b-info'}`}
      style={{ marginLeft: 8, cursor: 'help' }}>
      <CornerDownRight size={11} strokeWidth={1.75} />
      {data.totalCallers} svc{data.totalCallers === 1 ? '' : 's'} · {data.totalRps.toFixed(0)} rps
      {cascading && <> · {data.cascadingCallers} cascading</>}
    </span>
  );
}

