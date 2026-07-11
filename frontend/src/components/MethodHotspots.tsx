import { useMemo, useState } from 'react';
import { Button } from '@/components/ui';
import type { FlameNode, ProfileFrameKind } from '@/lib/types';
import { flameToHotspots, flameCategoryBreakdown, type MethodHotspot } from '@/lib/flameHotspots';
import { KindBadge, BreakdownBar, kindLabel } from './KindBadge';
import { useDataTable, DataTableColgroup, DataTableHead } from './DataTable';
import type { DataTableColumn } from '@/lib/dataTable';

// Method Hotspots — Dynatrace-style "which functions are
// heaviest, ignoring call site" table. Sits below the flame
// graph on /profile and is the second thing an operator scans
// after the flame. Sortable by Self / Total / Paths, filterable
// by name substring, capped at top 100 so a noisy profile
// doesn't lock the page.

const ROW_CAP = 100;

// Columns for the shared sortable + resizable DataTable primitive
// (v0.8.306 — replaces the hand-rolled SortHeader + sortHotspots
// pair). Self desc is the classic hotspot default; Method /
// Location gain sorting for free (asc-natural, nulls-last on
// missing locations).
const HOTSPOT_COLS: DataTableColumn<MethodHotspot>[] = [
  { id: 'method',   label: 'Method',   sortValue: h => h.name, naturalDir: 'asc', width: 380, minWidth: 160 },
  { id: 'location', label: 'Location', sortValue: h => h.file ? `${h.file}:${h.line ?? 0}` : null, naturalDir: 'asc', width: 220 },
  { id: 'self',     label: 'Self',     sortValue: h => h.self,  numeric: true, width: 130 },
  { id: 'total',    label: 'Total',    sortValue: h => h.total, numeric: true, width: 130 },
  { id: 'paths',    label: 'Paths',    sortValue: h => h.paths, numeric: true, width: 130 },
];

export function MethodHotspots({ root }: { root: FlameNode }) {
  const [filter, setFilter] = useState('');
  // Kind filter — operators chasing a lock-contention regression
  // want to see only Lock rows, not the CPU ones. 'all' is the
  // default; clicking a kind chip toggles it on; clicking the
  // active kind clears the filter.
  const [kindFilter, setKindFilter] = useState<ProfileFrameKind | 'all'>('all');

  const allHotspots = useMemo(() => flameToHotspots(root), [root]);
  const breakdown = useMemo(() => flameCategoryBreakdown(root), [root]);
  const totalValue = root.value || 1;

  const filtered = useMemo(() => {
    const f = filter.trim().toLowerCase();
    let list = allHotspots;
    if (kindFilter !== 'all') list = list.filter(h => h.kind === kindFilter);
    if (f) list = list.filter(h => h.name.toLowerCase().includes(f));
    return list;
  }, [allHotspots, filter, kindFilter]);

  // Shared sortable + resizable table. Client sort — the whole
  // profile is already in memory. ROW_CAP applies AFTER the sort so
  // the table keeps showing the top 100 by the active column.
  const dt = useDataTable<MethodHotspot>({
    storageKey: 'method-hotspots',
    columns: HOTSPOT_COLS,
    rows: filtered,
    initialSort: { id: 'self', dir: 'desc' },
  });
  const visible = useMemo(() => dt.sortedRows.slice(0, ROW_CAP), [dt.sortedRows]);

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
        <table style={{ tableLayout: 'fixed', width: '100%' }}>
          <DataTableColgroup dt={dt} />
          <DataTableHead dt={dt} />
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
          <Button key={k}
            onClick={() => onChange(active && k !== 'all' ? 'all' : k)}
            variant={active ? 'primary' : 'secondary'} size="sm">
            {label}
          </Button>
        );
      })}
    </span>
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
