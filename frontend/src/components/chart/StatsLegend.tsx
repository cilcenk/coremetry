import { useState } from 'react';
import { fmtSmart } from '@/lib/chartFmt';
import { seriesStats, isAdditiveUnit } from '@/lib/chart/legendStats';

// StatsLegend (v0.9.103, Grafana-parity #1) — kompakt OVC/TC grafiklerinin
// ALTINA seri-başı istatistik tablosu (Seçenek A, operatör onaylı). MLC/TSP'de
// lejant zaten vardı; bu açık OVC + TC içindi. Paylaşımlı: iki preset de tüketir
// (panel-panel kopya değil). Değer formatı fmtSmart(v, unit); istatistik
// çekirdeği saf lib/chart/legendStats.ts (test'li).
//
// COLLAPSE (zorunlu): "Series (N)" ▶/▼ toggle HER ZAMAN var; >8 seride
// VARSAYILAN kapalı (TSP LEGEND_COLLAPSE_THRESHOLD deseni) — sayfa uzamasın.
//
// Sum/Σ + "Toplam" satırı yalnız TOPLANABİLİR birimde (rps/sayaç/bytes);
// ms/s/% gizlenir. Her seri kendi birimine göre (TC dual-axis: left/right).
// İzole/gizle tıklaması opsiyonel (onToggle verilirse satır tıklanabilir).

const LEGEND_COLLAPSE_THRESHOLD = 8;

export interface StatsLegendSeries {
  label: string;
  color: string;   // CSS var() token ya da renk — swatch'ta doğrudan
  values: (number | null)[];
  unit?: string;
}

export function StatsLegend({ series, onToggle, isVisible }: {
  series: StatsLegendSeries[];
  onToggle?: (i: number) => void;              // opsiyonel izole/gizle
  isVisible?: (i: number) => boolean;
}) {
  const [collapsed, setCollapsed] = useState(series.length > LEGEND_COLLAPSE_THRESHOLD);
  if (series.length === 0) return null;

  const stats = series.map(s => seriesStats(s.values));
  const additive = series.map(s => isAdditiveUnit(s.unit));
  const showSum = additive.some(Boolean);
  const num = (v: number | null, unit?: string) => (v == null ? '—' : fmtSmart(v, unit ?? ''));

  // "Toplam" satırı: yalnız toplanabilir serilerin last + sum'ı (anlamlı
  // olanlar); Min/Max/Avg toplanmaz (—). Toplanabilir seri yoksa satır yok.
  const addIdx = series.map((_, i) => i).filter(i => additive[i]);
  const totalLast = addIdx.reduce((a, i) => a + (stats[i].last ?? 0), 0);
  const totalSum = addIdx.reduce((a, i) => a + stats[i].sum, 0);
  const totalUnit = addIdx.length ? series[addIdx[0]].unit : '';

  const th: React.CSSProperties = { color: 'var(--text3)', fontWeight: 500, textAlign: 'right', padding: '2px 8px', fontSize: 10, textTransform: 'uppercase', letterSpacing: '.3px' };
  const td: React.CSSProperties = { padding: '3px 8px', textAlign: 'right', borderTop: '1px solid var(--border)', fontVariantNumeric: 'tabular-nums', fontFamily: 'ui-monospace, monospace' };

  return (
    <div style={{ marginTop: 8 }}>
      <button type="button" onClick={() => setCollapsed(c => !c)}
        style={{
          background: 'none', border: 'none', cursor: 'pointer', padding: '2px 4px',
          fontSize: 11, color: 'var(--text2)', display: 'inline-flex', alignItems: 'center', gap: 4,
        }}
        title={collapsed ? 'İstatistikleri göster' : 'İstatistikleri gizle'}>
        <span style={{ fontSize: 9 }}>{collapsed ? '▶' : '▼'}</span>
        Series ({series.length})
      </button>
      {!collapsed && (
        <div style={{ overflowX: 'auto', marginTop: 4 }}>
          <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 11 }}>
            <thead>
              <tr>
                <th style={{ ...th, textAlign: 'left' }}>Series</th>
                <th style={th}>Last</th>
                <th style={th}>Min</th>
                <th style={th}>Max</th>
                <th style={th}>Avg</th>
                {showSum && <th style={th}>Σ Sum</th>}
              </tr>
            </thead>
            <tbody>
              {series.map((s, i) => {
                const st = stats[i];
                const on = isVisible ? isVisible(i) : true;
                return (
                  <tr key={s.label + i}
                    onClick={onToggle ? e => { e.preventDefault(); onToggle(i); } : undefined}
                    style={{ opacity: on ? 1 : 0.4, cursor: onToggle ? 'pointer' : 'default' }}
                    title={onToggle ? 'Tıkla: bu seriyi izole/aç-kapat' : undefined}>
                    <td style={{ ...td, textAlign: 'left', color: 'var(--text2)', maxWidth: 260, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', fontFamily: 'inherit' }}>
                      <span style={{ display: 'inline-block', width: 9, height: 9, borderRadius: 2, background: s.color, marginRight: 7, verticalAlign: 'middle' }} />
                      <span style={{ verticalAlign: 'middle' }}>{s.label}</span>
                    </td>
                    <td style={{ ...td, color: 'var(--text)', fontWeight: 600 }}>{num(st.last, s.unit)}</td>
                    <td style={{ ...td, color: 'var(--text2)' }}>{num(st.min, s.unit)}</td>
                    <td style={{ ...td, color: 'var(--text2)' }}>{num(st.max, s.unit)}</td>
                    <td style={{ ...td, color: 'var(--text2)' }}>{num(st.mean, s.unit)}</td>
                    {showSum && <td style={{ ...td, color: 'var(--text2)' }}>{additive[i] ? num(st.sum, s.unit) : '—'}</td>}
                  </tr>
                );
              })}
            </tbody>
            {showSum && addIdx.length > 0 && (
              <tfoot>
                <tr>
                  <td style={{ ...td, textAlign: 'left', color: 'var(--text2)', borderTop: '1px solid var(--text3)', fontFamily: 'inherit', fontWeight: 600 }}>Toplam</td>
                  <td style={{ ...td, borderTop: '1px solid var(--text3)', color: 'var(--text2)', fontWeight: 600 }}>{num(totalLast, totalUnit)}</td>
                  <td style={{ ...td, borderTop: '1px solid var(--text3)' }}>—</td>
                  <td style={{ ...td, borderTop: '1px solid var(--text3)' }}>—</td>
                  <td style={{ ...td, borderTop: '1px solid var(--text3)' }}>—</td>
                  <td style={{ ...td, borderTop: '1px solid var(--text3)', color: 'var(--text2)', fontWeight: 600 }}>{num(totalSum, totalUnit)}</td>
                </tr>
              </tfoot>
            )}
          </table>
        </div>
      )}
    </div>
  );
}
