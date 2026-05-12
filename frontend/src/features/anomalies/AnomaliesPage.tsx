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
type PSortKey = 'severity' | 'service' | 'metric' | 'value' | 'rule' | 'started' | 'status';
const SEV_RANK: Record<string, number> = { critical: 3, warning: 2, info: 1 };
const P_NATURAL_DIR: Record<PSortKey, SortDir> = {
  severity: 'desc', service: 'asc', metric: 'asc',
  value: 'desc', rule: 'asc', started: 'desc', status: 'asc',
};

export default function ProblemsPage() {
  const { user } = useAuth();
  const isAdmin = user?.role === 'admin' || user?.role === 'editor';
  // ?service= URL param pre-populates the service filter — driven
  // by the "View N problems →" / "Errors" pills on the service
  // detail page so the operator lands on the exact scope they
  // were looking at without retyping it.
  const [searchParams] = useSearchParams();
  const initialService = searchParams.get('service') ?? '';
  const [tab, setTab] = useState<string>('open');
  const [service, setService] = useState(initialService);
  const [search, setSearch] = useState('');
  const [, setServices] = useState<string[]>([]);
  const [users, setUsers] = useState<UserRow[]>([]);
  const [data, setData] = useState<ExceptionGroup[] | null | undefined>(undefined);
  // Expanded fingerprint(s) — multiple groups can be open at once for compare-and-contrast.
  const [expanded, setExpanded] = useState<Set<string>>(new Set());

  // Exception groups inbox — separate query because it depends
  // on tab + service filter; couldn't be folded into the shared
  // anomaly hooks above.
  const qc = useQueryClient();
  const refreshExceptionGroups = () => {
    setData(undefined);
    api.exceptionGroups({ state: tab, service: service || undefined, limit: 200 })
      .then(d => setData(d ?? [])).catch(() => setData(null));
  };
  useEffect(refreshExceptionGroups, [tab, service]);

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
            {filtered.length} groups · {fmtNum(filtered.reduce((n, g) => n + Number(g.occurrences), 0))} occurrences
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
  const [statusFilter, setStatusFilter] = useState<'open' | 'all' | 'resolved'>('open');
  const [sortBy, setSortBy] = useState<PSortKey>('started');
  const [sortDir, setSortDir] = useState<SortDir>('desc');
  // Which row's "Why?" panel is expanded. Null = none. Single
  // expansion at a time keeps the table compact during incident
  // triage — the operator typically only investigates one
  // problem at a time, switching rows replaces the panel.
  const [explainOpen, setExplainOpen] = useState<string | null>(null);

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
    const cmp = (a: Problem, b: Problem): number => {
      switch (sortBy) {
        case 'severity': return (SEV_RANK[a.severity] ?? 0) - (SEV_RANK[b.severity] ?? 0);
        case 'service':  return a.service.localeCompare(b.service);
        case 'metric':   return a.metric.localeCompare(b.metric);
        case 'value':    return a.value - b.value;
        case 'rule':     return a.ruleName.localeCompare(b.ruleName);
        case 'started':  return a.startedAt - b.startedAt;
        case 'status':   return a.status.localeCompare(b.status);
      }
    };
    const arr = [...data].sort(cmp);
    return sortDir === 'desc' ? arr.reverse() : arr;
  }, [data, sortBy, sortDir]);

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
      {sorted && sorted.length > 0 && (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <PSortTh col="severity" label="Severity" sort={sortBy} dir={sortDir} onSort={toggleSort} />
                <PSortTh col="service"  label="Service"  sort={sortBy} dir={sortDir} onSort={toggleSort} />
                <PSortTh col="metric"   label="Metric"   sort={sortBy} dir={sortDir} onSort={toggleSort} />
                <PSortTh col="value"    label="Value"    sort={sortBy} dir={sortDir} onSort={toggleSort} align="right" />
                <PSortTh col="rule"     label="Rule"     sort={sortBy} dir={sortDir} onSort={toggleSort} />
                <PSortTh col="started"  label="Started"  sort={sortBy} dir={sortDir} onSort={toggleSort} />
                <PSortTh col="status"   label="Status"   sort={sortBy} dir={sortDir} onSort={toggleSort} />
                <th>Why</th>
                <th>AI</th>
              </tr>
            </thead>
            <tbody>
              {sorted.map(p => {
                const isAnomaly = p.ruleId?.startsWith('anomaly:');
                const isOpen = explainOpen === p.id;
                return (
                  <Fragment key={p.id}>
                    <tr onClick={() => navigate(`/service?name=${encodeURIComponent(p.service)}`)}
                        style={{ cursor: 'pointer' }}>
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
                        {isAnomaly && (
                          <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 2 }}>
                            {p.description}
                          </div>
                        )}
                      </td>
                      <td className="mono">{tsLong(p.startedAt)}</td>
                      <td>
                        {p.status === 'open'
                          ? <span className="badge b-err">OPEN</span>
                          : <span className="badge b-ok">RESOLVED</span>}
                      </td>
                      <td onClick={e => e.stopPropagation()}>
                        {/* "Why?" — toggles the causal-correlation
                            panel below the row. Clicking again
                            (or another row's button) closes /
                            switches. Same data the SRE used to
                            piece together by hand: services
                            around the time of fire whose RED
                            metrics swung the most. */}
                        <button className="sec"
                          style={{ fontSize: 11, padding: '2px 8px' }}
                          onClick={() => setExplainOpen(isOpen ? null : p.id)}>
                          {isOpen ? '× hide' : '? why'}
                        </button>
                      </td>
                      <td onClick={e => e.stopPropagation()}>
                        <CopilotExplain kind="problem" id={p.id} label={<IconSparkles />} />
                      </td>
                    </tr>
                    {isOpen && (
                      <tr>
                        <td colSpan={9} style={{
                          background: 'var(--bg1)', padding: '10px 16px',
                          borderTop: '1px solid var(--border)',
                        }}>
                          <CorrelationsPanel atUnixNs={p.startedAt} service={p.service} />
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
    </div>
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

