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
// v0.9.788 — DÖRT mark da v2 motorunda (line · bars · area · stacked).
// Kademeli geçiş bitti: mod artık motoru SEÇMİYOR, yalnız chartsV2
// bayrağı seçiyor (?chartsV2=0 hâlâ toptan kaçış).
//
// Bunun doğrudan sonucu: SAYFADAKİ ÇİFT SENKRON GRUBU ayrımı SONA ERDİ.
// Eskiden karma bir sayfa (bars paneli + stacked paneli) iki ayrı uPlot
// sync grubuna bölünüyordu ve crosshair motorlar arasında geçmiyordu;
// artık bir sayfadaki tüm paneller aynı motorda, yani tek grupta.
// Anahtar hâlâ MOTORA göre ayrışır (v2 x'i ms, eski motor sn — yanlış
// hizalı senkron, hiç senkron olmamaktan kötüdür) ama bayrak sayfa
// çapında olduğu için sayfa başına DAİMA tek grup düşer.
//
// v0.9.793 — son iki v2 farkı da kapandı: focusedLabel (GroupTable hover
// vurgusu) CorePanel'in kontrollü odak kanalına, formül panelinin kesikli
// çizgisi de item.dashed'e bağlandı. v2 artık eski yolun görsel üstkümesi.
const CorePanelMultiLazy = lazy(() =>
  import('@/components/chart/corePanelEntry').then(m => ({ default: m.CorePanelMulti })));

// QueryPanel (explore-v2 Phase 2) — one query's chart card in the stack.
// Crosshair syncs across panels via uPlot.sync('explore-v2'); drag-zoom on
// any panel fans out through the parent's zoomWindow. React.memo per the
// plan's perf guards — only the touched panel re-renders on hover/zoom
// state changes that don't concern it.

// Sayfa başına TEK senkron grubu; anahtar motorun x birimini taşır
// (v2 = ms). Mod artık anahtara karışmaz.
const SYNC_KEY = 'explore-v2';
const SYNC_KEY_V2 = `${SYNC_KEY}-ms`;
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
        {panel.state === 'ready' && (
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
      {/* v0.9.804 — DÖRT durum, dördü de kendi cümlesini kuruyor. Tek
          `loading` bayrağı varken "hiç sorulmadı" ve "sorgu hata verdi"
          durumları da spinner'a düşüyor ve panel SONSUZA DEK dönüyordu. */}
      {panel.state === 'idle' ? (
        <div style={{
          height: PANEL_HEIGHT, display: 'grid', placeItems: 'center',
          color: 'var(--text3)', fontSize: 12, textAlign: 'center', padding: '0 16px',
        }}>
          {panel.emptyReason}
        </div>
      ) : panel.state === 'loading' ? (
        <div style={{ height: PANEL_HEIGHT, display: 'grid', placeItems: 'center' }}><Spinner /></div>
      ) : panel.state === 'error' ? (
        <div role="alert" style={{
          height: PANEL_HEIGHT, display: 'flex', flexDirection: 'column',
          alignItems: 'center', justifyContent: 'center', gap: 6,
          color: 'var(--err)', fontSize: 12, textAlign: 'center', padding: '0 16px',
        }}>
          <span style={{ fontSize: 18 }}>⚠</span>
          <span>Sorgu {panel.letter} hata verdi</span>
          <span style={{ color: 'var(--text2)', wordBreak: 'break-word' }}>
            {panel.errorMessage}
          </span>
        </div>
      ) : panel.series.length === 0 ? (
        <div style={{
          height: PANEL_HEIGHT, display: 'grid', placeItems: 'center',
          color: 'var(--text3)', fontSize: 12, textAlign: 'center', padding: '0 16px',
        }}>
          {panel.emptyReason}
        </div>
      ) : chartsV2() ? (
        <Suspense fallback={<div style={{ height: PANEL_HEIGHT, display: 'grid', placeItems: 'center' }}><Spinner /></div>}>
          <CorePanelMultiLazy
            title=""
            storageKey={`explore-panel-${panel.key}`}
            height={PANEL_HEIGHT}
            // TSMode ile CorePanel'in viz union'ı artık AYNI dört değer:
            // eşleme yok, düşürme yok — mark olduğu gibi geçer.
            viz={mode}
            unit={panel.unit || undefined}
            items={panel.series.map(ts => ({
              name: ts.label,
              role: 'data' as const,
              series: [{ groupKey: [], points: ts.points
                .filter(pt => pt.value != null)
                .map(pt => ({ time: pt.time, value: pt.value as number })) }],
              exemplars: ts.exemplars,
              // v0.9.793 — formül serisi KESİKLİ: "ölçülmedi, hesaplandı".
              // Panelin ƒ rozeti (yukarıda) zaten kesikli kenarlık kullanıyor;
              // çizgi o dili sürdürür. Lejantta değişiklik yok.
              dashed: panel.isFormula,
            }))}
            hiddenNames={hiddenLabels}
            focusedLabel={focusedLabel}
            hideLegend
            xRange={zoomWindow ?? xRange}
            regions={exploreRegions(panel)}
            thresholds={exploreThresholds(panel)}
            logScale={logScale}
            syncKey={SYNC_KEY_V2}
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
