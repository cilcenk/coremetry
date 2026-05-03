'use client';
import { Fragment, useEffect, useState, useMemo } from 'react';
import Link from 'next/link';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { Combobox } from '@/components/Combobox';
import { useAuth } from '@/components/AuthProvider';
import { api, type UserRow } from '@/lib/api';
import { fmtNum, tsLong } from '@/lib/utils';
import type { ExceptionGroup, ExceptionGroupState, ExceptionSample } from '@/lib/types';

// State buckets shown as tabs along the top of the page.
const TABS: { key: string; label: string; hint: string }[] = [
  { key: 'open',         label: 'Inbox',        hint: 'New + acknowledged + regressed' },
  { key: 'new',          label: 'New',          hint: 'Untouched since first occurrence' },
  { key: 'acknowledged', label: 'Acknowledged', hint: 'Someone is on it' },
  { key: 'regressed',    label: 'Regressed',    hint: 'Resolved but happening again' },
  { key: 'resolved',     label: 'Resolved',     hint: 'Closed out' },
  { key: 'ignored',      label: 'Ignored',      hint: 'Permanently silenced' },
];

export default function ExceptionsPage() {
  const { user } = useAuth();
  const isAdmin = user?.role === 'admin';
  const [tab, setTab] = useState<string>('open');
  const [service, setService] = useState('');
  const [search, setSearch] = useState('');
  const [services, setServices] = useState<string[]>([]);
  const [users, setUsers] = useState<UserRow[]>([]);
  const [data, setData] = useState<ExceptionGroup[] | null | undefined>(undefined);
  // Expanded fingerprint(s) — multiple groups can be open at once for compare-and-contrast.
  const [expanded, setExpanded] = useState<Set<string>>(new Set());

  const refresh = () => {
    setData(undefined);
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

  const filtered = useMemo(() => {
    const term = search.trim().toLowerCase();
    if (!term) return data ?? [];
    return (data ?? []).filter(g =>
      g.type.toLowerCase().includes(term) ||
      g.message.toLowerCase().includes(term) ||
      g.service.toLowerCase().includes(term)
    );
  }, [data, search]);

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
      <Topbar title="Exceptions" />
      <div id="content">
        <div style={{ display: 'flex', gap: 4, borderBottom: '1px solid var(--border)', marginBottom: 12 }}>
          {TABS.map(t => (
            <button key={t.key} onClick={() => setTab(t.key)} title={t.hint}
              style={{
                padding: '8px 14px', background: 'transparent',
                border: 'none', borderBottom: tab === t.key ? '2px solid var(--accent)' : '2px solid transparent',
                color: tab === t.key ? 'var(--text)' : 'var(--text2)',
                fontSize: 13, fontWeight: tab === t.key ? 600 : 500, cursor: 'pointer',
              }}>
              {t.label}
            </button>
          ))}
        </div>

        <div className="controls">
          <Combobox value={service} onChange={setService} options={services}
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
                  <th style={{ width: 110 }}>State</th>
                  <th>Exception</th>
                  <th>Service</th>
                  <th style={{ textAlign: 'right' }}>Occurrences</th>
                  <th>First seen</th>
                  <th>Last seen</th>
                  <th>Assignee</th>
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
                          <div style={{ fontWeight: 600, color: 'var(--err)' }}>{g.type}</div>
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

  return (
    <div>
      <div style={{ display: 'flex', alignItems: 'center', marginBottom: 8 }}>
        <span style={{ fontSize: 12, color: 'var(--text2)', fontWeight: 600 }}>
          Recent occurrences
        </span>
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
      {sample.statusMsg && (
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
