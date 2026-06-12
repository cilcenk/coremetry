import { useMemo } from 'react';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import { fmtSmart } from '@/lib/chartFmt';
import { fmtNum } from '@/lib/utils';
import type { PanelData } from './PanelStack';

// GroupTable (explore-v2 Phase 2) — ONE combined per-series breakdown across
// every panel (SUMMARY_COLS clone; col0 = letter badge + group label). Row
// hover focuses that series on its panel; click toggles visibility (eye).
// The @cursor column (cursorBus) lands in Phase 3 — Phase 2 shows Last.
// Sort + widths persist via the shared primitive (storageKey).

export interface GroupRow {
  rowKey: string;            // `${letter}:${label}` — the hidden/focus key
  letter: string;
  isFormula: boolean;
  label: string;
  unit: string;
  last: number;
  avg: number;
  max: number;
  buckets: number;
}

const COLS: DataTableColumn<GroupRow>[] = [
  { id: 'series',  label: 'Seri',     sortValue: r => `${r.letter} ${r.label}`, naturalDir: 'asc', width: 320 },
  { id: 'last',    label: 'Son',      sortValue: r => r.last,    numeric: true, width: 120 },
  { id: 'avg',     label: 'Ort',      sortValue: r => r.avg,     numeric: true, width: 120 },
  { id: 'max',     label: 'Maks',     sortValue: r => r.max,     numeric: true, width: 120 },
  { id: 'buckets', label: 'Bucket',   sortValue: r => r.buckets, numeric: true, width: 90 },
];

export function buildGroupRows(panels: PanelData[]): GroupRow[] {
  const rows: GroupRow[] = [];
  for (const p of panels) {
    if (p.loading) continue;
    for (const s of p.series) {
      const vs = s.points.map(x => x.value).filter((v): v is number => v != null && isFinite(v));
      rows.push({
        rowKey: `${p.letter}:${s.label}`,
        letter: p.letter,
        isFormula: p.isFormula,
        label: s.label,
        unit: p.unit,
        last: vs.length ? vs[vs.length - 1] : NaN,
        avg: vs.length ? vs.reduce((a, b) => a + b, 0) / vs.length : NaN,
        max: vs.length ? Math.max(...vs) : NaN,
        buckets: vs.length,
      });
    }
  }
  return rows;
}

export function GroupTable({ panels, hiddenKeys, onToggleHidden, onFocus }: {
  panels: PanelData[];
  hiddenKeys: Set<string>;
  onToggleHidden: (rowKey: string) => void;
  onFocus: (rowKey: string | null) => void;
}) {
  const rows = useMemo(() => buildGroupRows(panels), [panels]);

  const dt = useDataTable<GroupRow>({
    storageKey: 'explore-group-table',
    columns: COLS,
    rows,
    initialSort: { id: 'max', dir: 'desc' },
  });

  if (rows.length === 0) return null;

  return (
    <div className="table-wrap" style={{ marginTop: 12 }}
      onMouseLeave={() => onFocus(null)}>
      <table style={{ tableLayout: 'fixed', width: '100%' }}>
        <DataTableColgroup dt={dt} />
        <DataTableHead dt={dt} />
        <tbody>
          {dt.sortedRows.map(r => {
            const hidden = hiddenKeys.has(r.rowKey);
            return (
              <tr key={r.rowKey}
                onMouseEnter={() => onFocus(hidden ? null : r.rowKey)}
                onClick={() => onToggleHidden(r.rowKey)}
                title="Tıkla: seriyi gizle/göster · üzerine gel: panelde vurgula"
                style={{ cursor: 'pointer', opacity: hidden ? 0.45 : 1,
                         contentVisibility: 'auto', containIntrinsicSize: 'auto 36px' }}>
                <td style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                  <span style={{ marginRight: 6, fontSize: 11 }}>{hidden ? '○' : '◉'}</span>
                  <span style={{
                    display: 'inline-flex', alignItems: 'center', justifyContent: 'center',
                    width: 16, height: 16, borderRadius: 3, marginRight: 6,
                    background: r.isFormula ? 'var(--bg3)' : 'var(--accent2)',
                    color: r.isFormula ? 'var(--text2)' : 'var(--bg)',
                    fontSize: 10, fontWeight: 700, verticalAlign: 'middle',
                  }}>{r.letter}</span>
                  <b>{r.label}</b>
                </td>
                <td className="mono" style={{ textAlign: 'right' }}>{fmtSmart(r.last, r.unit)}</td>
                <td className="mono" style={{ textAlign: 'right' }}>{fmtSmart(r.avg, r.unit)}</td>
                <td className="mono" style={{ textAlign: 'right' }}>{fmtSmart(r.max, r.unit)}</td>
                <td className="mono" style={{ textAlign: 'right' }}>{fmtNum(r.buckets)}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
