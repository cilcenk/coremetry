// PanelLegend.tsx — v0.10.290 (chart audit B3 / Dilim 1.4): CorePanel'in
// iç lejantı KENDİ bileşeni. Saf sunum: istatistikler, görünürlük, odak ve
// katlanma durumu CorePanel'de kalır (sahibi uPlot instance'ı), burada
// yalnız DOM + jest sözleşmesi (legendVisibility: tık = izole, Ctrl/Cmd-tık
// = gizle/göster; Enter/Space klavye ikizleri, v0.9.711) yaşar.
//
// Kanonik lejant BUDUR (audit "StatsLegend kanonik" demişti; ölçüm tersini
// gösterdi: iç lejant hover/klavye vurgusu, tam-ad title'ı, display
// processor biçimlendirmesi ve toplanabilir-birim kuralı taşıyor,
// StatsLegend taşımıyor). StatsLegend hat B (TimeChart/OverviewChart) ile
// birlikte emekli olur (Dilim 2.5); yeni yüzey ona bağlanmaz.
import { Button } from '@/components/ui';
import { resolveVar } from '@/lib/chart/resolveVar';
import { seriesRoleColor, type SeriesRole } from '@/lib/chart/seriesRole';
import { isolateSeriesVisibility, toggleSeriesVisibility } from '@/lib/chart/legendVisibility';
import type { SeriesStat } from '@/lib/chart/legendStats';

export interface PanelLegendRow { name: string; stat: SeriesStat }

export function PanelLegend({
  count, open, onToggle, stats, vis, onVisChange, focusName, onHover, roles, fullNames, fmtCell, sumAdditive,
}: {
  count: number;
  open: boolean;
  onToggle: () => void;
  stats: PanelLegendRow[];
  vis: boolean[];
  onVisChange: (fn: (v: boolean[]) => boolean[]) => void;
  focusName: string | null;
  // onHover(name) vurgular; onHover(null, leaving) yalnız o satır vurguluysa bırakır.
  onHover: (name: string | null, leaving?: string) => void;
  roles?: SeriesRole[];
  fullNames: (string | undefined)[];
  fmtCell: (i: number, v: number | null) => string;
  sumAdditive: boolean;
}) {
  return (
    <div style={{ fontSize: 11 }}>
      <Button variant="secondary" size="xs"
        aria-expanded={open} onClick={onToggle}>
        {open ? '▼' : '▶'} Series ({count})
      </Button>
      {open && (
        <div style={{ overflowX: 'auto' }}>
        <table style={{ width: '100%', marginTop: 4 }}>
          <thead><tr>
            <th style={{ textAlign: 'left' }}>Seri</th>
            <th className="num">Son</th><th className="num">Min</th>
            <th className="num">Maks</th><th className="num">Ort</th>
            <th className="num">Toplam</th>
          </tr></thead>
          <tbody>
            {stats.map((s, i) => (
              <tr key={`${i}:${s.name}`}
                tabIndex={0}
                role="button"
                aria-pressed={vis[i] !== false}
                aria-label={`${s.name} — Enter: izole et, Boşluk: gizle/göster`}
                style={{
                  opacity: vis[i] === false ? 0.35 : 1, cursor: 'pointer',
                  background: focusName === s.name ? 'var(--bg2)' : undefined,
                }}
                // v0.9.793 — hover/fokus SERİYİ VURGULAR (TSP sözleşmesi).
                // Klavye fokusu da aynı kanaldan geçer: vurgu yalnız fareye
                // ait olsaydı Tab'la gezen operatör hangi satırın hangi
                // çizgi olduğunu göremezdi (v0.9.711 erişim dersi).
                onMouseEnter={() => onHover(s.name)}
                onMouseLeave={() => onHover(null, s.name)}
                onFocus={() => onHover(s.name)}
                onBlur={() => onHover(null, s.name)}
                onClick={e => onVisChange(v =>
                  e.ctrlKey || e.metaKey
                    ? toggleSeriesVisibility(v, i)
                    : isolateSeriesVisibility(v, i))}
                onKeyDown={e => {
                  // v0.9.711 — Ctrl+tık klavyeden ERİŞİLEMEZ (review
                  // bulgusu): Enter=izole, Space=tekil gizle/göster.
                  if (e.key === 'Enter') { e.preventDefault(); onVisChange(v => isolateSeriesVisibility(v, i)); }
                  if (e.key === ' ') { e.preventDefault(); onVisChange(v => toggleSeriesVisibility(v, i)); }
                }}>
                <td>
                  <span style={{
                    display: 'inline-block', width: 8, height: 8, borderRadius: 2,
                    background: resolveVar(seriesRoleColor(s.name, roles?.[i] ?? 'data')),
                    marginRight: 6,
                  }} />
                  {/* v0.9.1369 — tam ad title'da: lejant Grafana
                      yoğunluğunda kısa kalır (v0.9.539 operatör
                      isteği) ama operatör pod adını üzerine gelerek
                      okuyabilir/kopyalayabilir. */}
                  <span title={fullNames[i] || undefined}>{s.name}</span>
                </td>
                <td className="num">{fmtCell(i, s.stat.last)}</td>
                <td className="num">{fmtCell(i, s.stat.min)}</td>
                <td className="num">{fmtCell(i, s.stat.max)}</td>
                <td className="num">{fmtCell(i, s.stat.mean)}</td>
                <td className="num">{sumAdditive ? fmtCell(i, s.stat.sum) : '—'}</td>
              </tr>
            ))}
          </tbody>
        </table>
        </div>
      )}
    </div>
  );
}
