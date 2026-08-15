import { lazy, Suspense, useMemo } from 'react';
import { Spinner } from '@/components/Spinner';
import { timeRangeToNs } from '@/lib/utils';
import type { ChartTimeRegion } from '@/lib/chart/overlays';
import type { SpanMetricSeries, TimeRange } from '@/lib/types';

// MetricTile — one of the /endpoint page's three RED series cards
// (v0.9.839; promoted verbatim from the retired sparkline modal in
// Endpoints.tsx, where it was added in v0.5.391 / v0.9.819). Its data
// comes from series.ts, which explains why there is no fetch here.
//
// v0.9.819 — chart engine is CorePanel, not the v1 uPlot body: two
// engines on one screen means two tooltips, two legends and — worse —
// two crosshair-sync namespaces (v0.9.789: the v1 body keeps x in
// SECONDS, CorePanel in MILLISECONDS; a mixed group puts the cursor
// 1000× off).
//
// LAZY: a static @grafana/* import would add ~1MB to this route's
// vendor chunk.
const CorePanelMultiLazy = lazy(() =>
  import('@/components/chart/corePanelEntry').then(m => ({ default: m.CorePanelMulti })));

export function MetricTile({
  label, big, sub, subCls, series, unit, role, storageKey, range,
  emptyLabel, onZoom, onZoomReset, regions,
}: {
  label: string; big: string; sub: string; subCls?: string;
  series: SpanMetricSeries[]; unit?: string;
  /** Role comes from the CALLER, never guessed from the label. */
  role?: 'data' | 'error' | 'success' | 'muted';
  storageKey: string;
  range?: TimeRange;
  /** Empty-state override — says WHY there is nothing to draw. */
  emptyLabel?: string;
  onZoom?: (fromUnixSec: number, toUnixSec: number) => void;
  onZoomReset?: () => void;
  // v0.9.1044 (Ş3 paritesi) — operatör olayları chart-İÇİ bölge olarak
  // gelir (sayfa TEK fetch atar, eventRegions çevirir). Eski DOM-overlay
  // EventMarkers söküldü: karo başına ayrı /api/events fetch'i (3 karo =
  // 3 istek) + grafiğin x-scale'inden habersiz mutlak-konum çizgiler.
  regions?: ChartTimeRegion[];
}) {
  const bounds = useMemo(() => {
    if (!range) return null;
    return timeRangeToNs(range);
  }, [range]);
  // The x axis is pinned to the QUERY window (v0.9.725): with sparse
  // data the axis would otherwise shrink to fit and the three tiles
  // would show three different time spans — at which point the synced
  // crosshair lies.
  const xRange = useMemo(
    () => (bounds ? { from: bounds.from / 1e9, to: bounds.to / 1e9 } : null),
    [bounds]);
  const hasData = series.length > 0 && series[0].points.length > 0;
  return (
    <div style={{
      padding: '10px 12px', border: '1px solid var(--border)',
      borderRadius: 6, background: 'var(--bg1)', minWidth: 0,
    }}>
      <div style={{ fontSize: 11, color: 'var(--text2)', marginBottom: 4 }}>{label}</div>
      <div style={{ fontSize: 22, fontWeight: 600, marginBottom: 2 }}>{big}</div>
      <div style={{
        fontSize: 11, marginBottom: 8,
        color: subCls === 'err' ? 'var(--err)' : subCls === 'warn' ? 'var(--warn)' : 'var(--text3)',
      }}>{sub}</div>
      <div style={{ position: 'relative' }}>
        <Suspense fallback={<div style={{ height: 140, display: 'grid', placeItems: 'center' }}><Spinner /></div>}>
          <CorePanelMultiLazy
            title={label}
            storageKey={`endpoint-detail-${storageKey}`}
            height={140}
            unit={unit}
            xRange={xRange}
            // The '-ms' suffix is the ENGINE NAMESPACE (v0.9.789).
            syncKey="endpoint-detail-ms"
            emptyReason={hasData ? undefined : (emptyLabel ?? 'Bu pencerede veri yok')}
            items={[{ name: label, role: role ?? 'data', series }]}
            regions={regions}
            onZoom={onZoom}
            onZoomReset={onZoomReset} />
        </Suspense>
      </div>
    </div>
  );
}
