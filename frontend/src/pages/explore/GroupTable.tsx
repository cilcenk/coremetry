import { useMemo } from 'react';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import { Button } from '@/components/ui/Button';
import { panelsToCSV } from './exploreCsv';
import type { DataTableColumn } from '@/lib/dataTable';
import { fmtSmart, seriesColor } from '@/lib/chartFmt';
import { seriesStats, isAdditiveUnit } from '@/lib/chart/legendStats';
import { fmtNum } from '@/lib/utils';
import { useCursorTime, valueAtCursor } from './cursorBus';
import type { PanelData } from './PanelStack';

// GroupTable (explore-v2 Phase 2/3) — ONE combined per-series breakdown across
// every panel (SUMMARY_COLS clone; col0 = letter badge + group label). Row
// hover focuses that series on its panel; click toggles visibility (eye).
// Phase 3: the @imleç column reads the cursorBus and shows each series' value
// under the synced crosshair (blank when no cursor). Sort + widths persist via
// the shared primitive (storageKey).

export interface GroupRow {
  rowKey: string;            // `${letter}:${label}` — the hidden/focus key
  letter: string;
  isFormula: boolean;
  label: string;
  unit: string;
  // color — the series' chart colour. BOTH render engines derive a line's
  // colour from seriesColor(label) (the legacy TimeSeriesPanel through
  // PanelData.series[].color, the v2 CorePanel through seriesRoleColor's
  // 'data' role), so a row can be tinted to match its line without the two
  // ever drifting.
  color: string;
  last: number;
  min: number;
  avg: number;
  max: number;
  // sum — MEANINGFUL only when `additive`; a p95-latency column summed
  // across pods is a number with no referent.
  sum: number;
  additive: boolean;
  buckets: number;
  points: { time: number; value: number | null }[];  // for the @imleç lookup
}

// @imleç is intentionally NOT sortable (no sortValue): its value changes every
// cursor frame, so making it a sort key would force a re-sort per mousemove.
// Omitting sortValue keeps the column resizable but inert — rows stay stable.
//
// v0.9.806 — Min + Toplam eklendi; sıra Son·Min·Maks·Ort·Toplam. Toplam'ın
// sortValue'su toplanamaz birimde NULL döner: sortRows null'ı yön ne olursa
// olsun en dibe indirir, yani "—" basan satırlar sıralamayı kirletmez.
const COLS: DataTableColumn<GroupRow>[] = [
  { id: 'series',  label: 'Seri',     sortValue: r => `${r.letter} ${r.label}`, naturalDir: 'asc', width: 320 },
  { id: 'cursor',  label: '@imleç',   numeric: true, width: 110 },
  { id: 'last',    label: 'Son',      sortValue: r => r.last,    numeric: true, width: 110 },
  { id: 'min',     label: 'Min',      sortValue: r => r.min,     numeric: true, width: 110 },
  { id: 'max',     label: 'Maks',     sortValue: r => r.max,     numeric: true, width: 110 },
  { id: 'avg',     label: 'Ort',      sortValue: r => r.avg,     numeric: true, width: 110 },
  { id: 'sum',     label: 'Toplam',   sortValue: r => (r.additive ? r.sum : null), numeric: true, width: 110 },
  { id: 'buckets', label: 'Bucket',   sortValue: r => r.buckets, numeric: true, width: 90 },
];

export function buildGroupRows(panels: PanelData[]): GroupRow[] {
  const rows: GroupRow[] = [];
  for (const p of panels) {
    if (p.state !== 'ready') continue;   // v0.9.804 — idle/loading/error satır üretmez
    // v0.9.806 — Toplam sütununun kapısı panel BAŞINA sorulur: birim
    // paneldeki tüm serilerde aynı.
    const additive = isAdditiveUnit(p.unit);
    for (const s of p.series) {
      // seriesStats — lejantla PAYLAŞILAN tek geçişli çekirdek (v0.9.103).
      // Kendi reduce/Math.max zincirimizi yazmak, aynı sayının iki yerde
      // farklı hesaplanma riskini geri getirirdi; ayrıca Math.max(...vs)
      // spread'i uzun serilerde yığın sınırına dayanır.
      const st = seriesStats(s.points.map(x => x.value));
      rows.push({
        rowKey: `${p.letter}:${s.label}`,
        letter: p.letter,
        isFormula: p.isFormula,
        label: s.label,
        unit: p.unit,
        color: seriesColor(s.label),
        // Boş seride NaN — fmtSmart bunu "—" basar (mevcut sözleşme).
        last: st.last ?? NaN,
        min: st.min ?? NaN,
        avg: st.mean ?? NaN,
        max: st.max ?? NaN,
        sum: st.count ? st.sum : NaN,
        additive,
        buckets: st.count,
        points: s.points,
      });
    }
  }
  return rows;
}

export function GroupTable({ panels, hiddenKeys, onToggleHidden, onIsolate, onFocus }: {
  panels: PanelData[];
  hiddenKeys: Set<string>;
  onToggleHidden: (rowKey: string) => void;
  // v0.9.757 (operatör: "bir tanesine bastığımda sadece o gözüksün") —
  // düz tık İZOLE eder (CorePanel lejantı semantiği); Ctrl/Cmd+tık eski
  // tekil gizle/göster. İzole olanı yeniden tıklamak hepsini geri açar.
  onIsolate: (rowKey: string) => void;
  onFocus: (rowKey: string | null) => void;
}) {
  const rows = useMemo(() => buildGroupRows(panels), [panels]);
  // Re-renders this table (and only this table) once per animation frame while
  // the crosshair moves — the charts stay out of React via uPlot's own sync.
  const cursorSec = useCursorTime();

  const dt = useDataTable<GroupRow>({
    storageKey: 'explore-group-table',
    columns: COLS,
    rows,
    initialSort: { id: 'max', dir: 'desc' },
  });

  if (rows.length === 0) return null;

  // v0.8.412 (Data-Explorer parity DE1) — export the full result set
  // (every series point, long format) as CSV. Client-only Blob; no
  // request, no size surprise (the rows are already in memory).
  const exportCSV = () => {
    const blob = new Blob([panelsToCSV(panels)], { type: 'text/csv;charset=utf-8' });
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = `coremetry-explore-${new Date().toISOString().replace(/[:.]/g, '-')}.csv`;
    a.click();
    URL.revokeObjectURL(a.href);
  };

  return (
    <div className="table-wrap is-fit" style={{ marginTop: 12 }}
      onMouseLeave={() => onFocus(null)}>
      <div style={{ display: 'flex', justifyContent: 'flex-end', padding: '6px 8px 0' }}>
        <Button variant="secondary" size="sm" onClick={exportCSV}
          title={panels.some(p => p.more > 0 || p.rowsCapped)
            ? 'Görünen serileri indirir (top-N kırpılmış küme) — kırpma CSV içinde # yorum satırıyla işaretlidir'
            : 'Download every series point (long format: query, series, unit, time, value)'}>
          ⤓ CSV
        </Button>
      </div>
      <table style={{ tableLayout: 'fixed', width: '100%' }}>
        <DataTableColgroup dt={dt} />
        <DataTableHead dt={dt} />
        <tbody>
          {dt.sortedRows.map(r => {
            const hidden = hiddenKeys.has(r.rowKey);
            return (
              <tr key={r.rowKey}
                onMouseEnter={() => onFocus(hidden ? null : r.rowKey)}
                onClick={(e) => (e.ctrlKey || e.metaKey) ? onToggleHidden(r.rowKey) : onIsolate(r.rowKey)}
                title="Tıkla: yalnız bu seri · Ctrl/Cmd+tık: gizle-göster · üzerine gel: panelde vurgula"
                style={{ cursor: 'pointer', opacity: hidden ? 0.45 : 1,
                         contentVisibility: 'auto', containIntrinsicSize: 'auto 36px' }}>
                <td style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                  {/* v0.9.806 — glif SERİNİN RENGİNDE. Tablo tek lejant ama
                      renksizdi: 10 serilik bir panelde satırı çizgiyle
                      eşleştirmek imkânsızdı. Gizli seride --text3, çünkü
                      renk "bu çizgi ekranda" demektir. */}
                  <span aria-hidden style={{
                    marginRight: 6, fontSize: 11,
                    color: hidden ? 'var(--text3)' : r.color,
                  }}>{hidden ? '○' : '◉'}</span>
                  <span style={{
                    display: 'inline-flex', alignItems: 'center', justifyContent: 'center',
                    width: 16, height: 16, borderRadius: 3, marginRight: 6,
                    background: r.isFormula ? 'var(--bg3)' : 'var(--accent2)',
                    color: r.isFormula ? 'var(--text2)' : 'var(--bg)',
                    fontSize: 10, fontWeight: 700, verticalAlign: 'middle',
                  }}>{r.letter}</span>
                  <b>{r.label}</b>
                </td>
                <td className="mono" style={{ textAlign: 'right', color: cursorSec == null ? 'var(--text3)' : 'var(--accent2)' }}>
                  {cursorSec == null ? '·' : fmtSmart(valueAtCursor(r.points, cursorSec), r.unit)}
                </td>
                <td className="mono" style={{ textAlign: 'right' }}>{fmtSmart(r.last, r.unit)}</td>
                <td className="mono" style={{ textAlign: 'right' }}>{fmtSmart(r.min, r.unit)}</td>
                <td className="mono" style={{ textAlign: 'right' }}>{fmtSmart(r.max, r.unit)}</td>
                <td className="mono" style={{ textAlign: 'right' }}>{fmtSmart(r.avg, r.unit)}</td>
                {/* Toplam yalnız TOPLANABİLİR birimde. ms/%/latency'de
                    pod'lar arası toplam anlamsız bir sayı olurdu — "—"
                    basıp neden olduğunu title'da söylüyoruz. */}
                <td className="mono" style={{ textAlign: 'right' }}
                  title={r.additive ? undefined
                    : `Toplam bu birimde anlamlı değil (${r.unit || 'birimsiz'})`}>
                  {r.additive ? fmtSmart(r.sum, r.unit) : '—'}
                </td>
                <td className="mono" style={{ textAlign: 'right' }}>{fmtNum(r.buckets)}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
