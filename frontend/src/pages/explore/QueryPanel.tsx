import { memo, lazy, Suspense } from 'react';
import { TimeSeriesPanel, type TSMode } from '@/components/viz/TimeSeriesPanel';
import { Spinner } from '@/components/Spinner';
import { Button } from '@/components/ui/Button';
import { publishCursor } from './cursorBus';
import type { PanelData } from './PanelStack';
import { chartsV2 } from '@/lib/featureFlags';
import type { ChartTimeRegion, ChartThreshold } from '@/lib/chart/overlays';

// v0.9.745 (Explore v2) — line modunda panel gövdesi CorePanel'e
// (@grafana/ui motoru, Overview ile AYNI render). Lazy: corePanelEntry
// vendor'ı sayfaya statik bağlamaz (708 bundle dersi).
//
// v0.9.785 — bars da v2 motorunda (CorePanel viz='bars', Grafana'nın
// kendi Bars draw-style'ı). stacked/area eski TimeSeriesPanel'de kalır:
// CorePanel o markları henüz çizmiyor, dürüst kademeli geçiş —
// ?chartsV2=0 hâlâ toptan kaçış.
//
// Bilinen v2 farkları (bilinçli, eski yol bir bayrak uzakta):
//   • focusedLabel (GroupTable hover vurgusu) v2'de henüz yok.
//   • GEÇİCİ AYRIM: v2 panelleri `${SYNC_KEY}-ms` grubunda, eski motor
//     `SYNC_KEY`'de senkron olur (x ekseni ms vs sn — yanlış hizalı
//     senkron hiç senkron olmamaktan kötüdür). Yani karma bir sayfada
//     (bars/line paneli + stacked paneli) crosshair motorlar arasında
//     geçmez. stacked dilimi bu ayrımı kapatacak; o gün iki grup teke
//     iner.
const CorePanelMultiLazy = lazy(() =>
  import('@/components/chart/corePanelEntry').then(m => ({ default: m.CorePanelMulti })));

// QueryPanel (explore-v2 Phase 2) — one query's chart card in the stack.
// Crosshair syncs across panels via uPlot.sync('explore-v2'); drag-zoom on
// any panel fans out through the parent's zoomWindow. React.memo per the
// plan's perf guards — only the touched panel re-renders on hover/zoom
// state changes that don't concern it.

const SYNC_KEY = 'explore-v2';
const PANEL_HEIGHT = 200;

export const QueryPanel = memo(function QueryPanel({
  panel, mode, hiddenLabels, focusedLabel, zoomWindow, onZoom, onZoomReset, onExemplarClick, logScale, onPin, xRange,
}: {
  panel: PanelData;
  mode: TSMode;
  hiddenLabels: Set<string>;
  focusedLabel: string | null;
  zoomWindow: { from: number; to: number } | null;
  xRange?: { from: number; to: number } | null;
  onZoom: (fromSec: number, toSec: number) => void;
  // Grafana-parite M1 — çift-tık: Explore'un lokal zoom geri-yığınını pop eder.
  onZoomReset?: () => void;
  onExemplarClick?: (traceId: string) => void;
  logScale?: boolean;   // v0.8.418 (DE3) — TimeSeriesPanel's log10 y-axis
  // v0.8.419 (DE4) — pin this query to a dashboard. Absent for formula
  // panels and OR-group queries (no dashboard equivalent — see
  // pinToDashboard.ts header), so the 📌 simply doesn't render.
  onPin?: () => void;
}) {
  return (
    <div style={{
      background: 'var(--bg1)', border: '1px solid var(--border)',
      borderRadius: 8, padding: '10px 14px 8px',
    }}>
      <div style={{
        display: 'flex', alignItems: 'center', gap: 8,
        fontSize: 11, color: 'var(--text2)', marginBottom: 6,
      }}>
        <span style={{
          display: 'inline-flex', alignItems: 'center', justifyContent: 'center',
          width: 18, height: 18, borderRadius: 4, flexShrink: 0,
          background: panel.isFormula ? 'var(--bg3)' : 'var(--accent2)',
          color: panel.isFormula ? 'var(--text2)' : 'var(--bg)',
          border: panel.isFormula ? '1px dashed var(--border)' : '1px solid var(--accent2)',
          fontSize: 11, fontWeight: 700,
        }}>{panel.letter}</span>
        <span style={{ color: 'var(--accent2)', fontWeight: 600 }}>{panel.desc}</span>
        <span style={{ flex: 1 }} />
        {!panel.loading && (
          <span style={{ color: 'var(--text3)' }}>
            {panel.series.length} seri{panel.more > 0 ? ` · +${panel.more} daha (alan bazlı kırpıldı)` : ''}
            {panel.rowsCapped && (
              <span style={{ color: 'var(--warn)' }}
                title="Sorgu 50k satır tavanına çarptı — seriler grup anahtarına göre ALFABETİK kesildi; geç harfli seriler eksik olabilir ve 'daha' sayısı gerçek evreni bilemez. Pencereyi daralt, adımı büyüt ya da filtre ekle.">
                {' '}· ⚠ satır tavanı doldu — liste eksik olabilir
              </span>
            )}
            {panel.unit ? ` · ${panel.unit}` : ''}
          </span>
        )}
        {onPin && (
          <Button variant="ghost" size="sm" onClick={onPin}
            title="Dashboard'a pinle — bu sorgu canlı bir panel olarak eklenir"
            aria-label={`Pin query ${panel.letter} to a dashboard`}>
            📌
          </Button>
        )}
      </div>
      {panel.loading ? (
        <div style={{ height: PANEL_HEIGHT, display: 'grid', placeItems: 'center' }}><Spinner /></div>
      ) : panel.series.length === 0 ? (
        <div style={{
          height: PANEL_HEIGHT, display: 'grid', placeItems: 'center',
          color: 'var(--text3)', fontSize: 12,
        }}>
          {panel.isFormula
            ? 'Formül için ortak zaman aralığında veri yok'
            : 'Bu pencerede veri yok — aralığı genişlet veya filtreleri azalt'}
        </div>
      ) : (chartsV2() && (mode === 'line' || mode === 'bars')) ? (
        <Suspense fallback={<div style={{ height: PANEL_HEIGHT, display: 'grid', placeItems: 'center' }}><Spinner /></div>}>
          <CorePanelMultiLazy
            title=""
            storageKey={`explore-panel-${panel.key}`}
            height={PANEL_HEIGHT}
            // Kapı yalnız bu ikisini geçiriyor; ternary üçüncü bir mark'ı
            // sessizce line'a düşürmez çünkü üçüncü mark buraya GELMEZ.
            viz={mode === 'bars' ? 'bars' : 'line'}
            unit={panel.unit || undefined}
            items={panel.series.map(ts => ({
              name: ts.label,
              role: 'data' as const,
              series: [{ groupKey: [], points: ts.points
                .filter(pt => pt.value != null)
                .map(pt => ({ time: pt.time, value: pt.value as number })) }],
              exemplars: ts.exemplars,
            }))}
            hiddenNames={hiddenLabels}
            hideLegend
            xRange={zoomWindow ?? xRange}
            regions={exploreRegions(panel)}
            thresholds={exploreThresholds(panel)}
            logScale={logScale}
            syncKey={`${SYNC_KEY}-ms`}
            onCursorTime={publishCursor}
            onExemplarClick={onExemplarClick}
            onZoom={onZoom}
            onZoomReset={onZoomReset}
          />
        </Suspense>
      ) : (
        <TimeSeriesPanel
          series={panel.series}
          deploys={panel.deploys}
          events={panel.events}
          thresholds={panel.thresholds}
          height={PANEL_HEIGHT}
          mode={mode}
          logScale={logScale}
          syncKey={SYNC_KEY}
          hideLegend
          zoomWindow={zoomWindow}
          xRange={xRange}
          hiddenLabels={hiddenLabels}
          focusedLabel={focusedLabel}
          onCursorTime={publishCursor}
          onExemplarClick={onExemplarClick}
          onZoom={onZoom}
          onZoomReset={onZoomReset} />
      )}
    </div>
  );
});


// Saf eşlemeler — deploy ▼ (ns) ve operatör olayları (A7) CorePanel'in
// region kanalına iner: fromSec===toSec ince dikey bant çizer.
function exploreRegions(panel: PanelData): ChartTimeRegion[] | undefined {
  const out: ChartTimeRegion[] = [];
  for (const d of panel.deploys ?? []) {
    out.push({ fromSec: d / 1e9, toSec: d / 1e9, color: 'var(--accent2)', label: 'deploy' });
  }
  for (const e of panel.events ?? []) {
    out.push({ fromSec: e.timeUnixNs / 1e9, toSec: e.timeUnixNs / 1e9, label: e.label });
  }
  return out.length ? out : undefined;
}

function exploreThresholds(panel: PanelData): ChartThreshold[] | undefined {
  if (!panel.thresholds?.length) return undefined;
  return panel.thresholds.map(t => ({ value: t.value, label: t.label, color: t.color }));
}
