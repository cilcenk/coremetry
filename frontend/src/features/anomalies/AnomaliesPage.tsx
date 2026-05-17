import { Fragment, useEffect, useState, useMemo } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { SavedViewsBar } from '@/components/SavedViewsBar';
import { Spinner, Empty } from '@/components/Spinner';
import { ServicePicker } from '@/components/ServicePicker';
import { useAuth } from '@/components/AuthProvider';
import { CopilotExplain } from '@/components/CopilotExplain';
import { ClusterChips } from '@/components/ClusterChips';
import { IconBell, IconSparkles } from '@/components/icons';
import { useProblems, keys } from '@/lib/queries';
import { useQueryClient } from '@tanstack/react-query';
import { api, type UserRow } from '@/lib/api';
import { fmtNum, tsLong } from '@/lib/utils';
import type {
  ExceptionGroup, ExceptionGroupState, ExceptionSample, Problem,
} from '@/lib/types';

// State buckets shown as tabs along the top of the page.
const TABS: { key: string; label: string; hint: string }[] = [
  { key: 'open',         label: 'Inbox',        hint: 'New + acknowledged + regressed' },
  { key: 'new',          label: 'New',          hint: 'Untouched since first occurrence' },
  { key: 'acknowledged', label: 'Acknowledged', hint: 'Someone is on it' },
  { key: 'regressed',    label: 'Regressed',    hint: 'Resolved but happening again' },
  { key: 'resolved',     label: 'Resolved',     hint: 'Closed out' },
  { key: 'ignored',      label: 'Ignored',      hint: 'Permanently silenced' },
];

type SortKey = 'state' | 'type' | 'service' | 'occurrences' | 'firstSeen' | 'lastSeen' | 'assignee';
type SortDir = 'asc' | 'desc';

// Severity-style ordering for state column (worst at top desc-sorted)
const STATE_RANK: Record<string, number> = {
  new: 5, regressed: 4, acknowledged: 3, resolved: 2, ignored: 1,
};

const NATURAL_DIR: Record<SortKey, SortDir> = {
  state: 'desc', type: 'asc', service: 'asc',
  occurrences: 'desc', firstSeen: 'desc', lastSeen: 'desc', assignee: 'asc',
};

// Problems-specific sort + severity ordering — kept separate from the
// exception inbox table because the columns don't overlap.
type PSortKey = 'priority' | 'severity' | 'service' | 'metric' | 'value' | 'rule' | 'started' | 'status';
const SEV_RANK: Record<string, number> = { critical: 3, warning: 2, info: 1 };
// P1 ranks above P2 ranks above P3 (lower number = more urgent).
const PRIO_RANK: Record<string, number> = { P1: 3, P2: 2, P3: 1 };
const P_NATURAL_DIR: Record<PSortKey, SortDir> = {
  priority: 'desc', severity: 'desc', service: 'asc', metric: 'asc',
  value: 'desc', rule: 'asc', started: 'desc', status: 'asc',
};

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
  const setPage = (next: number | ((p: number) => number)) =>
    setSearchParams(prev => {
      const p = new URLSearchParams(prev);
      const v = typeof next === 'function' ? next(page) : next;
      if (v > 0) p.set('page', String(v)); else p.delete('page');
      return p;
    }, { replace: true });
  const [search, setSearch] = useState('');
  const [, setServices] = useState<string[]>([]);
  const [users, setUsers] = useState<UserRow[]>([]);
  const [data, setData] = useState<ExceptionGroup[] | null | undefined>(undefined);
  const [total, setTotal] = useState(0);
  const PAGE_SIZE = 50;
  // Expanded fingerprint(s) — multiple groups can be open at once for compare-and-contrast.
  const [expanded, setExpanded] = useState<Set<string>>(new Set());

  // Exception groups inbox — separate query because it depends
  // on tab + service filter; couldn't be folded into the shared
  // anomaly hooks above.
  const qc = useQueryClient();
  const refreshExceptionGroups = () => {
    setData(undefined);
    api.exceptionGroups({
      state: tab, service: service || undefined,
      limit: PAGE_SIZE, offset: page * PAGE_SIZE,
    })
      .then(d => { setData(d.items ?? []); setTotal(d.total ?? 0); })
      .catch(() => setData(null));
  };
  // Page reset on filter change is owned by setTab/setService now
  // (they delete ?page=). Single effect drives the fetch.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(refreshExceptionGroups, [tab, service, page]);

  useEffect(() => {
    api.services({ from: 0, to: 0 })
      .then(s => setServices((s ?? []).map(x => x.name))).catch(() => {});
  }, []);

  useEffect(() => {
    if (!isAdmin) return;
    api.listUsers().then(u => setUsers(u ?? [])).catch(() => {});
  }, [isAdmin]);

  const [sortBy, setSortBy] = useState<SortKey>('lastSeen');
  const [sortDir, setSortDir] = useState<SortDir>('desc');

  const filtered = useMemo(() => {
    const term = search.trim().toLowerCase();
    const list = (data ?? []).filter(g => {
      if (!term) return true;
      return g.type.toLowerCase().includes(term)
          || g.message.toLowerCase().includes(term)
          || g.service.toLowerCase().includes(term);
    });
    const cmp = (a: ExceptionGroup, b: ExceptionGroup): number => {
      switch (sortBy) {
        case 'state':       return (STATE_RANK[a.state] ?? 0) - (STATE_RANK[b.state] ?? 0);
        case 'type':        return a.type.localeCompare(b.type);
        case 'service':     return a.service.localeCompare(b.service);
        case 'occurrences': return Number(a.occurrences) - Number(b.occurrences);
        case 'firstSeen':   return a.firstSeen - b.firstSeen;
        case 'lastSeen':    return a.lastSeen  - b.lastSeen;
        case 'assignee':    return (a.assignee || '').localeCompare(b.assignee || '');
      }
    };
    const arr = [...list].sort(cmp);
    return sortDir === 'desc' ? arr.reverse() : arr;
  }, [data, search, sortBy, sortDir]);

  const toggleSort = (col: SortKey) => {
    if (sortBy === col) setSortDir(sortDir === 'desc' ? 'asc' : 'desc');
    else { setSortBy(col); setSortDir(NATURAL_DIR[col]); }
  };

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

  return (
    <>
      <Topbar title="Problems" />
      <div id="content">
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
            Click a row to inspect recent occurrences. Use Ack / Resolve / Ignore to manage state.
          </Empty>
        )}
        {data && filtered.length > 0 && (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th style={{ width: 24 }}></th>
                  <SortTh col="state"       label="State"       sort={sortBy} dir={sortDir} onSort={toggleSort} />
                  <SortTh col="type"        label="Exception"   sort={sortBy} dir={sortDir} onSort={toggleSort} />
                  <SortTh col="service"     label="Service"     sort={sortBy} dir={sortDir} onSort={toggleSort} />
                  <SortTh col="occurrences" label="Occurrences" sort={sortBy} dir={sortDir} onSort={toggleSort} align="right" />
                  <SortTh col="firstSeen"   label="First seen"  sort={sortBy} dir={sortDir} onSort={toggleSort} />
                  <SortTh col="lastSeen"    label="Last seen"   sort={sortBy} dir={sortDir} onSort={toggleSort} />
                  <SortTh col="assignee"    label="Assignee"    sort={sortBy} dir={sortDir} onSort={toggleSort} />
                  {isAdmin && <th style={{ width: 240 }}>Actions</th>}
                </tr>
              </thead>
              <tbody>
                {filtered.map(g => {
                  const open = expanded.has(g.fingerprint);
                  return (
                    <Fragment key={g.fingerprint}>
                      <tr onClick={() => toggleExpand(g.fingerprint)}
                        onKeyDown={(e) => {
                          // Keyboard accessibility — pre-v0.4.79 the row
                          // was mouse-only. Screen-reader + keyboard
                          // users couldn't expand a group. Enter/Space
                          // now toggles the same way as a click.
                          if (e.key === 'Enter' || e.key === ' ') {
                            e.preventDefault();
                            toggleExpand(g.fingerprint);
                          }
                        }}
                        tabIndex={0}
                        role="button"
                        aria-expanded={open}
                        style={{ cursor: 'pointer' }}>
                        <td style={{ color: 'var(--text3)', textAlign: 'center' }}>
                          {open ? '▾' : '▸'}
                        </td>
                        <td><StateBadge s={g.state} /></td>
                        <td>
                          <div style={{ display: 'flex', alignItems: 'center', gap: 6, fontWeight: 600, color: 'var(--err)' }}>
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
                          <div style={{ fontSize: 11, color: 'var(--text2)',
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
                        <td className="mono" style={{ textAlign: 'right' }}>
                          <span className="badge b-err">{fmtNum(Number(g.occurrences))}</span>
                        </td>
                        <td className="mono" style={{ fontSize: 11, color: 'var(--text3)' }}>{tsLong(g.firstSeen)}</td>
                        <td className="mono" style={{ fontSize: 11 }}>{tsLong(g.lastSeen)}</td>
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
            <button type="button" className="sec"
              disabled={page === 0}
              onClick={() => setPage(p => Math.max(0, p - 1))}
              style={{ fontSize: 11, padding: '3px 10px' }}>
              ← Prev
            </button>
            <span style={{ color: 'var(--text3)' }}>
              Page {page + 1} of {Math.max(1, Math.ceil(total / PAGE_SIZE))}
            </span>
            <button type="button" className="sec"
              disabled={(page + 1) * PAGE_SIZE >= total}
              onClick={() => setPage(p => p + 1)}
              style={{ fontSize: 11, padding: '3px 10px' }}>
              Next →
            </button>
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
  const [statusFilter, setStatusFilter] = useState<'open' | 'all' | 'resolved'>('open');
  const [sortBy, setSortBy] = useState<PSortKey>('priority');
  const [sortDir, setSortDir] = useState<SortDir>('desc');
  // Triage drawer state — id of the problem currently shown
  // in the right-side panel. Replaces the v0.5.x inline "Why?"
  // expansion; the same causal-correlation panel now lives
  // inside the drawer alongside the rule details + deploy
  // chip + AI buttons in one consolidated triage surface.
  const [drawerProblemId, setDrawerProblemId] = useState<string | null>(null);
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
        setDrawerProblemId(null);
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
    try {
      const raw = localStorage.getItem('problems.sev');
      if (raw) {
        const arr = JSON.parse(raw);
        if (Array.isArray(arr) && arr.length > 0) return new Set(arr);
      }
    } catch { /* ignore */ }
    return new Set(['critical', 'warning', 'info']);
  });
  // Priority filter — defaults to P1+P2 so the operator's inbox
  // surfaces signal first. P3 (steady warnings) is one click
  // away. Persisted alongside the severity set.
  const [prioSet, setPrioSet] = useState<Set<string>>(() => {
    try {
      const raw = localStorage.getItem('problems.prio');
      if (raw) {
        const arr = JSON.parse(raw);
        if (Array.isArray(arr) && arr.length > 0) return new Set(arr);
      }
    } catch { /* ignore */ }
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
      try { localStorage.setItem('problems.prio', JSON.stringify([...next])); } catch { /* ignore */ }
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
      try { localStorage.setItem('problems.sev', JSON.stringify([...next])); } catch { /* ignore */ }
      return next;
    });
  };
  // (Pre-v0.5.80 inline "Why?" expansion lived here; the
  // same correlation panel is now embedded inside the
  // triage drawer.)

  const problemsQ = useProblems({
    status: statusFilter === 'all' ? undefined : statusFilter,
    service: serviceFilter || undefined,
    limit: 200,
  });
  const data: Problem[] | null | undefined = problemsQ.isLoading
    ? undefined
    : problemsQ.isError
      ? null
      : (problemsQ.data ?? []);

  const open = data?.filter(p => p.status === 'open').length ?? 0;
  const resolved = data?.filter(p => p.status === 'resolved').length ?? 0;

  const sorted = useMemo(() => {
    if (!data) return data;
    // Apply severity + priority chip filters before sort so
    // the visible row count under the chips matches what's
    // rendered.
    const filtered = data.filter(p =>
      sevSet.has(p.severity) &&
      prioSet.has(p.priority ?? 'P3'));
    const cmp = (a: Problem, b: Problem): number => {
      switch (sortBy) {
        case 'priority': {
          const ra = PRIO_RANK[a.priority ?? 'P3'] ?? 0;
          const rb = PRIO_RANK[b.priority ?? 'P3'] ?? 0;
          if (ra !== rb) return ra - rb;
          // Same priority bucket — break ties by severity then
          // by start time so the operator gets a stable order
          // within a bucket.
          const sa = SEV_RANK[a.severity] ?? 0;
          const sb = SEV_RANK[b.severity] ?? 0;
          if (sa !== sb) return sa - sb;
          return a.startedAt - b.startedAt;
        }
        case 'severity': return (SEV_RANK[a.severity] ?? 0) - (SEV_RANK[b.severity] ?? 0);
        case 'service':  return a.service.localeCompare(b.service);
        case 'metric':   return a.metric.localeCompare(b.metric);
        case 'value':    return a.value - b.value;
        case 'rule':     return a.ruleName.localeCompare(b.ruleName);
        case 'started':  return a.startedAt - b.startedAt;
        case 'status':   return a.status.localeCompare(b.status);
      }
    };
    const arr = [...filtered].sort(cmp);
    return sortDir === 'desc' ? arr.reverse() : arr;
  }, [data, sortBy, sortDir, sevSet, prioSet]);

  // Counts per severity for the chip labels — operator sees
  // "critical (3)" instead of guessing how many would land.
  const sevCounts = useMemo(() => {
    const counts = { critical: 0, warning: 0, info: 0 } as Record<string, number>;
    for (const p of data ?? []) counts[p.severity] = (counts[p.severity] ?? 0) + 1;
    return counts;
  }, [data]);

  const toggleSort = (col: PSortKey) => {
    if (sortBy === col) setSortDir(sortDir === 'desc' ? 'asc' : 'desc');
    else { setSortBy(col); setSortDir(P_NATURAL_DIR[col]); }
  };

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
      <div className="controls" style={{ marginBottom: 14 }}>
        <div style={{ display: 'flex', border: '1px solid var(--border)', borderRadius: 6, overflow: 'hidden' }}>
          {(['open', 'resolved', 'all'] as const).map(s => (
            <button key={s} onClick={() => setStatusFilter(s)}
              className={statusFilter === s ? '' : 'sec'}
              style={{ borderRadius: 0, borderRight: '1px solid var(--border)' }}>
              {s.charAt(0).toUpperCase() + s.slice(1)}
            </button>
          ))}
        </div>
        <span style={{ color: 'var(--text2)', fontSize: 12 }}>
          {open} open · {resolved} resolved
        </span>
        {/* Severity chip filter — multi-select toggle. Counts
            reflect the unfiltered status-tab result so the
            operator sees how many would land if they toggle a
            chip back on. At least one chip stays on at all
            times (toggleSev guard) — empty table looks
            broken. */}
        <div style={{ display: 'flex', gap: 4 }}>
          {(['critical', 'warning', 'info'] as const).map(s => {
            const on = sevSet.has(s);
            const colour = s === 'critical' ? 'var(--err)'
                         : s === 'warning'  ? 'var(--warn)'
                         : 'var(--accent2)';
            return (
              <button key={s} onClick={() => toggleSev(s)}
                title={on ? `Hide ${s}` : `Show ${s} only — click again to add`}
                style={{
                  all: 'unset', cursor: 'pointer',
                  fontSize: 11, padding: '2px 8px', borderRadius: 12,
                  fontFamily: 'ui-monospace, monospace',
                  border: `1px solid ${on ? colour : 'var(--border)'}`,
                  background: on ? colour : 'transparent',
                  color: on ? 'var(--bg)' : 'var(--text3)',
                  fontWeight: on ? 600 : 400,
                  textTransform: 'uppercase', letterSpacing: 0.4,
                }}>
                {s} ({sevCounts[s] ?? 0})
              </button>
            );
          })}
        </div>
        {/* Priority chip filter (v0.5.210) — defaults to P1+P2
            so the operator's first paint is signal, not noise.
            Click P3 to widen. Counts reflect the unfiltered set. */}
        <div style={{ display: 'flex', gap: 4 }}>
          {(['P1', 'P2', 'P3'] as const).map(pp => {
            const on = prioSet.has(pp);
            const count = data?.filter(d => (d.priority ?? 'P3') === pp).length ?? 0;
            const colour = pp === 'P1' ? 'var(--err)'
                        : pp === 'P2' ? 'var(--warn, #facc15)'
                        : 'var(--text3)';
            return (
              <button key={pp} onClick={() => togglePrio(pp)}
                title={on ? `Hide ${pp}` : `Show ${pp}`}
                style={{
                  all: 'unset', cursor: 'pointer',
                  fontSize: 11, padding: '2px 8px', borderRadius: 12,
                  fontFamily: 'ui-monospace, monospace',
                  border: `1px solid ${on ? colour : 'var(--border)'}`,
                  background: on ? colour : 'transparent',
                  color: on ? 'var(--bg)' : 'var(--text3)',
                  fontWeight: on ? 700 : 400,
                  letterSpacing: 0.4,
                }}>
                {pp} ({count})
              </button>
            );
          })}
        </div>
        <Link to="/alerts" className="sec" style={{
          marginLeft: 'auto', textDecoration: 'none', padding: '5px 12px',
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
          <button onClick={() => setSelectedIds(new Set())}
            className="sec" style={{ fontSize: 11, padding: '3px 8px' }}>
            Clear
          </button>
          <span style={{ flex: 1 }} />
          <button disabled={bulkBusy}
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
            }}
            style={{ fontSize: 12, padding: '4px 12px' }}>
            {bulkBusy ? 'Acknowledging…' : 'Acknowledge'}
          </button>
        </div>
      )}
      {sorted && sorted.length > 0 && (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
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
                <PSortTh col="priority" label="Priority" sort={sortBy} dir={sortDir} onSort={toggleSort} />
                <PSortTh col="severity" label="Severity" sort={sortBy} dir={sortDir} onSort={toggleSort} />
                <PSortTh col="service"  label="Service"  sort={sortBy} dir={sortDir} onSort={toggleSort} />
                <PSortTh col="metric"   label="Metric"   sort={sortBy} dir={sortDir} onSort={toggleSort} />
                <PSortTh col="value"    label="Value"    sort={sortBy} dir={sortDir} onSort={toggleSort} align="right" />
                <PSortTh col="rule"     label="Rule"     sort={sortBy} dir={sortDir} onSort={toggleSort} />
                <PSortTh col="started"  label="Started"  sort={sortBy} dir={sortDir} onSort={toggleSort} />
                <PSortTh col="status"   label="Status"   sort={sortBy} dir={sortDir} onSort={toggleSort} />
                <th>Assignee</th>
                <th>Triage</th>
              </tr>
            </thead>
            <tbody>
              {sorted.map(p => {
                const isAnomaly = p.ruleId?.startsWith('anomaly:');
                return (
                  <tr key={p.id}
                      onClick={() => navigate(`/service?name=${encodeURIComponent(p.service)}`)}
                      style={{ cursor: 'pointer' }}>
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
                        <b style={{ color: 'var(--err)' }}>{p.value.toFixed(2)}</b>
                        <span style={{ color: 'var(--text3)' }}> / {p.threshold.toFixed(2)}</span>
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
                            style={{
                              marginLeft: 8, fontSize: 11,
                              padding: '2px 8px', borderRadius: 12,
                              background: 'rgba(56,139,253,0.10)',
                              border: '1px solid rgba(56,139,253,0.35)',
                              color: 'var(--accent2)', textDecoration: 'none',
                              whiteSpace: 'nowrap',
                            }}>
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
                          <span
                            onClick={e => e.stopPropagation()}
                            title={`service.version=${p.recentDeploy.version} first seen ${fmtAge(p.recentDeploy.ageSeconds)} before this problem opened`}
                            style={{
                              marginLeft: 8, fontSize: 11,
                              padding: '2px 8px', borderRadius: 12,
                              background: 'rgba(250,204,21,0.10)',
                              border: '1px solid rgba(250,204,21,0.40)',
                              color: 'var(--warn, #facc15)',
                              whiteSpace: 'nowrap',
                            }}>
                            ⬇ {p.recentDeploy.version} · {fmtAge(p.recentDeploy.ageSeconds)} before
                          </span>
                        )}
                        {isAnomaly && (
                          <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 2 }}>
                            {p.description}
                          </div>
                        )}
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
                        <button className="sec"
                          style={{ fontSize: 11, padding: '2px 8px',
                                   color: 'var(--accent2)',
                                   border: '1px solid var(--accent2)',
                                   borderRadius: 4 }}
                          onClick={() => setDrawerProblemId(p.id)}>
                          Triage ▶
                        </button>
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
        return <TriageDrawer problem={p} onClose={() => setDrawerProblemId(null)} />;
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
                <span style={{ color: 'var(--text3)' }}> / {problem.threshold.toFixed(2)}</span>
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
              background: 'rgba(250,204,21,0.10)',
              border: '1px solid rgba(250,204,21,0.40)',
              fontSize: 12, color: 'var(--text)',
            }}>
              <div style={{ fontWeight: 600, marginBottom: 2 }}>⬇ Recent deploy correlation</div>
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
                  background: 'rgba(56,139,253,0.10)',
                  border: '1px solid rgba(56,139,253,0.35)',
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

          <div style={{ marginTop: 4 }}>
            <div style={{
              fontSize: 11, color: 'var(--text3)',
              textTransform: 'uppercase', letterSpacing: 0.4,
              marginBottom: 6,
            }}>What changed around the fire</div>
            <CorrelationsPanel atUnixNs={problem.startedAt} service={problem.service} />
          </div>
        </div>
      </div>
    </>
  );
}

// CorrelationsPanel renders the "what changed around this time"
// causal-correlation list. Pulls /api/correlations once on first
// expand (no live polling — the data is for a fixed point in time
// and reads expensively). Renders a compact table: top-N services
// sorted by composite anomaly score with their per-signal deltas
// + pre-formatted "reasons" bullets from the server.
function CorrelationsPanel({ atUnixNs, service }: { atUnixNs: number; service: string }) {
  type CS = import('@/lib/types').ChangedService;
  const [data, setData] = useState<CS[] | null | undefined>(undefined);
  useEffect(() => {
    setData(undefined);
    api.correlations(atUnixNs)
      .then(r => setData(r ?? []))
      .catch(() => setData(null));
  }, [atUnixNs]);

  if (data === undefined) return <Spinner />;
  if (data === null) {
    return <div style={{ fontSize: 12, color: 'var(--err)' }}>
      Correlation query failed. Check the server log.
    </div>;
  }
  if (data.length === 0) {
    return <div style={{ fontSize: 12, color: 'var(--text3)' }}>
      No services in the surrounding window had notable RED-metric changes.
      The fire may be local to <b>{service}</b> with no upstream / downstream propagation.
    </div>;
  }
  return (
    <div>
      <div style={{ fontSize: 12, fontWeight: 600, marginBottom: 8 }}>
        Top {data.length} services that changed around this time
        <span style={{ color: 'var(--text3)', fontWeight: 400, marginLeft: 8 }}>
          (current 10 min vs. prior 40 min, ranked by composite score)
        </span>
      </div>
      <div className="table-wrap">
        <table>
          <thead><tr>
            <th style={{ width: 40 }}>#</th>
            <th>Service</th>
            <th>What changed</th>
            <th className="num" style={{ width: 70 }}>Score</th>
          </tr></thead>
          <tbody>
            {data.map((c, i) => (
              <tr key={c.service}
                  style={{ background: c.service === service ? 'rgba(220,38,38,0.06)' : undefined }}>
                <td className="mono" style={{ color: 'var(--text3)' }}>{i + 1}</td>
                <td>
                  <Link to={`/service?name=${encodeURIComponent(c.service)}`}
                        style={{ fontWeight: 600 }}>{c.service}</Link>
                  {c.service === service && (
                    <span style={{
                      marginLeft: 6, fontSize: 10, padding: '1px 5px',
                      borderRadius: 3, background: 'rgba(220,38,38,0.15)',
                      color: 'var(--err)', fontWeight: 600,
                    }}>SOURCE</span>
                  )}
                </td>
                <td style={{ fontSize: 12, lineHeight: 1.55 }}>
                  {c.reasons.map((r, k) => <div key={k}>{r}</div>)}
                </td>
                <td className="num mono" style={{
                  fontWeight: 600,
                  color: c.score > 50 ? 'var(--err)' : c.score > 20 ? 'var(--warn)' : 'var(--text2)',
                }}>{c.score.toFixed(0)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
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
  const palette = p === 'P1'
    ? { bg: 'rgba(239,68,68,0.15)', border: 'rgba(239,68,68,0.55)', color: 'var(--err)' }
    : p === 'P2'
      ? { bg: 'rgba(250,204,21,0.12)', border: 'rgba(250,204,21,0.45)', color: 'var(--warn, #facc15)' }
      : { bg: 'rgba(148,163,184,0.10)', border: 'rgba(148,163,184,0.30)', color: 'var(--text3)' };
  return (
    <span
      title={reason ? `${p} — ${reason}` : p}
      style={{
        padding: '2px 8px', borderRadius: 12,
        fontSize: 11, fontWeight: 700,
        background: palette.bg, border: `1px solid ${palette.border}`,
        color: palette.color, whiteSpace: 'nowrap',
      }}>
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
          <button className="sec" style={{ padding: '2px 8px', fontSize: 11, marginTop: 8 }}
            onClick={() => setShowTrace(t => !t)}>
            {showTrace ? '▾ Hide stacktrace' : '▸ Show stacktrace'}
          </button>
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
  const cls = { padding: '3px 7px', fontSize: 11 };
  switch (g.state) {
    case 'new':
    case 'regressed':
      return (
        <>
          <button className="sec" style={{ ...cls, marginRight: 4 }}
            onClick={() => onSet(g, 'acknowledged')}>Ack</button>
          <button className="sec" style={{ ...cls, marginRight: 4 }}
            onClick={() => onSet(g, 'resolved')}>Resolve</button>
          <button className="sec" style={cls}
            onClick={() => onSet(g, 'ignored')}>Ignore</button>
        </>
      );
    case 'acknowledged':
      return (
        <>
          <button className="sec" style={{ ...cls, marginRight: 4 }}
            onClick={() => onSet(g, 'resolved')}>Resolve</button>
          <button className="sec" style={{ ...cls, marginRight: 4 }}
            onClick={() => onSet(g, 'new')}>Reopen</button>
          <button className="sec" style={cls}
            onClick={() => onSet(g, 'ignored')}>Ignore</button>
        </>
      );
    case 'resolved':
      return (
        <button className="sec" style={cls} onClick={() => onSet(g, 'new')}>Reopen</button>
      );
    case 'ignored':
      return (
        <button className="sec" style={cls} onClick={() => onSet(g, 'new')}>Unignore</button>
      );
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
  return <span className={`badge ${cls}`}>{s.toUpperCase()}</span>;
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
            style={{
              padding: '2px 8px', borderRadius: 12,
              background: isSelf ? 'rgba(34,197,94,0.12)'
                       : isTeam ? 'rgba(56,139,253,0.10)'
                       : 'rgba(168,85,247,0.10)',
              border: '1px solid ' + (
                isSelf ? 'rgba(34,197,94,0.45)'
              : isTeam ? 'rgba(56,139,253,0.35)'
              : 'rgba(168,85,247,0.35)'),
              color: isSelf ? 'var(--ok)' : 'var(--accent2)',
              cursor: 'pointer', whiteSpace: 'nowrap',
            }}>
            {isTeam ? '👥 ' : ''}{assignee}
          </span>
        )
        : <span style={{ color: 'var(--text3)' }}>—</span>}
      {currentUserEmail !== '' && !isSelf && (
        <button className="sec" disabled={busy}
          onClick={() => void set(currentUserEmail)}
          style={{ fontSize: 10, padding: '2px 6px' }}
          title="Claim this problem for yourself">
          Take it
        </button>
      )}
    </span>
  );
}

// SortTh is the generic accessible sort-header cell. Replaces the
// pre-v0.4.79 SortTh + PSortTh copy-paste pair (they only differed
// in the SortKey type parameter, which Go-style generics let us
// unify cleanly). Click + Enter + Space all toggle — operators
// with screen readers and keyboard-only users can now sort the
// table the same way as mouse users, which was the audit blocker.
function SortTh<K extends string>({ col, label, sort, dir, onSort, align }: {
  col: K; label: string;
  sort: K; dir: SortDir;
  onSort: (c: K) => void;
  align?: 'left' | 'right';
}) {
  const active = sort === col;
  return (
    <th className={`sortable${active ? ' sorted' : ''}`}
        style={{ textAlign: align ?? 'left' }}
        aria-sort={active ? (dir === 'desc' ? 'descending' : 'ascending') : 'none'}>
      <button type="button"
        onClick={() => onSort(col)}
        style={{
          all: 'unset', display: 'inline-flex', alignItems: 'baseline',
          gap: 4, width: '100%', cursor: 'pointer',
          justifyContent: align === 'right' ? 'flex-end' : 'flex-start',
        }}
        aria-label={`Sort by ${label}${active ? ` (currently ${dir === 'desc' ? 'descending' : 'ascending'})` : ''}`}>
        {label}
        <span className="sort-arrow">{active ? (dir === 'desc' ? '▼' : '▲') : '↕'}</span>
      </button>
    </th>
  );
}

// PSortTh kept as a type-narrowed alias so the existing render
// sites don't need to be touched — TS picks the right K.
// Eliminating it entirely would mean retyping every call site
// with explicit <PSortKey>; not worth the churn.
const PSortTh = SortTh<PSortKey>;

// fmtAge — compact "Nm" / "Nh" / "Ns" formatter for the deploy
// correlation tag. ageSeconds is always positive (deploy was
// before problem) but be defensive.
function fmtAge(sec: number): string {
  const s = Math.max(0, Math.round(sec));
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.round(s / 60)}m`;
  return `${Math.round(s / 3600)}h`;
}

