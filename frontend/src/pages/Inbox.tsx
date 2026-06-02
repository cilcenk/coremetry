import { useEffect, useMemo, useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { TableSkeleton } from '@/components/Skeleton';
import { api } from '@/lib/api';
import { tsLong } from '@/lib/utils';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import type { InboxItem, InboxKind } from '@/lib/types';

// /inbox — unified triage view (v0.5.211). Merges Problems +
// Exception groups + Anomaly events server-side with a normalised
// P1/P2/P3 priority blend so operators get "everything needing a
// human" in one place instead of tab-hopping between three pages.
//
// Each row drill-downs to the source page with the item focused.
// The per-source pages still exist as deep-drill workspaces; this
// page is the daily landing surface.

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
  const [data, setData] = useState<InboxItem[] | null | undefined>(undefined);
  const [statusFilter, setStatusFilter] = useState<'open' | 'all'>('open');
  // Multi-select chips for priority + kind. Persisted so the
  // operator's view sticks across page reloads. Default: P1+P2
  // (signal-first) across all kinds.
  const [prioSet, setPrioSet] = useState<Set<string>>(() => {
    try {
      const raw = localStorage.getItem('inbox.prio');
      if (raw) {
        const arr = JSON.parse(raw);
        if (Array.isArray(arr) && arr.length > 0) return new Set(arr);
      }
    } catch { /* ignore */ }
    return new Set(['P1', 'P2']);
  });
  const [kindSet, setKindSet] = useState<Set<InboxKind>>(() => {
    try {
      const raw = localStorage.getItem('inbox.kind');
      if (raw) {
        const arr = JSON.parse(raw);
        if (Array.isArray(arr) && arr.length > 0) return new Set(arr);
      }
    } catch { /* ignore */ }
    return new Set<InboxKind>(['problem', 'exception', 'anomaly']);
  });
  const [serviceFilter, setServiceFilter] = useState('');
  const [ownerFilter, setOwnerFilter] = useState('');
  const [sreFilter, setSreFilter] = useState('');

  const togglePrio = (p: string) => {
    setPrioSet(prev => {
      const next = new Set(prev);
      if (next.has(p)) {
        if (next.size === 1) return prev;
        next.delete(p);
      } else {
        next.add(p);
      }
      try { localStorage.setItem('inbox.prio', JSON.stringify([...next])); } catch { /* ignore */ }
      return next;
    });
  };
  const toggleKind = (k: InboxKind) => {
    setKindSet(prev => {
      const next = new Set(prev);
      if (next.has(k)) {
        if (next.size === 1) return prev;
        next.delete(k);
      } else {
        next.add(k);
      }
      try { localStorage.setItem('inbox.kind', JSON.stringify([...next])); } catch { /* ignore */ }
      return next;
    });
  };

  useEffect(() => {
    setData(undefined);
    api.inbox({
      status: statusFilter,
      service: serviceFilter || undefined,
      ownerTeam: ownerFilter || undefined,
      sreTeam: sreFilter || undefined,
      limit: 300,
    })
      .then(r => setData(r ?? []))
      .catch(() => setData(null));
  }, [statusFilter, serviceFilter, ownerFilter, sreFilter]);

  const filtered = useMemo(() => {
    if (!data) return data;
    return data.filter(it =>
      prioSet.has(it.priority) &&
      kindSet.has(it.kind));
  }, [data, prioSet, kindSet]);

  // Shared sortable + resizable table. Pre-sort by lastSeen desc so the
  // primitive's stable priority-desc sort reproduces the prior fixed
  // ordering (P1 first, newest within priority). Hook is unconditional.
  const inboxRows = useMemo(
    () => (filtered ? [...filtered].sort((a, b) => b.lastSeen - a.lastSeen) : []),
    [filtered]);
  const dt = useDataTable<InboxItem>({
    storageKey: 'inbox',
    columns: INBOX_COLS,
    rows: inboxRows,
    initialSort: { id: 'priority', dir: 'desc' },
  });

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
  const { ownerOptions, sreOptions } = useMemo(() => {
    const owners = new Set<string>();
    const sres   = new Set<string>();
    for (const it of data ?? []) {
      if (it.ownerTeam) owners.add(it.ownerTeam);
      if (it.sreTeam)   sres.add(it.sreTeam);
    }
    return {
      ownerOptions: [...owners].sort(),
      sreOptions:   [...sres].sort(),
    };
  }, [data]);

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

  return (
    <>
      <Topbar title="Inbox" />
      <div id="content">
        <p style={{ color: 'var(--text2)', fontSize: 13, marginBottom: 14 }}>
          Everything needing a human — Problems (alert rules), open Exception
          groups, and active Anomaly detections. Default view: <b>P1 + P2</b>
          across all kinds. Click any row to drill into the source surface.
        </p>

        <div style={{ display: 'flex', gap: 10, alignItems: 'center', marginBottom: 12, flexWrap: 'wrap' }}>
          {/* Status pivot */}
          <div style={{ display: 'flex', gap: 4 }}>
            {(['open', 'all'] as const).map(s => (
              <button key={s} onClick={() => setStatusFilter(s)}
                className={statusFilter === s ? '' : 'sec'}
                style={{ fontSize: 11, padding: '4px 10px' }}>
                {s === 'open' ? 'Open / Active' : 'All'}
              </button>
            ))}
          </div>

          {/* Priority chips */}
          <div style={{ display: 'flex', gap: 4 }}>
            {(['P1', 'P2', 'P3'] as const).map(pp => {
              const on = prioSet.has(pp);
              const colour = pp === 'P1' ? 'var(--err)'
                          : pp === 'P2' ? 'var(--warn)'
                          : 'var(--text3)';
              return (
                <button key={pp} onClick={() => togglePrio(pp)}
                  style={{
                    all: 'unset', cursor: 'pointer',
                    fontSize: 11, padding: '2px 8px', borderRadius: 12,
                    fontFamily: 'ui-monospace, monospace',
                    border: `1px solid ${on ? colour : 'var(--border)'}`,
                    background: on ? colour : 'transparent',
                    color: on ? 'var(--bg)' : 'var(--text3)',
                    fontWeight: on ? 700 : 400,
                  }}>
                  {pp} ({counts[pp] ?? 0})
                </button>
              );
            })}
          </div>

          {/* Kind chips */}
          <div style={{ display: 'flex', gap: 4 }}>
            {(['problem', 'exception', 'anomaly'] as const).map(k => {
              const on = kindSet.has(k);
              const label = k === 'problem' ? 'Problems'
                         : k === 'exception' ? 'Exceptions'
                         : 'Anomalies';
              return (
                <button key={k} onClick={() => toggleKind(k)}
                  style={{
                    all: 'unset', cursor: 'pointer',
                    fontSize: 11, padding: '2px 8px', borderRadius: 12,
                    border: `1px solid ${on ? 'var(--accent2)' : 'var(--border)'}`,
                    background: on ? 'rgba(56,139,253,0.10)' : 'transparent',
                    color: on ? 'var(--accent2)' : 'var(--text3)',
                    fontWeight: on ? 600 : 400,
                  }}>
                  {label} ({counts[k] ?? 0})
                </button>
              );
            })}
          </div>

          <span style={{ flex: 1 }} />

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

          <input value={serviceFilter}
            onChange={e => setServiceFilter(e.target.value)}
            placeholder="Filter by service…"
            style={{ fontSize: 12, padding: '4px 8px', minWidth: 180 }} />
        </div>

        {data === undefined && <TableSkeleton cols={6} wideFirst />}
        {data === null && <Empty icon="!" title="Failed to load inbox" />}
        {filtered && filtered.length === 0 && (
          <Empty icon="✓" title="Inbox clear">
            {prioSet.size < 3 || kindSet.size < 3
              ? 'Widen the priority / kind filter to see more.'
              : 'Nothing needs your attention right now.'}
          </Empty>
        )}
        {filtered && filtered.length > 0 && (
          <div className="table-wrap">
            <table style={{ tableLayout: 'fixed', width: '100%' }}>
              <DataTableColgroup dt={dt} />
              <DataTableHead dt={dt} />
              <tbody>
                {dt.sortedRows.map(it => (
                  <tr key={it.id}
                    onClick={() => goToSource(it)}
                    style={{
                      cursor: 'pointer',
                      // content-visibility lets the browser skip
                      // off-screen rows on initial paint — fits
                      // the "1000+ services, busy inbox" path.
                      contentVisibility: 'auto',
                      containIntrinsicSize: 'auto 44px',
                    }}>
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
                              style={{
                                all: 'unset', cursor: 'pointer',
                                fontSize: 10, padding: '1px 6px', borderRadius: 10,
                                background: ownerFilter === it.ownerTeam
                                  ? 'rgba(56,139,253,0.22)' : 'rgba(56,139,253,0.08)',
                                border: '1px solid rgba(56,139,253,0.30)',
                                color: 'var(--accent2)', whiteSpace: 'nowrap',
                                fontWeight: ownerFilter === it.ownerTeam ? 600 : 400,
                              }}>
                              👥 {it.ownerTeam}
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
                              style={{
                                all: 'unset', cursor: 'pointer',
                                fontSize: 10, padding: '1px 6px', borderRadius: 10,
                                background: sreFilter === it.sreTeam
                                  ? 'rgba(168,85,247,0.22)' : 'rgba(168,85,247,0.08)',
                                border: '1px solid rgba(168,85,247,0.35)',
                                color: 'var(--accent2)', whiteSpace: 'nowrap',
                                fontWeight: sreFilter === it.sreTeam ? 600 : 400,
                              }}>
                              🛡 {it.sreTeam}
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
    </>
  );
}

// PriorityBadge — same palette as the Problems-page badge so
// operators don't have to relearn the colour code.
function PriorityBadge({ p, reason }: { p: 'P1' | 'P2' | 'P3'; reason?: string }) {
  const palette = p === 'P1'
    ? { bg: 'rgba(239,68,68,0.15)', border: 'rgba(239,68,68,0.55)', color: 'var(--err)' }
    : p === 'P2'
      ? { bg: 'rgba(250,204,21,0.12)', border: 'rgba(250,204,21,0.45)', color: 'var(--warn)' }
      : { bg: 'rgba(148,163,184,0.10)', border: 'rgba(148,163,184,0.30)', color: 'var(--text3)' };
  return (
    <span title={reason ? `${p} — ${reason}` : p}
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

function AssigneePill({ v }: { v: string }) {
  const isTeam = !v.includes('@');
  return (
    <span style={{
      fontSize: 11, padding: '2px 8px', borderRadius: 12,
      background: isTeam ? 'rgba(56,139,253,0.10)' : 'rgba(168,85,247,0.10)',
      border: `1px solid ${isTeam ? 'rgba(56,139,253,0.35)' : 'rgba(168,85,247,0.35)'}`,
      color: 'var(--accent2)', whiteSpace: 'nowrap',
    }}>
      {isTeam ? '👥 ' : ''}{v}
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
        <span className="mono"><b style={{ color: 'var(--err)' }}>{it.problem.value.toFixed(2)}</b></span>
        <span className="mono" style={{ color: 'var(--text3)' }}> / {it.problem.threshold.toFixed(2)}</span>
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
        peak <span className="mono">{it.anomaly.peakRatio.toFixed(1)}x</span>
        {' · '}now <span className="mono">{it.anomaly.currentRatio.toFixed(1)}x</span>
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
