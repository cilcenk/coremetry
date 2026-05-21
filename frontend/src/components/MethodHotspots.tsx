import { useMemo, useState } from 'react';
import type { FlameNode, ProfileFrameKind } from '@/lib/types';
import { flameToHotspots, sortHotspots, flameCategoryBreakdown, type HotspotSort, type MethodHotspot } from '@/lib/flameHotspots';
import { KindBadge, BreakdownBar, kindLabel } from './KindBadge';

// Method Hotspots — Dynatrace-style "which functions are
// heaviest, ignoring call site" table. Sits below the flame
// graph on /profile and is the second thing an operator scans
// after the flame. Sortable by Self / Total / Paths, filterable
// by name substring, capped at top 100 so a noisy profile
// doesn't lock the page.

const ROW_CAP = 100;

export function MethodHotspots({ root }: { root: FlameNode }) {
  const [sortBy, setSortBy] = useState<HotspotSort>('self');
  const [filter, setFilter] = useState('');
  // Kind filter — operators chasing a lock-contention regression
  // want to see only Lock rows, not the CPU ones. 'all' is the
  // default; clicking a kind chip toggles it on; clicking the
  // active kind clears the filter.
  const [kindFilter, setKindFilter] = useState<ProfileFrameKind | 'all'>('all');

  const allHotspots = useMemo(() => flameToHotspots(root), [root]);
  const breakdown = useMemo(() => flameCategoryBreakdown(root), [root]);
  const totalValue = root.value || 1;

  const visible = useMemo(() => {
    const f = filter.trim().toLowerCase();
    let list = allHotspots;
    if (kindFilter !== 'all') list = list.filter(h => h.kind === kindFilter);
    if (f) list = list.filter(h => h.name.toLowerCase().includes(f));
    list = sortHotspots(list, sortBy);
    return list.slice(0, ROW_CAP);
  }, [allHotspots, sortBy, filter, kindFilter]);

  if (allHotspots.length === 0) return null;

  return (
    <div style={{
      marginTop: 16,
      background: 'var(--bg1)',
      border: '1px solid var(--border)',
      borderRadius: 8,
      padding: 12,
    }}>
      <BreakdownBar b={breakdown} />
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 12, marginBottom: 10, flexWrap: 'wrap' }}>
        <span style={{ fontSize: 13, fontWeight: 700 }}>Method hotspots</span>
        <span style={{ fontSize: 11, color: 'var(--text3)' }}>
          {visible.length} of {allHotspots.length} shown
        </span>
        <KindFilterChips cur={kindFilter} onChange={setKindFilter} />
        <input
          type="text"
          value={filter}
          onChange={e => setFilter(e.target.value)}
          placeholder="Filter by name…"
          style={{
            marginLeft: 'auto',
            padding: '4px 10px', fontSize: 12,
            background: 'var(--bg)', color: 'var(--text)',
            border: '1px solid var(--border)', borderRadius: 4,
            width: 220,
          }}
        />
      </div>
      <div className="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Method</th>
              <th style={{ width: 220 }}>Location</th>
              <SortHeader col="self" cur={sortBy} onChange={setSortBy} label="Self" />
              <SortHeader col="total" cur={sortBy} onChange={setSortBy} label="Total" />
              <SortHeader col="paths" cur={sortBy} onChange={setSortBy} label="Paths" />
            </tr>
          </thead>
          <tbody>
            {visible.map(h => (
              <HotspotRow key={h.name} h={h} totalValue={totalValue} />
            ))}
          </tbody>
        </table>
      </div>
      <div style={{ marginTop: 8, fontSize: 11, color: 'var(--text3)' }}>
        <b>Self</b> = samples where the method was the leaf ·{' '}
        <b>Total</b> = samples where the method appeared anywhere on the stack ·{' '}
        <b>Paths</b> = distinct callers that reached this method
      </div>
    </div>
  );
}

function KindFilterChips({ cur, onChange }: {
  cur: ProfileFrameKind | 'all';
  onChange: (k: ProfileFrameKind | 'all') => void;
}) {
  const kinds: (ProfileFrameKind | 'all')[] = ['all', 'cpu', 'lock', 'io', 'sleep', 'gc'];
  return (
    <span style={{ display: 'inline-flex', gap: 4 }}>
      {kinds.map(k => {
        const active = k === cur;
        const label = k === 'all' ? 'All' : kindLabel(k as ProfileFrameKind);
        return (
          <button key={k}
            onClick={() => onChange(active && k !== 'all' ? 'all' : k)}
            className={active ? '' : 'sec'}
            style={{
              fontSize: 10, padding: '2px 8px', borderRadius: 3,
              border: '1px solid var(--border)',
              background: active ? 'var(--accent2)' : 'transparent',
              color: active ? 'white' : 'var(--text2)',
              cursor: 'pointer', fontWeight: 600,
            }}>
            {label}
          </button>
        );
      })}
    </span>
  );
}

function SortHeader({
  col, cur, onChange, label,
}: { col: HotspotSort; cur: HotspotSort; onChange: (c: HotspotSort) => void; label: string }) {
  const active = col === cur;
  return (
    <th className="num" style={{ width: 130, cursor: 'pointer' }} onClick={() => onChange(col)}>
      <span style={{ color: active ? 'var(--text)' : 'var(--text2)' }}>
        {label}{active ? ' ▼' : ''}
      </span>
    </th>
  );
}

function HotspotRow({ h, totalValue }: { h: MethodHotspot; totalValue: number }) {
  const selfPct = (h.self / totalValue) * 100;
  const totalPct = (h.total / totalValue) * 100;
  return (
    <tr style={{ contentVisibility: 'auto', containIntrinsicSize: 'auto 32px' }}>
      <td className="mono" style={{ wordBreak: 'break-all', fontSize: 12 }} title={h.name}>
        {h.name}<KindBadge kind={h.kind} />
      </td>
      <td className="mono" style={{ fontSize: 11, color: 'var(--text2)', wordBreak: 'break-all' }}>
        {h.file ? `${h.file}${h.line ? `:${h.line}` : ''}` : '—'}
      </td>
      <td className="num mono"><Bar pct={selfPct} value={h.self} /></td>
      <td className="num mono"><Bar pct={totalPct} value={h.total} /></td>
      <td className="num mono">{h.paths.toLocaleString()}</td>
    </tr>
  );
}

function Bar({ pct, value }: { pct: number; value: number }) {
  // Inline horizontal bar — width tracks the percentage so the
  // operator can eye-scan the column. Number on top of the bar
  // so the bar is a visual aid, not a replacement for the
  // numbers.
  const safe = Math.max(0, Math.min(100, pct));
  return (
    <div style={{ position: 'relative', minWidth: 110 }}>
      <div style={{
        position: 'absolute', inset: 0,
        background: 'linear-gradient(to right, var(--accent2) 0%, var(--accent2) ' + safe + '%, transparent ' + safe + '%)',
        opacity: 0.18,
        borderRadius: 2,
      }} />
      <span style={{ position: 'relative', fontSize: 11 }}>
        {value.toLocaleString()} <span style={{ color: 'var(--text3)' }}>({safe.toFixed(1)}%)</span>
      </span>
    </div>
  );
}
