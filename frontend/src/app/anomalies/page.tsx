'use client';
import { Fragment, useEffect, useState, useMemo } from 'react';
import Link from 'next/link';
import { Topbar } from '@/components/Topbar';
import { SavedViewsBar } from '@/components/SavedViewsBar';
import { Spinner, Empty } from '@/components/Spinner';
import { ServicePicker } from '@/components/ServicePicker';
import { useAuth } from '@/components/AuthProvider';
import { api, type UserRow } from '@/lib/api';
import { fmtNum, tsLong } from '@/lib/utils';
import type {
  ExceptionGroup, ExceptionGroupState, ExceptionSample,
  LogPatternAnomaly, TraceOpAnomaly, Problem, AnomalyEvent,
  AnomalySilence,
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

export default function AnomaliesPage() {
  const { user } = useAuth();
  const isAdmin = user?.role === 'admin' || user?.role === 'editor';
  const [tab, setTab] = useState<string>('open');
  const [service, setService] = useState('');
  const [search, setSearch] = useState('');
  const [services, setServices] = useState<string[]>([]);
  const [users, setUsers] = useState<UserRow[]>([]);
  const [data, setData] = useState<ExceptionGroup[] | null | undefined>(undefined);
  // Expanded fingerprint(s) — multiple groups can be open at once for compare-and-contrast.
  const [expanded, setExpanded] = useState<Set<string>>(new Set());

  // Anomaly signals — independently fetched + 30-60s cached on
  // the server. All refresh alongside the exception inbox.
  const [logPatterns, setLogPatterns] = useState<LogPatternAnomaly[] | undefined>(undefined);
  const [traceOps,    setTraceOps]    = useState<TraceOpAnomaly[]    | undefined>(undefined);
  const [metrics,     setMetrics]     = useState<Problem[]           | undefined>(undefined);
  // Anomaly history (active + cleared from last 24h) — persisted
  // by the background recorder so the operator can answer "did
  // this fire today, even if it has subsided".
  const [history, setHistory]         = useState<AnomalyEvent[]      | undefined>(undefined);
  const [silences, setSilences]       = useState<AnomalySilence[]    | undefined>(undefined);

  const refreshSilences = () => {
    api.anomalySilences().then(s => setSilences(s ?? [])).catch(() => setSilences([]));
  };
  const onMute = async (kind: string, pattern: string, service: string, durationSec: number) => {
    // The fingerprint is computed server-side from (kind, pattern,
    // service); we send the raw triplet plus a placeholder
    // fingerprint that the server overrides if mismatched. Easier
    // than duplicating the sha1 logic on the client.
    await api.createAnomalySilence({
      fingerprint: `${kind}|${pattern}|${service}`,
      kind, pattern, service, durationSec,
    });
    // Refresh: the live sections drop the muted entry on the
    // next fetch; silences list refreshes immediately.
    refreshSilences();
    refresh();
  };
  const onUnmute = async (id: string) => {
    await api.deleteAnomalySilence(id);
    refreshSilences();
    refresh();
  };

  const refresh = () => {
    setData(undefined);
    setLogPatterns(undefined);
    setTraceOps(undefined);
    setMetrics(undefined);
    api.logPatternAnomalies().then(p => setLogPatterns(p ?? [])).catch(() => setLogPatterns([]));
    api.traceOpAnomalies().then(t => setTraceOps(t ?? [])).catch(() => setTraceOps([]));
    api.metricAnomalies().then(m => setMetrics(m ?? [])).catch(() => setMetrics([]));
    api.anomalyEvents().then(h => setHistory(h ?? [])).catch(() => setHistory([]));
    refreshSilences();
    api.exceptionGroups({ state: tab, service: service || undefined, limit: 200 })
      .then(d => setData(d ?? [])).catch(() => setData(null));
  };
  useEffect(refresh, [tab, service]);

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
    try { await api.setExceptionGroupState(g.fingerprint, next); refresh(); }
    catch (err) { alert(humanize(err)); }
  };
  const setAssignee = async (g: ExceptionGroup, userId: string) => {
    try { await api.assignExceptionGroup(g.fingerprint, userId); refresh(); }
    catch (err) { alert(humanize(err)); }
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
      <Topbar title="Anomalies" />
      <div id="content">
        <SavedViewsBar page="anomalies" />
        {/* Three top-of-page anomaly streams stacked in
            descending freshness order: log patterns (raw text,
            most reactive), trace ops (per-endpoint failure
            spike), then metric (service-wide z-score). The
            traditional exception inbox sits below. */}
        <SilencesSection items={silences} onUnmute={onUnmute} />
        <LogPatternsSection items={logPatterns} onMute={onMute} />
        <TraceOpsSection    items={traceOps}    onMute={onMute} />
        <MetricSection      items={metrics} />

        {/* Persistent history — every detection the recorder
            saw in the last 24h, with status. Lets the operator
            answer "did this fire earlier today, even though
            it has stopped now". */}
        <HistorySection items={history} />

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
                          <Link href={`/service?name=${encodeURIComponent(g.service)}`}
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
      </div>
    </>
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
        <Link href={`/trace?id=${sample.traceId}`} style={{ fontFamily: 'monospace' }}>
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

function SortTh({ col, label, sort, dir, onSort, align }: {
  col: SortKey; label: string;
  sort: SortKey; dir: SortDir;
  onSort: (c: SortKey) => void;
  align?: 'left' | 'right';
}) {
  const active = sort === col;
  return (
    <th className={`sortable${active ? ' sorted' : ''}`}
        onClick={() => onSort(col)}
        style={{ textAlign: align ?? 'left' }}>
      {label}
      <span className="sort-arrow">{active ? (dir === 'desc' ? '▼' : '▲') : '↕'}</span>
    </th>
  );
}

// AnomalyShell standardises the look across the three top
// sections. Empty when items==[] (nothing to show); empty when
// items===undefined (still loading) collapses to nothing too —
// the exception inbox below is the page's anchor and we don't
// want pre-load reflow.
function AnomalyShell({ title, hint, count, children }: {
  title: string; hint: string; count: number; children: React.ReactNode;
}) {
  if (count === 0) return null;
  return (
    <div style={{
      background: 'var(--bg1)', border: '1px solid var(--border)',
      borderRadius: 8, padding: 14, marginBottom: 16,
    }}>
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 10, marginBottom: 10 }}>
        <span style={{ fontSize: 13, fontWeight: 700 }}>{title}</span>
        <span style={{ fontSize: 11, color: 'var(--text3)' }}>{hint}</span>
      </div>
      {children}
    </div>
  );
}

// TraceOpsSection lists per-(service, operation) error spikes —
// the SRE's "which endpoint just got bad" view. Brand-new
// failures are flagged separately from amplified existing ones.
function TraceOpsSection({ items, onMute }: {
  items: TraceOpAnomaly[] | undefined;
  onMute: (kind: string, pattern: string, service: string, durationSec: number) => void;
}) {
  if (items === undefined) return null;
  return (
    <AnomalyShell
      title="Trace operation anomalies"
      hint={`${items.length} operation${items.length === 1 ? '' : 's'} with new or doubled error rate`}
      count={items.length}>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(380px, 1fr))', gap: 10 }}>
        {items.map((a, i) => (
          <div key={i} style={{
            padding: 10, border: '1px solid var(--border)',
            borderRadius: 6, background: 'var(--bg2)',
            display: 'flex', flexDirection: 'column', gap: 4,
          }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
              {a.kind === 'new_error'
                ? <span className="badge b-warn" style={{ fontSize: 10 }}>NEW ERROR</span>
                : <span className="badge b-err"  style={{ fontSize: 10 }}>SPIKE ×{a.ratio.toFixed(1)}</span>}
              <span style={{ fontWeight: 600, fontSize: 12 }}>{a.operation || '(unnamed)'}</span>
              <span style={{ flex: 1 }} />
              {a.sampleTraceId && (
                <Link href={`/trace?id=${a.sampleTraceId}`} style={{ fontSize: 11, color: 'var(--accent2)' }}>
                  trace ↗
                </Link>
              )}
              <SnoozeButton onMute={d => onMute('trace_op', a.operation, a.service, d)} />
            </div>
            <div style={{ fontSize: 11, color: 'var(--text2)' }}>
              <Link href={`/service?name=${encodeURIComponent(a.service)}`}
                    style={{ fontFamily: 'monospace', color: 'var(--text)', textDecoration: 'none' }}>
                {a.service}
              </Link>
              {' · '}{fmtNum(a.currentErrors)} errors now
              {a.baselineErrors > 0 && <> · {fmtNum(a.baselineErrors)} prev</>}
            </div>
          </div>
        ))}
      </div>
    </AnomalyShell>
  );
}

// SnoozeButton: small dropdown that POSTs an anomaly_silence
// for the given fingerprint. Emits an `onMute(durationSec)`
// callback so the caller can refresh the list and confirm the
// row dropped from the live view.
function SnoozeButton({ onMute }: { onMute: (durationSec: number) => void }) {
  const [open, setOpen] = useState(false);
  const opts: { label: string; sec: number }[] = [
    { label: '1 hour',  sec: 3600 },
    { label: '8 hours', sec: 8 * 3600 },
    { label: '24 hours', sec: 24 * 3600 },
    { label: '7 days', sec: 7 * 24 * 3600 },
  ];
  return (
    <span style={{ position: 'relative' }}>
      <button type="button"
        onClick={() => setOpen(o => !o)}
        title="Mute this anomaly"
        style={{
          fontSize: 10, padding: '2px 8px', borderRadius: 3,
          background: 'var(--bg3)', border: '1px solid var(--border)',
          color: 'var(--text2)', cursor: 'pointer',
        }}>
Mute
      </button>
      {open && (
        <div style={{
          position: 'absolute', top: '100%', right: 0,
          marginTop: 4, padding: 4, borderRadius: 4, zIndex: 10,
          background: 'var(--bg1)', border: '1px solid var(--border)',
          boxShadow: '0 6px 18px rgba(0,0,0,0.25)',
          display: 'flex', flexDirection: 'column', gap: 2,
        }} onClick={e => e.stopPropagation()}>
          {opts.map(o => (
            <button key={o.sec} type="button"
              onClick={() => { setOpen(false); onMute(o.sec); }}
              style={{
                fontSize: 11, padding: '4px 10px', textAlign: 'left',
                background: 'transparent', border: 'none',
                color: 'var(--text)', cursor: 'pointer', whiteSpace: 'nowrap',
              }}>
              {o.label}
            </button>
          ))}
        </div>
      )}
    </span>
  );
}

// MetricSection shows the open Problems opened by the
// background z-score detector against service-level metrics.
// Smaller than the other two — there's typically just a handful
// at a time when something's actually wrong.
function MetricSection({ items }: { items: Problem[] | undefined }) {
  if (items === undefined) return null;
  return (
    <AnomalyShell
      title="Metric anomalies"
      hint={`${items.length} service-level z-score deviation${items.length === 1 ? '' : 's'} open`}
      count={items.length}>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(360px, 1fr))', gap: 10 }}>
        {items.map(p => (
          <div key={p.id} style={{
            padding: 10, border: '1px solid var(--border)',
            borderRadius: 6, background: 'var(--bg2)',
          }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 4 }}>
              <span className={`badge ${p.severity === 'critical' ? 'b-err' : 'b-warn'}`} style={{ fontSize: 10 }}>
                {p.severity.toUpperCase()}
              </span>
              <span style={{ fontWeight: 600, fontSize: 12 }}>{p.metric}</span>
              <span style={{ flex: 1 }} />
              <Link href={`/service?name=${encodeURIComponent(p.service)}`} style={{ fontSize: 11, color: 'var(--accent2)' }}>
                {p.service} ↗
              </Link>
            </div>
            <div style={{ fontSize: 11, color: 'var(--text2)' }}>
              {p.description || `value ${p.value.toFixed(2)} vs threshold ${p.threshold.toFixed(2)}`}
            </div>
          </div>
        ))}
      </div>
    </AnomalyShell>
  );
}

// SilencesSection lists currently-muted anomalies with an unmute
// affordance. Compact strip across the top — present only when
// at least one silence is active, otherwise hidden so the page
// doesn't carry permanent dead space.
function SilencesSection({ items, onUnmute }: {
  items: AnomalySilence[] | undefined;
  onUnmute: (id: string) => void;
}) {
  if (!items || items.length === 0) return null;
  return (
    <div style={{
      background: 'var(--bg2)', border: '1px solid var(--border)',
      borderRadius: 6, padding: '8px 12px', marginBottom: 12,
      display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap',
    }}>
      <span style={{ fontSize: 11, color: 'var(--text2)', fontWeight: 600 }}>
Muted ({items.length})
      </span>
      {items.map(s => {
        const remaining = Math.max(0, s.untilAt / 1e6 - Date.now());
        const remainStr = remaining > 24 * 3600 * 1000
          ? `${Math.floor(remaining / (24 * 3600 * 1000))}d`
          : remaining > 3600 * 1000
            ? `${Math.floor(remaining / (3600 * 1000))}h`
            : `${Math.floor(remaining / 60000)}m`;
        return (
          <span key={s.id} style={{
            display: 'inline-flex', alignItems: 'center', gap: 6,
            padding: '2px 8px', borderRadius: 3, fontSize: 11,
            background: 'var(--bg3)', border: '1px solid var(--border)',
            fontFamily: 'monospace',
          }}>
            <span title={`${s.kind} · ${s.pattern}`}>
              {s.pattern}
              {s.service && <span style={{ color: 'var(--text3)' }}> @ {s.service}</span>}
            </span>
            <span style={{ color: 'var(--text3)' }}>{remainStr} left</span>
            <button type="button" onClick={() => onUnmute(s.id)}
              title="Unmute now"
              style={{
                background: 'transparent', border: 'none', color: 'var(--text3)',
                cursor: 'pointer', padding: 0, fontSize: 11, lineHeight: 1,
              }}>×</button>
          </span>
        );
      })}
    </div>
  );
}

// HistorySection renders the persistent log + trace-op anomaly
// timeline from the last 24 hours. Each row shows whether the
// event is still active or has cleared, when it started, when
// it was last seen, and its peak ratio. The operator gets a
// chronological view of "what's been wrong today" without
// having to monitor the live sections continuously.
function HistorySection({ items }: { items: AnomalyEvent[] | undefined }) {
  if (items === undefined || items.length === 0) return null;
  const active  = items.filter(e => e.status === 'active');
  const cleared = items.filter(e => e.status === 'cleared');
  return (
    <AnomalyShell
      title="Anomaly history (last 24h)"
      hint={`${active.length} active · ${cleared.length} cleared`}
      count={items.length}>
      <div className="table-wrap">
        <table>
          <thead><tr>
            <th style={{ width: 70 }}>Status</th>
            <th>Pattern</th>
            <th>Service</th>
            <th>Kind</th>
            <th className="num">Peak ×</th>
            <th>Started</th>
            <th>Last seen</th>
          </tr></thead>
          <tbody>
            {items.map(e => (
              <tr key={e.id}>
                <td>
                  <span className={`badge ${e.status === 'active' ? 'b-err' : 'b-ok'}`} style={{ fontSize: 10 }}>
                    {e.status === 'active' ? 'ACTIVE' : 'CLEARED'}
                  </span>
                </td>
                <td style={{ fontWeight: 600 }}>{e.pattern}</td>
                <td>
                  <Link href={`/service?name=${encodeURIComponent(e.service)}`}
                        style={{ fontFamily: 'monospace', fontSize: 11 }}>
                    {e.service || '—'}
                  </Link>
                </td>
                <td style={{ fontSize: 11, color: 'var(--text2)' }}>
                  {e.kind === 'log_pattern' ? 'log' : 'trace op'}
                </td>
                <td className="num mono">{e.peakRatio.toFixed(1)}</td>
                <td className="mono" style={{ fontSize: 11, color: 'var(--text3)' }}>{tsLong(e.startedAt)}</td>
                <td className="mono" style={{ fontSize: 11 }}>{tsLong(e.lastSeen)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </AnomalyShell>
  );
}

// LogPatternsSection renders the curated production-signal
// anomalies (Oracle ORA-, OOM, NPE, deadlock, panic, …) at the
// top of the page. Empty when there's nothing surprising — we
// suppress the section entirely rather than render a "no
// anomalies" placeholder so the eye drops straight to the
// exception inbox below.
function LogPatternsSection({ items, onMute }: {
  items: LogPatternAnomaly[] | undefined;
  onMute: (kind: string, pattern: string, service: string, durationSec: number) => void;
}) {
  if (items === undefined) return null;
  if (items.length === 0) return null;
  return (
    <div style={{
      background: 'var(--bg1)', border: '1px solid var(--border)',
      borderRadius: 8, padding: 14, marginBottom: 16,
    }}>
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 10, marginBottom: 10 }}>
        <span style={{ fontSize: 13, fontWeight: 700 }}>
          Log-pattern anomalies
        </span>
        <span style={{ fontSize: 11, color: 'var(--text3)' }}>
          {items.length} pattern{items.length === 1 ? '' : 's'} changed in the last 5 min
        </span>
      </div>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(380px, 1fr))', gap: 10 }}>
        {items.map((a, i) => (
          <div key={i} style={{
            padding: 10, border: '1px solid var(--border)',
            borderRadius: 6, background: 'var(--bg2)',
            display: 'flex', flexDirection: 'column', gap: 4,
          }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
              {a.kind === 'new'
                ? <span className="badge b-warn" style={{ fontSize: 10 }}>NEW</span>
                : <span className="badge b-err"  style={{ fontSize: 10 }}>SPIKE ×{a.ratio.toFixed(1)}</span>}
              <span style={{ fontWeight: 600, fontSize: 12 }}>{a.pattern}</span>
              <span style={{ flex: 1 }} />
              {/* Drill-down to logs scoped to this service. We
                  intentionally do NOT pass the pattern regex as
                  ?q= because the /logs search uses substring
                  match (multiSearchAnyCaseInsensitive on the
                  tokenbf_v1 index), and a regex like
                  "ORA-[0-9]+" never substring-matches a real log
                  body. Operator gets the right service + can
                  type a token themselves if they want to narrow
                  further. */}
              <Link href={`/logs?service=${encodeURIComponent(a.service)}`}
                    style={{ fontSize: 11, color: 'var(--accent2)' }}>
                logs ↗
              </Link>
              <SnoozeButton onMute={d => onMute('log_pattern', a.pattern, a.service, d)} />
            </div>
            <div style={{ fontSize: 11, color: 'var(--text2)' }}>
              <span style={{ fontFamily: 'monospace' }}>{a.service || 'unknown'}</span>
              {' · '}
              {fmtNum(a.currentCount)} now
              {a.baselineCount > 0 && <> · {fmtNum(a.baselineCount)} prev</>}
            </div>
            {a.sample && (
              <div style={{
                fontSize: 11, color: 'var(--text3)',
                fontFamily: 'monospace',
                whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis',
              }} title={a.sample}>
                {a.sample}
              </div>
            )}
          </div>
        ))}
      </div>
    </div>
  );
}
