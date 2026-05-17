import { useEffect, useMemo, useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { api } from '@/lib/api';
import { tsLong } from '@/lib/utils';
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
    api.inbox({ status: statusFilter, service: serviceFilter || undefined, limit: 300 })
      .then(r => setData(r ?? []))
      .catch(() => setData(null));
  }, [statusFilter, serviceFilter]);

  const filtered = useMemo(() => {
    if (!data) return data;
    return data.filter(it =>
      prioSet.has(it.priority) &&
      kindSet.has(it.kind));
  }, [data, prioSet, kindSet]);

  const counts = useMemo(() => {
    const out: Record<string, number> = { P1: 0, P2: 0, P3: 0,
      problem: 0, exception: 0, anomaly: 0 };
    for (const it of data ?? []) {
      out[it.priority] = (out[it.priority] ?? 0) + 1;
      out[it.kind] = (out[it.kind] ?? 0) + 1;
    }
    return out;
  }, [data]);

  const goToSource = (it: InboxItem) => {
    if (it.kind === 'problem') navigate('/problems');
    else if (it.kind === 'exception') navigate(`/problems?tab=exceptions`);
    else if (it.kind === 'anomaly') navigate('/anomalies');
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
                          : pp === 'P2' ? 'var(--warn, #facc15)'
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

          <input value={serviceFilter}
            onChange={e => setServiceFilter(e.target.value)}
            placeholder="Filter by service…"
            style={{ fontSize: 12, padding: '4px 8px', minWidth: 180 }} />
        </div>

        {data === undefined && <Spinner />}
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
            <table>
              <thead>
                <tr>
                  <th style={{ width: 56 }}>Priority</th>
                  <th style={{ width: 90 }}>Source</th>
                  <th>Service</th>
                  <th>Detail</th>
                  <th style={{ width: 160 }}>Last seen</th>
                  <th style={{ width: 140 }}>Assignee</th>
                </tr>
              </thead>
              <tbody>
                {filtered.sort((a, b) => {
                  const ra = PRIO_RANK[a.priority] ?? 0;
                  const rb = PRIO_RANK[b.priority] ?? 0;
                  if (ra !== rb) return rb - ra;
                  return b.lastSeen - a.lastSeen;
                }).map(it => (
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
      ? { bg: 'rgba(250,204,21,0.12)', border: 'rgba(250,204,21,0.45)', color: 'var(--warn, #facc15)' }
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
