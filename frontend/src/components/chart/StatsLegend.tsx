import { useState } from 'react';
import { fmtSmart } from '@/lib/chartFmt';
import { seriesStats, isAdditiveUnit, resolveLegendCollapsed } from '@/lib/chart/legendStats';
import { getItem, setItem, legendCollapseKey } from '@/lib/storage';
import { DisclosureButton } from '@/components/ui';

// StatsLegend (v0.9.103, Grafana-parity #1) — kompakt OVC/TC grafiklerinin
// ALTINA seri-başı istatistik tablosu (Seçenek A, operatör onaylı). MLC/TSP'de
// lejant zaten vardı; bu açık OVC + TC içindi. Paylaşımlı: iki preset de tüketir
// (panel-panel kopya değil). Değer formatı fmtSmart(v, unit); istatistik
// çekirdeği saf lib/chart/legendStats.ts (test'li).
//
// COLLAPSE (zorunlu): "Series (N)" ▶/▼ toggle HER ZAMAN var; >8 seride
// VARSAYILAN kapalı (TSP LEGEND_COLLAPSE_THRESHOLD deseni) — sayfa uzamasın.
// v0.9.483 — çağıran defaultCollapsed ile eşikten bağımsız kapalı başlatabilir
// (Overview RED kartları: 4 seri, eşiğe hiç değmiyordu ama tablo ~150px dikey
// alanı kalıcı yiyordu — operatör raporu) ve storageKey ile kullanıcının
// tercihi grafik başına kalıcılaşır.
//
// Sum/Σ + "Toplam" satırı yalnız TOPLANABİLİR birimde (rps/sayaç/bytes);
// ms/s/% gizlenir. Her seri kendi birimine göre (TC dual-axis: left/right).
// İzole/gizle tıklaması opsiyonel (onToggle verilirse satır tıklanabilir):
// düz tık = isolate (yalnız o seri), Ctrl/Cmd-tık = toggle (gizle/göster) —
// MLC/TSP'nin yerleşik v0.5.364 sözleşmesi + Grafana kontratıyla AYNI jest
// dili (review 8/8 #8 — tek uygulama içinde ters eşleme olmaz); işlem
// semantiği lib/chart/legendVisibility.ts'te, jest→işlem eşlemesi caller'da
// (OVC/TC handleLegendToggle).

const LEGEND_COLLAPSE_THRESHOLD = 8;

export interface StatsLegendSeries {
  label: string;
  color: string;   // CSS var() token ya da renk — swatch'ta doğrudan
  values: (number | null)[];
  unit?: string;
}

export function StatsLegend({ series, onToggle, isVisible, defaultCollapsed, storageKey }: {
  series: StatsLegendSeries[];
  // v0.9.483 — açık/kapalı seçimi grafik başına KALICI (legendStorageKey ile
  // aynı anahtar, ayrı aile: lib/storage.ts legendCollapseKey). Bir kez açan
  // kullanıcı her ziyarette açık bulur; hiç dokunmayan default'u görür.
  // Anahtar verilmezse davranış eskisi gibi (oturumluk state).
  storageKey?: string;
  // v0.9.245 — eşikten BAĞIMSIZ olarak kapalı başlat. Traces histogramında
  // tam 3 seri var, yani LEGEND_COLLAPSE_THRESHOLD hiç devreye girmiyor ve
  // istatistik tablosu ~150px'i kalıcı olarak yiyor; asıl iş olan trace
  // tablosu ekranın altına düşüyordu (operatör raporu). Toggle yerinde
  // duruyor, tek değişen açılış durumu.
  defaultCollapsed?: boolean;
  // Opsiyonel gizle/izole — additive true = Ctrl/Cmd-tık (toggle: gizle/
  // göster); düz tık (false) = isolate (yalnız o seri). MLC/TSP v0.5.364
  // jest sözleşmesinin aynısı.
  onToggle?: (i: number, additive: boolean) => void;
  isVisible?: (i: number) => boolean;
}) {
  // v0.9.483 — açılış durumu: kalıcı kullanıcı seçimi > defaultCollapsed >
  // seri-sayısı eşiği (çözüm saf: legendStats.resolveLegendCollapsed).
  // Lazy initializer: localStorage bir kez, mount'ta okunur.
  const [collapsed, setCollapsed] = useState(() => resolveLegendCollapsed(
    storageKey ? getItem<boolean | null>(legendCollapseKey(storageKey), null) : null,
    defaultCollapsed, series.length, LEGEND_COLLAPSE_THRESHOLD,
  ));
  const toggleCollapsed = () => {
    // Yan etki updater'ın DIŞINDA — StrictMode updater'ı iki kez çağırır,
    // içeride yazmak çift kayıt demek olurdu.
    const next = !collapsed;
    setCollapsed(next);
    if (storageKey) setItem(legendCollapseKey(storageKey), next);
  };
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
      <DisclosureButton expanded={!collapsed} onClick={toggleCollapsed}
        title={collapsed ? 'İstatistikleri göster' : 'İstatistikleri gizle'}>
        Series ({series.length})
      </DisclosureButton>
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
                    onClick={onToggle ? e => { e.preventDefault(); onToggle(i, e.ctrlKey || e.metaKey); } : undefined}
                    style={{ opacity: on ? 1 : 0.4, cursor: onToggle ? 'pointer' : 'default' }}
                    title={onToggle ? 'Tıkla: yalnız bu seri · Ctrl/Cmd-tık: gizle/göster' : undefined}>
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
