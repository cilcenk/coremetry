import { memo } from 'react';
import { TimeSeriesPanel, type TSMode } from '@/components/viz/TimeSeriesPanel';
import { Spinner } from '@/components/Spinner';
import { publishCursor } from './cursorBus';
import type { PanelData } from './PanelStack';

// QueryPanel (explore-v2 Phase 2) — one query's chart card in the stack.
// Crosshair syncs across panels via uPlot.sync('explore-v2'); drag-zoom on
// any panel fans out through the parent's zoomWindow. React.memo per the
// plan's perf guards — only the touched panel re-renders on hover/zoom
// state changes that don't concern it.

const SYNC_KEY = 'explore-v2';
const PANEL_HEIGHT = 200;

export const QueryPanel = memo(function QueryPanel({
  panel, mode, hiddenLabels, focusedLabel, zoomWindow, onZoom, onExemplarClick,
}: {
  panel: PanelData;
  mode: TSMode;
  hiddenLabels: Set<string>;
  focusedLabel: string | null;
  zoomWindow: { from: number; to: number } | null;
  onZoom: (fromSec: number, toSec: number) => void;
  onExemplarClick?: (traceId: string) => void;
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
            {panel.unit ? ` · ${panel.unit}` : ''}
          </span>
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
      ) : (
        <TimeSeriesPanel
          series={panel.series}
          deploys={panel.deploys}
          thresholds={panel.thresholds}
          height={PANEL_HEIGHT}
          mode={mode}
          syncKey={SYNC_KEY}
          hideLegend
          zoomWindow={zoomWindow}
          hiddenLabels={hiddenLabels}
          focusedLabel={focusedLabel}
          onCursorTime={publishCursor}
          onExemplarClick={onExemplarClick}
          onZoom={onZoom} />
      )}
    </div>
  );
});
