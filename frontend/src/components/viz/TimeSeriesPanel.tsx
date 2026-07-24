import { useEffect, useMemo, useRef, useState } from 'react';
import uPlot from 'uplot';
import 'uplot/dist/uPlot.min.css';
import { downsampleXY } from '@/lib/perf/lttb';
import { fmtSmart, fmtXTicks, seriesColor } from '@/lib/chartFmt';
import { escapeHTML } from '@/lib/utils';
import { placeTooltip } from '@/lib/chartTooltip';
import { useThemeTick } from '@/lib/useThemeTick';
import { timeSeriesPanelBuildSignature } from '@/lib/chartBuildSig';
import { resolveVar as resolveColor } from '@/lib/chart/resolveVar';
import { xRangePinned, type XPin } from '@/lib/chart/xRange';
import { stepGapsRefiner, nearestFilledIdx } from '@/lib/chart/gapPolicy';
import { useChartEngine } from '@/lib/chart/engine';
import { buildCursorOpts } from '@/lib/chart/cursorOpts';
import { sortedTooltipRows } from '@/lib/chart/tooltipModel';
import { decidePinClick, applyPinStyle, clearPinStyle } from '@/lib/chart/tooltipPin';
import { toggleSeriesVisibility, isolateSeriesVisibility } from '@/lib/chart/legendVisibility';
import { drawThresholds, drawTimeRegions, type ChartTimeRegion } from '@/lib/chart/overlays';
import type { ChartAnnotation } from '@/lib/types';

// TimeSeriesPanel (v0.8 Phase 1A — Grafana-grade) — the single chart primitive
// the redesigned Metrics surfaces draw on. Built directly on uPlot (the only
// chart lib in the stack) and styled exclusively with CSS-variable tokens so it
// reads correctly in both themes.
//
// What it does that MultiLineChart didn't (and why it's a NEW component, not an
// edit to the shared one):
//   • dual y-axis (left/right) — drive a rate line + a latency line on one panel
//   • line / area / bars / stacked render modes
//   • SYNCHRONISED rich hover tooltip (timestamp + per-series swatch/label/value)
//   • cursor SYNC across panels via uPlot.sync(syncKey) — hover one, crosshair on all
//   • drag-to-zoom (uPlot select) + double-click reset
//   • threshold lines + tinted breach bands, per-threshold colour
//   • deploy ANNOTATIONS — dashed vline + ▼ flag drawn in a uPlot draw hook
//   • interactive legend TABLE — last/min/max/avg per series, click to isolate/toggle
//   • log scale (per-axis)
//   • EVERY series downsampled to ≤2000 points via downsampleXY BEFORE uPlot,
//     so a 50k-point series never blows the interaction budget.
//
// Points arrive in unix NANOSECONDS (matches the metric/spanmetric API shape);
// we convert to unix seconds for uPlot's time axis once, here.
//
// v0.9.131 (chart-consolidation Adım 3) — new uPlot / destroy / ResizeObserver /
// setData fast-path (v0.9.78-79 zoom-koruma) İSKELETİ engine.ts::useChartEngine'e
// çıkarıldı; bu bileşen artık en zengin PRESET. Kendine özgü opts (dual-axis,
// line/area/bars/stacked, exemplar ◆, event annotation, LTTB, cursorBus,
// controlled zoom/focus/hidden) preset'te kalır. Motorun afterBuild (kontrollü
// zoom/focus re-apply + exemplar-tık dinleyici), afterData (zoom re-apply) ve
// refitScales (dual+log-farkında y refit) kancaları burada tam sınanır.
// DAVRANIŞ birebir korunur (OVC/TC/MLC ile aynı seam).

const MAX_POINTS = 2000;

// v0.8.284 (A7) — annotation-line colour per operator-event kind. Token names
// so canvas strokes re-resolve on a theme flip (resolveColor reads the live
// CSS var). Mirrors the semantics of the legacy EventMarkers DOM overlay.
const ANNOTATION_KIND_TOKEN: Record<string, string> = {
  deploy: 'var(--ok)',
  config: 'var(--accent)',
  incident: 'var(--err)',
  maintenance: 'var(--warn)',
};
const ANNOTATION_DEFAULT_TOKEN = 'var(--text3)';

export interface TSSeries {
  label: string;
  points: { time: number /* ns */; value: number | null }[];
  color?: string;          // CSS colour override; falls back to stable seriesColor(label)
  unit?: string;           // per-series unit (drives the axis it's bound to)
  axis?: 'left' | 'right'; // which y-axis this series reads against (default 'left')
  dash?: number[];         // canvas dash pattern (explore-v2: the formula series)
  // v0.9.80 (uPlot Aşama 2 madde 1) — adım çizim: scrape gauge/counter
  // örnekler arası sabittir; düz çizgi olmayan geçiş uydurur. line/area
  // modunda uPlot.paths.stepped(align:1) ile değeri sonraki örneğe kadar
  // tutar. bars/stacked'te yok sayılır (zaten adım/katman).
  stepped?: boolean;
  // explore-v2 Phase 3.2 — exemplar trace markers (◆) anchored on this series.
  // time is unix NANOS (bucket start); value is the series value to pin the
  // glyph at; kind tints it (error = red, slow = accent; 'otlp' — v0.8.332
  // pivot Phase 3, a real OTLP exemplar, neutral --purple). Click opens the
  // trace.
  exemplars?: { time: number; value: number; traceId: string; kind: 'slow' | 'error' | 'otlp' }[];
}

export interface TSThreshold {
  value: number;
  label?: string;
  color?: string;          // CSS colour; default amber
}

export type TSMode = 'line' | 'area' | 'bars' | 'stacked';

interface TimeSeriesPanelProps {
  series: TSSeries[];
  deploys?: number[];          // unix ns — dashed vline + ▼ flag per deploy
  // v0.8.284 (A7) — operator-event annotation lines (deploy/config/incident/
  // maintenance/custom). Solid kind-coloured vline + top diamond, distinct from
  // the dashed auto-deploy ▼. Pre-windowed by annotationsInWindow; the draw hook
  // re-clamps to the live x-scale.
  events?: ChartAnnotation[];
  thresholds?: TSThreshold[];
  // Grafana-parite M3 — problem/anomali x-bölgeleri (arka-plan gölge + üst
  // şerit + etiket; lib/chart/overlays.ts). valToPos canlı ölçeği okur →
  // zoom'la doğru konumlanır.
  regions?: ChartTimeRegion[];
  height: number;
  mode?: TSMode;               // default 'line'
  logScale?: boolean;          // log10 on the y-axes
  syncKey?: string;            // shared cursor key across panels
  // Optional drag-zoom propagation. Receives unix SECONDS. When omitted the
  // drag still zooms visually (setScale) and double-click resets.
  onZoom?: (fromUnixSec: number, toUnixSec: number) => void;
  // onZoomReset (Grafana-parite M1) — çift-tık "bir adım geri" sahipliğini
  // sayfa katmanına devreder (Explore zoomWindow pop'u; Service/Dashboard
  // ?range= pop'u). VERİLMEZSE eski yerel davranış: tam veri aralığına dön.
  onZoomReset?: () => void;
  // Hide the built-in legend table (e.g. when the parent renders its own).
  hideLegend?: boolean;
  // ── explore-v2 controlled props — all applied rebuild-free via effects ──
  // Controlled x-window in unix SECONDS. The parent fans one panel's onZoom
  // out to every synced panel; null restores the full data range.
  zoomWindow?: { from: number; to: number } | null;
  // Controlled per-label visibility (a label in the set is hidden). When
  // provided, this is the source of truth — the built-in legend should be
  // hidden and toggling driven by the parent (GroupTable).
  hiddenLabels?: Set<string>;
  // Highlight one series (uPlot focus + alpha-dim of the rest); null clears.
  focusedLabel?: string | null;
  // Crosshair time channel — called from the setCursor hook with the cursor's
  // time in unix SECONDS (null when the cursor leaves). The explore page wires
  // the cursorBus through this so its GroupTable's @cursor column tracks the
  // hover; kept generic so this primitive stays decoupled from that page.
  onCursorTime?: (timeSec: number | null) => void;
  // explore-v2 Phase 3.2 — click an exemplar ◆ to open its trace. Receives the
  // exemplar's trace_id; the page navigates to the trace view.
  onExemplarClick?: (traceId: string) => void;
  // v0.9.83 (uPlot Aşama 2 madde 2) — x-eksenini sorgu penceresine sabitle
  // (unix sec); zoomWindow/drag isteği aynen geçer. Verilmezse eski davranış.
  xRange?: XPin | null;
  // v0.9.99 (operatör: runtime pod grafikleri) — pürüzsüz + kesintisiz
  // gösterim: line/area serilerde spline path (zigzag azalır) + spanGaps
  // (eksik bucket'lar köprülenir, kopukluk kalkar). Metrik "gerçek değer"
  // disiplini için VARSAYILAN kapalı; yalnız çağıran (RuntimeCharts) açar.
  smooth?: boolean;
}

// v0.9.75 (chart-consolidation Adım 0) — resolveColor lib/chart/
// resolveVar'a çıkarıldı (OVC/TC cssVar ile aynı regex + fallback);
// import dosya başında, `resolveColor` alias'ı ile kullanım aynı kaldı.

// Append an alpha byte to a #rrggbb colour for fills. Non-hex colours pass
// through unchanged (uPlot still strokes/fills, just opaque-ish — acceptable).
function withAlpha(hex: string, aa: string): string {
  return /^#[0-9a-fA-F]{6}$/.test(hex) ? hex + aa : hex;
}

// toHex (breach-band tint yardımcısı) Grafana-parite M3'te kalktı — threshold
// bandı artık lib/chart/overlays.ts::drawThresholds'ta (globalAlpha yolu).

// legendStat — per-series last/min/max/avg over the raw data so the numbers
// match the points the operator hovers. uPlot series index is 1-based.
interface LegendRow {
  idx: number;
  label: string;
  color: string;
  unit: string;
  last: number;
  min: number;
  max: number;
  avg: number;
}

export function TimeSeriesPanel({
  series, deploys, events, thresholds, regions, height, mode = 'line', logScale, syncKey, onZoom, onZoomReset, hideLegend, smooth,
  zoomWindow, hiddenLabels, focusedLabel, onCursorTime, onExemplarClick, xRange,
}: TimeSeriesPanelProps) {
  const containerRef = useRef<HTMLDivElement>(null);
  // Live per-series visibility, kept in a ref so legend clicks don't rebuild
  // the chart (setSeries(idx,{show}) is <1ms; React state would force a rebuild).
  const visibleRef = useRef<boolean[]>([]);
  // React mirror used ONLY to re-paint the legend's dim state. The CHART never
  // reads this — it reads visibleRef.
  const [visTick, setVisTick] = useState(0);
  // Latest controlled-prop values for the rebuild path: when the chart is
  // destroyed + recreated (data/mode change), the new uPlot must re-apply the
  // parent-controlled zoom/visibility/focus without those props being in the
  // rebuild dep list (that would force a full rebuild per zoom/hover).
  const zoomRef = useRef(zoomWindow);
  zoomRef.current = zoomWindow;
  const xRangeRef = useRef(xRange); xRangeRef.current = xRange;
  const hiddenRef = useRef(hiddenLabels);
  hiddenRef.current = hiddenLabels;
  const focusedRef = useRef(focusedLabel);
  focusedRef.current = focusedLabel;
  // Held in a ref so the cursor publish never enters the rebuild dep list —
  // a 60fps mousemove path must not rebuild the chart.
  const cursorTimeRef = useRef(onCursorTime);
  cursorTimeRef.current = onCursorTime;
  const exemplarClickRef = useRef(onExemplarClick);
  exemplarClickRef.current = onExemplarClick;
  // Latest series held in a ref (v0.8.531) so the once-per-build draw hook /
  // tooltip / exemplar-click listener read the CURRENT exemplars + raw values
  // without the chart rebuilding on a data-only poll. Series STRUCTURE (labels/
  // colours/axes/units) is captured by the build signature below, so a fresh
  // arrow of same-shape data rides setData; only exemplar/point VALUES flow live
  // through this ref.
  const seriesRef = useRef(series);
  seriesRef.current = series;
  // Theme tick — a data-theme flip must re-resolve the canvas CSS-var colours,
  // which only happens on a rebuild. So it's a BUILD dep (never the fast-path).
  // Pre-v0.8.531 the panel had no theme tick and only re-coloured when data
  // changed; with the fast-path a poll no longer rebuilds, so this is now
  // required for a theme toggle to repaint the strokes.
  const themeTick = useThemeTick();

  // ── Grafana-parite #2: tooltip pin ──────────────────────────────────────
  // pinRef: pinli veri index'i (null = pin yok). Tetik (TSP): çizim alanına
  // DÜZ TIK — exemplar ◆ isabeti ÖNCELİKLİ (trace açar, pin devreye girmez);
  // ikinci tık/Esc çözer. Dinleyici afterBuild'de (aşağıda), tek kopya.
  const pinRef = useRef<number | null>(null);
  const unpinTooltip = () => {
    pinRef.current = null;
    const tip = containerRef.current?.querySelector('.tsp-tooltip') as HTMLDivElement | null;
    if (tip) clearPinStyle(tip, 'opacity');
  };
  // Esc → pin çöz.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape' && pinRef.current != null) unpinTooltip(); };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, []); // unpinTooltip yalnız ref'lere dokunur — stable

  // Downsample each series to ≤2000 points BEFORE uPlot (gap-aware), then
  // re-align onto a union x grid. Memoised on series identity so we don't
  // re-decimate on unrelated renders (parents MUST memoise the series array).
  const prepared = useMemo(() => {
    const perSeries = series.map(s => {
      const xs = s.points.map(p => p.time / 1e9);
      const ys = s.points.map(p => p.value);
      const ds = downsampleXY(xs, ys, MAX_POINTS);
      return { xs: ds.xs, ys: ds.ys };
    });
    const allX = new Set<number>();
    perSeries.forEach(ps => ps.xs.forEach(x => allX.add(x)));
    const times = [...allX].sort((a, b) => a - b);
    const ySeries: (number | null)[][] = perSeries.map(ps => {
      const byT = new Map<number, number | null>();
      for (let i = 0; i < ps.xs.length; i++) byT.set(ps.xs[i], ps.ys[i]);
      return times.map(t => byT.get(t) ?? null);
    });
    return { times, ySeries };
  }, [series]);

  // Per-series stats for the legend, over raw (pre-downsample) values.
  const legendRows = useMemo<LegendRow[]>(() => series.map((s, i) => {
    const vs = s.points.map(p => p.value).filter((v): v is number => v != null && isFinite(v));
    const color = resolveColor(s.color ?? seriesColor(s.label));
    if (vs.length === 0) return { idx: i + 1, label: s.label, color, unit: s.unit ?? '', last: NaN, min: NaN, max: NaN, avg: NaN };
    return {
      idx: i + 1,
      label: s.label,
      color,
      unit: s.unit ?? '',
      last: vs[vs.length - 1],
      min: Math.min(...vs),
      max: Math.max(...vs),
      avg: vs.reduce((a, b) => a + b, 0) / vs.length,
    };
  }), [series]);

  const hasRight = useMemo(() => series.some(s => s.axis === 'right'), [series]);
  const leftUnit = useMemo(() => series.find(s => s.axis !== 'right')?.unit ?? '', [series]);
  const rightUnit = useMemo(() => series.find(s => s.axis === 'right')?.unit ?? '', [series]);
  const anyUnit = leftUnit || rightUnit;

  // Aligned uPlot data bundle (v0.8.531) — the single data-prep pass feeding BOTH
  // the build effect and the setData fast-path. Stacked mode plots cumulative
  // running sums; the raw ySeries is kept so the tooltip/legend read real layer
  // values (a null in a stacked layer counts as 0 so a gap doesn't punch
  // through). Keyed on the DATA inputs (prepared) + mode only, so a poll
  // recomputes once and either seeds a rebuild or rides setData.
  const bundle = useMemo(() => {
    const { times, ySeries } = prepared;
    let drawMatrix: (number | null)[][] = ySeries;
    if (mode === 'stacked') {
      const cum: (number | null)[][] = [];
      for (let i = 0; i < ySeries.length; i++) {
        const below = cum[i - 1];
        cum[i] = ySeries[i].map((v, j) => (below?.[j] ?? 0) + (v ?? 0));
      }
      drawMatrix = cum;
    }
    return { times, ySeries, data: [times, ...drawMatrix] as uPlot.AlignedData };
  }, [prepared, mode]);
  const bundleRef = useRef(bundle);
  bundleRef.current = bundle;

  // Build signature — the pure "rebuild vs setData" seam. Everything that, when
  // changed, forces a full re-create (series shape, axes, mode, overlays, zoom
  // presence). Point VALUES + exemplar positions ride setData/refs; `pointsTier`
  // buckets the point count so crossing the dot-visibility threshold rebuilds;
  // `hasExemplars` rebuilds a none→some transition so the draw/click hooks wire.
  const buildSig = timeSeriesPanelBuildSignature({
    series,
    mode, logScale, syncKey, hasZoom: !!onZoom, height,
    deploys, events, thresholds, regions,
    hasExemplars: series.some(s => (s.exemplars?.length ?? 0) > 0),
    pointsTier: prepared.times.length <= 100 ? 2 : prepared.times.length <= 300 ? 1 : 0,
    renderable: series.length > 0 && prepared.times.length > 0,
  });

  // buildOptions — renkleri REBUILD ANINDA çöz (motor tema flip'te yeniden
  // çağırır); eski build effect'in opts'unun birebiri. width motordan gelir.
  // el motor tarafından garanti (renderable && el varken çağrılır).
  const buildOptions = (width: number): uPlot.Options => {
    const el = containerRef.current!;

    // visibility'yi rebuild'de resetle (series `show` bunu okur) — controlled
    // hiddenLabels varsa ondan tohumla. Yalnız rebuild'de; setData'ya dokunmaz.
    visibleRef.current = series.map(s => !(hiddenRef.current?.has(s.label)));

    const css = getComputedStyle(document.documentElement);
    const text3 = css.getPropertyValue('--text3').trim() || '#484f58';
    const grid = css.getPropertyValue('--bg2').trim() || '#21262d';
    const bg1 = css.getPropertyValue('--bg1').trim() || '#0d1117';  // exemplar ◆ halo

    const stacked = mode === 'stacked';
    const { times } = bundleRef.current;

    const colors = series.map(s => resolveColor(s.color ?? seriesColor(s.label)));
    const yScaleKey = (s: TSSeries) => (s.axis === 'right' ? 'yr' : 'y');

    // Grafana-parite M1 — drag(+sync)+setSelect paylaşımlı buildCursorOpts'tan
    // (davranış birebir: x-only drag + setScale:true, 4px eşik; onZoom
    // buildOptions closure'ından — rebuild'de tazelenir, mevcut sözleşme).
    const cz = buildCursorOpts({ syncKey, onZoom });

    const opts: uPlot.Options = {
      width,
      height,
      // v0.9.93 (uPlot Aşama 3) — stacked bant dolguları arası saç-teli
      // beyaz çizgileri kaldırır (pxAlign:0 dolguları sürekli hizalar);
      // non-stacked'te crisp gridline için 1 (default).
      pxAlign: stacked ? 0 : 1,
      scales: {
        x: { time: true, range: (u, mn, mx) => xRangePinned(u.data[0] as number[], xRangeRef.current, mn, mx) },
        y: logScale ? { distr: 3, log: 10 } : {},
        ...(hasRight ? { yr: logScale ? { distr: 3, log: 10 } : {} } : {}),
      },
      series: [
        {},
        ...series.map((s, i): uPlot.Series => {
          const color = colors[i];
          const u = s.unit ?? anyUnit;
          const base: uPlot.Series = {
            label: s.label,
            stroke: color,
            scale: yScaleKey(s),
            width: 2,
            show: visibleRef.current[i],
            dash: s.dash,
            value: (_u: uPlot, v: number | null) => fmtSmart(v, u),
            points: {
              show: times.length <= 300,
              size: times.length <= 100 ? 5 : 3,
            },
          };
          if (mode === 'bars') {
            base.paths = uPlot.paths.bars ? uPlot.paths.bars({ size: [0.85, Infinity] }) : undefined;
            base.fill = withAlpha(color, 'cc');
            base.points = { show: false };
          } else if (mode === 'area') {
            base.fill = withAlpha(color, '22');
          } else if (stacked) {
            base.fill = i === 0 ? withAlpha(color, '47') : undefined;
            base.points = { show: false };
          }
          // v0.9.80 (Aşama 2 madde 1) — adım çizim: line/area modunda
          // stepped seri (scrape gauge/counter) değeri sonraki örneğe
          // kadar sabit tutar. bars/stacked kendi path'ini kullanır.
          // smooth istenmişse spline kazanır (aşağıda).
          if (s.stepped && mode !== 'bars' && !stacked && !smooth && uPlot.paths.stepped) {
            base.paths = uPlot.paths.stepped({ align: 1 });
          }
          if (mode !== 'bars' && !stacked) {
            if (smooth && uPlot.paths.spline) {
              // v0.9.99 (operatör: runtime pod grafikleri) — pürüzsüz +
              // kesintisiz: spline path zigzag'ı azaltır, spanGaps eksik
              // bucket'ları köprüler (kopukluk kalkar). Gap-refiner'ı ezer.
              base.paths = uPlot.paths.spline();
              base.spanGaps = true;
            } else {
              // v0.9.84 (madde 3) — tek kaçmış scrape köprülenir, gerçek
              // kesinti kırık kalır. bars/stacked'e dokunma.
              base.gaps = stepGapsRefiner;
            }
          }
          return base;
        }),
      ],
      // Stacked bands: fill between cumulative line k and k-1 (1-based).
      bands: stacked
        ? series.slice(1).map((_s, k) => ({ series: [k + 2, k + 1] as [number, number], fill: withAlpha(colors[k + 1], '47') }))
        : undefined,
      axes: [
        {
          stroke: text3,
          grid: { stroke: grid, width: 1 },
          ticks: { stroke: grid, width: 1 },
          values: (_u, splits) => fmtXTicks(splits),
        },
        {
          scale: 'y',
          stroke: text3,
          grid: { stroke: grid, width: 1 },
          ticks: { stroke: grid, width: 1 },
          values: (_u, splits) => splits.map(v => fmtSmart(v, leftUnit)),
          size: 56,
        },
        ...(hasRight ? [{
          scale: 'yr',
          side: 1,
          stroke: text3,
          grid: { show: false },
          ticks: { stroke: grid, width: 1 },
          values: (_u: uPlot, splits: number[]) => splits.map(v => fmtSmart(v, rightUnit)),
          size: 56,
        } satisfies uPlot.Axis] : []),
      ] as uPlot.Axis[],
      cursor: {
        x: true, y: true, focus: { prox: 15 },
        // v0.9.128 (operatör: "line'ların noktası hareket etmiyor") — imleçle
        // çizgi üstünde kayan belirgin hover noktası (OverviewChart/RPS emsali
        // size:7). Default cursor.points belirsizdi; açıkça göster.
        points: { show: true, size: 6 },
        // v0.9.84 (madde 4) — line/area modda hover en yakın DOLU örneğe
        // snap'ler (±2 bucket); bars/stacked kendi hizasında kalır.
        ...(mode === 'line' || mode === 'area' ? {
          dataIdx: (u: uPlot, sidx: number, idx: number) =>
            nearestFilledIdx(u.data[sidx] as (number | null)[], idx, 2),
        } : {}),
        // Grafana-parite M1 — drag+sync paylaşımlı buildCursorOpts'tan.
        ...cz.cursor,
      },
      legend: { show: false }, // our own interactive legend table renders below
      focus: { alpha: 0.35 }, // dim non-focused series (cursor prox + focusedLabel)
      // NOTE: uPlot's fire() does `if (evName in hooks)` — a key explicitly
      // set to undefined still passes the `in` check and crashes on
      // hooks[evName].forEach. Only set keys that actually have handlers.
      hooks: {
        // Drag-zoom → sayfaya devir; paylaşımlı cursorOpts hook'u (4px eşik,
        // sıralı sec aralığı, gri band temizliği). Only set when onZoom exists.
        ...(cz.setSelect ? { setSelect: cz.setSelect } : {}),
        // Overlay draw — regions (background-most), thresholds (line + breach
        // band), then deploy annotations.
        ...(((thresholds && thresholds.length > 0) || (regions && regions.length > 0) || (deploys && deploys.length > 0) || (events && events.length > 0) || series.some(s => s.exemplars && s.exemplars.length > 0)) ? { draw: [
          (u) => {
            const ctx = u.ctx;
            ctx.save();

            // Grafana-parite M3 — problem/anomali bölgeleri en arkada
            // (gölge, üstüne threshold/deploy/exemplar biner).
            if (regions && regions.length > 0) drawTimeRegions(u, regions);

            // Grafana-parite M3 — eski yerel threshold bloğu (MLC ile birebir
            // kopyaydı) paylaşımlı drawThresholds'a delege. Renk çözümü + '14'
            // hex-alfa bandının alfa eşdeğeri (0x14/255) aynen korunur.
            if (thresholds && thresholds.length > 0) {
              drawThresholds(u, thresholds.map(th => ({
                value: th.value,
                label: th.label,
                color: resolveColor(th.color ?? 'var(--warn)') || '#f0b352',
              })), { bandAlpha: 0x14 / 255 });
            }

            if (deploys && deploys.length > 0) {
              const xMin = u.scales.x.min ?? 0;
              const xMax = u.scales.x.max ?? 0;
              const purple = resolveColor('var(--purple)') || '#a371f7';
              ctx.strokeStyle = purple;
              ctx.fillStyle = purple;
              ctx.lineWidth = 1.2;
              ctx.font = '10px ui-monospace, monospace';
              for (const dNs of deploys) {
                const t = dNs / 1e9;
                if (t < xMin || t > xMax) continue;
                const x = u.valToPos(t, 'x', true);
                ctx.setLineDash([5, 4]);
                ctx.beginPath();
                ctx.moveTo(x, u.bbox.top);
                ctx.lineTo(x, u.bbox.top + u.bbox.height);
                ctx.stroke();
                ctx.setLineDash([]);
                ctx.fillText('▼', x - 3, u.bbox.top + 9);
              }
            }

            // v0.8.284 (A7) — operator-event annotation lines. Solid vline in the
            // kind colour + a small top diamond, distinct from the dashed auto-
            // deploy ▼. Re-clamped to the live x-scale so a drag-zoom hides
            // out-of-window markers.
            if (events && events.length > 0) {
              const xMin = u.scales.x.min ?? 0;
              const xMax = u.scales.x.max ?? 0;
              for (const ann of events) {
                const t = ann.timeUnixNs / 1e9;
                if (t < xMin || t > xMax) continue;
                const col = resolveColor(ANNOTATION_KIND_TOKEN[ann.kind] ?? ANNOTATION_DEFAULT_TOKEN)
                  || '#8b949e';
                const x = u.valToPos(t, 'x', true);
                ctx.strokeStyle = col;
                ctx.fillStyle = col;
                ctx.lineWidth = 1.2;
                ctx.setLineDash([]);
                ctx.beginPath();
                ctx.moveTo(x, u.bbox.top);
                ctx.lineTo(x, u.bbox.top + u.bbox.height);
                ctx.stroke();
                // top diamond
                ctx.beginPath();
                ctx.moveTo(x, u.bbox.top);
                ctx.lineTo(x + 3, u.bbox.top + 3);
                ctx.lineTo(x, u.bbox.top + 6);
                ctx.lineTo(x - 3, u.bbox.top + 3);
                ctx.closePath();
                ctx.fill();
              }
            }

            // explore-v2 Phase 3.2 — exemplar ◆ markers, drawn last so they sit
            // on top of the lines. One per (visible series, bucket-with-a-trace);
            // a thin halo in the panel bg keeps them legible over same-coloured
            // lines. error = --err, slow = --accent2, otlp = --purple (v0.8.332).
            const exMinX = u.scales.x.min ?? 0;
            const exMaxX = u.scales.x.max ?? 0;
            // Read exemplars LIVE from the ref — the setData fast-path swaps them
            // without rebuilding this hook; series COUNT/axes are structural.
            const drawSeries = seriesRef.current;
            for (let si = 0; si < drawSeries.length; si++) {
              const exs = drawSeries[si].exemplars;
              if (!exs || exs.length === 0 || !visibleRef.current[si]) continue;
              const sk = drawSeries[si].axis === 'right' ? 'yr' : 'y';
              for (const ex of exs) {
                const t = ex.time / 1e9;
                if (t < exMinX || t > exMaxX) continue;
                const x = u.valToPos(t, 'x', true);
                const y = u.valToPos(ex.value, sk, true);
                const col = resolveColor(
                  ex.kind === 'error' ? 'var(--err)'
                    : ex.kind === 'otlp' ? 'var(--purple)'
                    : 'var(--accent2)',
                ) || (ex.kind === 'error' ? '#ef4444' : '#a371f7');
                ctx.beginPath();
                ctx.moveTo(x, y - 4);
                ctx.lineTo(x + 4, y);
                ctx.lineTo(x, y + 4);
                ctx.lineTo(x - 4, y);
                ctx.closePath();
                ctx.fillStyle = col;
                ctx.strokeStyle = bg1;
                ctx.lineWidth = 1;
                ctx.fill();
                ctx.stroke();
              }
            }
            ctx.restore();
          },
        ] } : {}),
        // SYNCHRONISED rich tooltip + y-axis crosshair pill.
        setCursor: [
          (u) => {
            const tip = el.querySelector('.tsp-tooltip') as HTMLDivElement | null;
            const yPill = el.querySelector('.tsp-ypill') as HTMLDivElement | null;
            const cTop = u.cursor.top ?? -1;
            if (yPill) {
              if (cTop < u.bbox.top || cTop > u.bbox.top + u.bbox.height) {
                yPill.style.opacity = '0';
              } else {
                const yVal = u.posToVal(cTop, 'y');
                if (isFinite(yVal)) {
                  yPill.textContent = fmtSmart(yVal, leftUnit);
                  yPill.style.top = `${cTop - 8}px`;
                  yPill.style.opacity = '1';
                } else yPill.style.opacity = '0';
              }
            }
            const idx = u.cursor.idx;
            // Publish the crosshair time (unix sec) to whoever wired the bus.
            // Done BEFORE the tooltip early-returns so a cursor-leave (idx null)
            // clears the channel too. Synced panels all fire the same time —
            // the bus dedupes them to one notification per frame.
            if (cursorTimeRef.current) {
              const cx = idx == null || idx < 0 ? null : (u.data[0][idx] as number | null);
              cursorTimeRef.current(cx != null && isFinite(cx) ? cx : null);
            }
            if (!tip) return;
            // Grafana-parite #2 — pinliyken tooltip donuk: içerik/pozisyon
            // dokunulmaz (cursorTime kanalı + yPill + crosshair yaşar —
            // Explore GroupTable sürücüsü etkilenmez).
            if (pinRef.current != null) return;
            if (idx == null || idx < 0) { tip.style.opacity = '0'; return; }
            const xVal = u.data[0][idx];
            if (xVal == null) { tip.style.opacity = '0'; return; }
            const d = new Date((xVal as number) * 1000);
            const hh = d.getHours().toString().padStart(2, '0');
            const mm = d.getMinutes().toString().padStart(2, '0');
            const ss = d.getSeconds().toString().padStart(2, '0');

            // Per-series rows from RAW values (stacked panels show real layer
            // value, not cumulative). Grafana-parite #2 — sıralama + format
            // paylaşımlı sortedTooltipRows'ta (null-drop + değer DESC stable
            // + fmtSmart seri-birimi); gizli seri value:null ile satırdan
            // düşer. Davranış birebir eski yerel kopya.
            // Stacked draws cumulative into u.data, so read the RAW layer value
            // from the live bundle ref (fresh on the fast-path); non-stacked is
            // raw already, so read it straight off u.data. Labels/colours/units
            // are structural (rebuild on change) — safe to close over.
            const rawY = bundleRef.current.ySeries;
            const rows = sortedTooltipRows(series.map((s, i) => {
              // v0.9.84 (madde 4) — dataIdx snap'i seri başına idxs'te; tooltip
              // aynı dolu örneği okur (seyrek seri artık "—" değil).
              const si = u.cursor.idxs?.[i + 1] ?? idx;
              const v = !visibleRef.current[i] ? null
                : stacked ? rawY[i]?.[idx]
                : (u.data[i + 1] as (number | null)[])?.[si];
              return { label: s.label, color: colors[i], value: v, unit: s.unit ?? anyUnit };
            }));
            if (rows.length === 0) { tip.style.opacity = '0'; return; }

            let deployRow = '';
            if (deploys && deploys.length > 0) {
              const cursorX = u.cursor.left ?? -1;
              let nearestNs: number | null = null;
              let bestDx = 12;
              for (const dNs of deploys) {
                const px = u.valToPos(dNs / 1e9, 'x', true);
                const dx = Math.abs(px - cursorX);
                if (dx < bestDx) { bestDx = dx; nearestNs = dNs; }
              }
              if (nearestNs != null) {
                deployRow =
                  `<div style="display:flex;gap:8px;align-items:center;line-height:1.5;margin-bottom:4px;padding-bottom:4px;border-bottom:1px solid var(--border)">` +
                    `<span style="display:inline-block;width:8px;height:8px;background:var(--purple,#a371f7);border-radius:2px;flex-shrink:0"></span>` +
                    `<span style="flex:1">deploy</span>` +
                  `</div>`;
              }
            }

            // v0.8.284 (A7) — nearest operator-event annotation within 12px.
            let eventRow = '';
            if (events && events.length > 0) {
              const cursorX = u.cursor.left ?? -1;
              let near: ChartAnnotation | null = null;
              let bestDx = 12;
              for (const ann of events) {
                const px = u.valToPos(ann.timeUnixNs / 1e9, 'x', true);
                const dx = Math.abs(px - cursorX);
                if (dx < bestDx) { bestDx = dx; near = ann; }
              }
              if (near) {
                const col = ANNOTATION_KIND_TOKEN[near.kind] ?? ANNOTATION_DEFAULT_TOKEN;
                const txt = near.label ? `${near.kind} · ${near.label}` : near.kind;
                eventRow =
                  `<div style="display:flex;gap:8px;align-items:center;line-height:1.5;margin-bottom:4px;padding-bottom:4px;border-bottom:1px solid var(--border)">` +
                    `<span style="display:inline-block;width:8px;height:8px;background:${col};transform:rotate(45deg);flex-shrink:0"></span>` +
                    `<span style="flex:1;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;max-width:240px" title="${escapeHTML(txt)}">${escapeHTML(txt)}</span>` +
                  `</div>`;
              }
            }

            // explore-v2 Phase 3.2 — if the cursor is near an exemplar ◆ of a
            // visible series, surface it (kind + short trace id). Same over-
            // relative px basis as cursor.left/top (valToPos w/o canvasPixels).
            let exemplarRow = '';
            {
              const cx = u.cursor.left ?? -1, cy = u.cursor.top ?? -1;
              let near: { kind: string; traceId: string } | null = null;
              let bd = 196; // 14px squared
              const hitSeries = seriesRef.current;
              for (let i = 0; i < hitSeries.length; i++) {
                const exs = hitSeries[i].exemplars;
                if (!exs || !visibleRef.current[i]) continue;
                const sk = hitSeries[i].axis === 'right' ? 'yr' : 'y';
                for (const ex of exs) {
                  const px = u.valToPos(ex.time / 1e9, 'x');
                  const py = u.valToPos(ex.value, sk);
                  const d = (px - cx) ** 2 + (py - cy) ** 2;
                  if (d < bd) { bd = d; near = { kind: ex.kind, traceId: ex.traceId }; }
                }
              }
              if (near) {
                const c = near.kind === 'error' ? 'var(--err)'
                  : near.kind === 'otlp' ? 'var(--purple)'
                  : 'var(--accent2)';
                exemplarRow =
                  `<div style="display:flex;gap:8px;align-items:center;line-height:1.5;margin-bottom:4px;padding-bottom:4px;border-bottom:1px solid var(--border)">` +
                    `<span style="color:${c}">◆</span>` +
                    `<span style="flex:1">${escapeHTML(near.kind)} trace ${escapeHTML(near.traceId.slice(0, 8))}… · tıkla→aç</span>` +
                  `</div>`;
              }
            }

            tip.innerHTML =
              `<div style="font-weight:600;margin-bottom:4px">${hh}:${mm}:${ss}</div>` +
              exemplarRow +
              deployRow +
              eventRow +
              rows.map(r => {
                const lbl = escapeHTML(r.label);
                return `<div style="display:flex;gap:8px;align-items:center;line-height:1.5">` +
                  `<span style="display:inline-block;width:8px;height:8px;background:${r.color};border-radius:2px;flex-shrink:0"></span>` +
                  `<span style="flex:1;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;max-width:240px" title="${lbl}">${lbl}</span>` +
                  `<span style="font-family:ui-monospace,monospace;font-variant-numeric:tabular-nums">${r.text}</span>` +
                `</div>`;
              }).join('');
            tip.style.opacity = '1';
            // cursor.left/top are over-relative; pass the over box's offset +
            // size + the container box so placement and clamping share one
            // basis and the panel never lands under the pointer (esp. near the
            // y-axis, where the over box is inset by the axis width).
            const over = u.over;
            const { x, y } = placeTooltip(
              u.cursor.left ?? 0, u.cursor.top ?? 0,
              tip.offsetWidth, tip.offsetHeight,
              over.clientWidth, over.clientHeight,
              over.offsetLeft, over.offsetTop,
              el.clientWidth, el.clientHeight,
            );
            tip.style.left = `${x}px`;
            tip.style.top = `${y}px`;
          },
        ],
      },
    };

    return opts;
  };

  // afterBuild (motor: new uPlot SONRASI) — kontrollü zoom + focus'u taze
  // uPlot'a yeniden uygula (controlled-prop effect'leri yalnız PROP değişince
  // ateşler) + exemplar ◆ tıkla→trace dinleyicisini over'a bağla. Cleanup
  // gerekmez: motor rebuild/unmount'ta u.destroy() over'ı DOM'dan kaldırır,
  // dinleyici GC'lenir (eski build-effect'in explicit removeEventListener'ının
  // yerine — tek instance başına tek dinleyici).
  const afterBuild = (u: uPlot) => {
    // Grafana-parite #2 — rebuild: pin çözülür (tooltip DOM'u yaşar ama
    // içerik bayatlardı; dinleyici aşağıda taze over'a bağlanır).
    if (pinRef.current != null) unpinTooltip();
    if (zoomRef.current) {
      u.setScale('x', { min: zoomRef.current.from, max: zoomRef.current.to });
    }
    if (focusedRef.current != null) {
      const fi = series.findIndex(s => s.label === focusedRef.current);
      if (fi >= 0) u.setSeries(fi + 1, { focus: true });
    }
    // explore-v2 Phase 3.2 — hit-test over-relative px (valToPos without
    // canvasPixels = over element's offset basis), so the click lines up.
    const over = u.over;
    let downX: number | null = null;
    over.addEventListener('mousedown', ev => { downX = ev.clientX; });
    // Grafana-parite #2 — çift-tık (zoom-geri) pin'i deterministik çözer:
    // zoom-geri sonrası pinli bayat tooltip kalamaz, jitter'lı re-pin
    // senaryosu kapanır (review 8/8 #3).
    over.addEventListener('dblclick', () => unpinTooltip());
    over.addEventListener('click', (ev: MouseEvent) => {
      // 1) exemplar ◆ ÖNCELİKLİ — mevcut davranış birebir (isabet → trace aç).
      const cb = exemplarClickRef.current;
      if (cb) {
        let hitId: string | null = null;
        let bestD = 100; // 10px squared tolerance
        const clickSeries = seriesRef.current;
        for (let si = 0; si < clickSeries.length; si++) {
          const exs = clickSeries[si].exemplars;
          if (!exs || !visibleRef.current[si]) continue;
          const sk = clickSeries[si].axis === 'right' ? 'yr' : 'y';
          for (const ex of exs) {
            const px = u.valToPos(ex.time / 1e9, 'x');
            const py = u.valToPos(ex.value, sk);
            const d = (px - ev.offsetX) ** 2 + (py - ev.offsetY) ** 2;
            if (d < bestD) { bestD = d; hitId = ex.traceId; }
          }
        }
        if (hitId) { cb(hitId); return; }
      }
      // 2) Grafana-parite #2 — düz tık tooltip'i PIN'ler / ikinci tık çözer.
      // mousedown→click mesafesi drag-zoom kuyruğunu eler (u.select,
      // setSelect hook'unda sıfırlandığından güvenilmez); detail çift-tık
      // click'lerini eler (yukarıdaki dblclick dinleyicisi pin'i çözer).
      const tip = containerRef.current?.querySelector('.tsp-tooltip') as HTMLDivElement | null;
      if (!tip) return;
      const d = decidePinClick({
        pinnedIdx: pinRef.current, cursorIdx: u.cursor.idx,
        dragPx: downX == null ? 0 : Math.abs(ev.clientX - downX),
        detail: ev.detail,
      });
      if (d.action === 'unpin') unpinTooltip();
      else if (d.action === 'pin' && tip.style.opacity === '1') { pinRef.current = d.idx; applyPinStyle(tip); }
    });
  };

  // afterData (motor: setData fast-path SONRASI) — kontrollü zoom penceresini
  // yeniden uygula (audit §5 risk #2: setData x'i full-range'e resetler, 30s
  // poll operatörü drag-zoom'dan atmasın). Motor zoomlu dalda x'i zaten korur;
  // bu, controlled window için idempotent güvence + isXZoomed'in kaçırdığı
  // full-range'e-eşit pencere kenar durumunu kapatır.
  const afterData = (u: uPlot) => {
    if (zoomRef.current) {
      u.setScale('x', { min: zoomRef.current.from, max: zoomRef.current.to });
    }
  };

  // refitScales (motor: zoomlu fast-path'te y'yi elle refit — setData(false)
  // y'yi resetlemez). TSP y-ekseni {} (uPlot-auto) / log + dual (y/yr); motorun
  // default [0,max*1.1] refit'i bunu ÜRETMEZ ve log'da min:0 geçersizdir. Bu
  // yüzden her eksenin uPlot-init'te normalize ettiği KENDİ range fonksiyonunu,
  // görünür serilerin veri min/max'ıyla çağırıp setData(true)'nun auto
  // davranışını BİREBİR üretiriz (linear + log, gizli seri hariç — uPlot da
  // gizliyi range'e katmaz).
  const refitScales = (u: uPlot, data: uPlot.AlignedData) => {
    const refit = (key: string, idxs: number[]) => {
      let dmin = Infinity, dmax = -Infinity;
      for (const si of idxs) {
        if (!visibleRef.current[si - 1]) continue;
        const col = data[si] as (number | null)[] | undefined;
        if (!col) continue;
        for (const v of col) {
          if (v != null && isFinite(v)) { if (v < dmin) dmin = v; if (v > dmax) dmax = v; }
        }
      }
      if (dmin === Infinity) return; // görünür veri yok → dokunma (uPlot da öyle)
      const rf = (u.scales[key] as { range?: unknown } | undefined)?.range;
      if (typeof rf === 'function') {
        const [lo, hi] = (rf as (u: uPlot, mn: number, mx: number, k: string) => [number, number])(u, dmin, dmax, key);
        u.setScale(key, { min: lo, max: hi });
      }
    };
    const leftIdxs: number[] = [];
    const rightIdxs: number[] = [];
    series.forEach((s, i) => (s.axis === 'right' ? rightIdxs : leftIdxs).push(i + 1));
    refit('y', leftIdxs);
    if (hasRight) refit('yr', rightIdxs);
  };

  // Yaşam döngüsü motorda (v0.9.131) — new uPlot / destroy / ResizeObserver /
  // setData fast-path (zoom-koruma). buildOptions/afterBuild/afterData/
  // refitScales specRef'ten canlı okunur; tetikleyici yalnız buildSig+themeTick
  // (rebuild) ve bundle.data (fast-path).
  const plotRef = useChartEngine(containerRef, {
    signature: buildSig,
    height,
    renderable: series.length > 0 && prepared.times.length > 0,
    data: bundle.data,
    buildOptions,
    afterBuild,
    afterData,
    refitScales,
    // Çift-tık (Grafana-parite M1) — dinleyici artık MOTORDA (tek kopya,
    // 4 preset ortak). Sahiplik: sayfa geri-yığını verdiyse (onZoomReset)
    // karar onun — Explore zoomWindow pop'u / Service-Dashboard ?range=
    // pop'u; verilmediyse ESKİ yerel davranış birebir: tam veri aralığına
    // dön (v0.8 Phase 1A "double-click reset"). Spec canlı okunur.
    onZoomReset: () => {
      if (onZoomReset) { onZoomReset(); return; }
      const u = plotRef.current;
      if (!u) return;
      const xs = u.data[0];
      if (!xs || xs.length === 0) return;
      u.setScale('x', { min: xs[0] as number, max: xs[xs.length - 1] as number });
    },
  }, themeTick);

  // ── explore-v2 controlled props — applied to the LIVE uPlot, no rebuild ──

  // Controlled x-window. setScale is the same call drag-zoom makes, so
  // applying the parent's window to the originating panel is idempotent.
  useEffect(() => {
    const u = plotRef.current;
    if (!u) return;
    if (zoomWindow) {
      u.setScale('x', { min: zoomWindow.from, max: zoomWindow.to });
    } else {
      const xs = u.data[0];
      if (xs && xs.length) u.setScale('x', { min: xs[0] as number, max: xs[xs.length - 1] as number });
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [zoomWindow]);

  // Controlled visibility — diff against visibleRef so we only touch series
  // whose state actually flipped (setSeries(show) redraws once per call).
  useEffect(() => {
    const u = plotRef.current;
    if (!u || !hiddenLabels) return;
    series.forEach((s, i) => {
      const show = !hiddenLabels.has(s.label);
      if (visibleRef.current[i] !== show) {
        visibleRef.current[i] = show;
        u.setSeries(i + 1, { show });
      }
    });
    setVisTick(t => t + 1);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hiddenLabels, series]);

  // Controlled focus — uPlot's focus.alpha dims everything else.
  useEffect(() => {
    const u = plotRef.current;
    if (!u) return;
    if (focusedLabel == null) {
      u.setSeries(null, { focus: false });
      return;
    }
    const i = series.findIndex(s => s.label === focusedLabel);
    if (i >= 0) u.setSeries(i + 1, { focus: true });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [focusedLabel, series]);

  // Legend interaction — isolate-on-click (second click restores all);
  // Ctrl/Cmd-click toggles only that series. Bypasses React state for the
  // chart; bumps visTick so the legend re-paints its dim state.
  // Grafana-parite #2 — isolate/toggle kararı paylaşımlı legendVisibility
  // çekirdeğinde (aynı semantik + "hepsi gizliyse hepsini geri getir" kuralı
  // additive uçta); uygulama eskisi gibi setSeries (fark eden seriler),
  // rebuild yok.
  const toggleSeries = (dataIdx0: number, additive: boolean) => {
    const u = plotRef.current;
    if (!u) return;
    const next = additive
      ? toggleSeriesVisibility(visibleRef.current, dataIdx0)
      : isolateSeriesVisibility(visibleRef.current, dataIdx0);
    next.forEach((show, i) => {
      if (visibleRef.current[i] !== show) { visibleRef.current[i] = show; u.setSeries(i + 1, { show }); }
    });
    setVisTick(t => t + 1);
  };

  return (
    <div>
      <div ref={containerRef} style={{ position: 'relative', width: '100%' }}>
        <div className="tsp-tooltip" style={{
          position: 'absolute', pointerEvents: 'none',
          background: 'var(--bg2)', border: '1px solid var(--border)', borderRadius: 4,
          padding: '8px 10px', fontSize: 11, color: 'var(--text)',
          opacity: 0, transition: 'opacity .08s', zIndex: 5,
          boxShadow: '0 4px 14px rgba(0,0,0,0.35)', maxWidth: 320,
        }} />
        <div className="tsp-ypill" style={{
          position: 'absolute', pointerEvents: 'none', left: 4, transform: 'translateY(-50%)',
          background: 'var(--bg3)', border: '1px solid var(--border)', borderRadius: 3,
          padding: '1px 5px', fontSize: 10, color: 'var(--text)',
          fontFamily: 'ui-monospace, monospace', opacity: 0, transition: 'opacity .08s',
          zIndex: 6, whiteSpace: 'nowrap',
        }} />
      </div>
      {!hideLegend && legendRows.length > 0 && (
        <TimeSeriesLegend rows={legendRows} visTick={visTick}
          isVisible={i => visibleRef.current[i] ?? true}
          onToggle={(i, additive) => toggleSeries(i, additive)} />
      )}
    </div>
  );
}

// ── Interactive legend table ────────────────────────────────────────────────
// v0.9.99 (operatör: çok-pod'lu servisler) — >8 seride lejand VARSAYILAN
// KAPALI (30+ pod'da sayfa aşırı uzuyordu); tıkla-aç başlık.
// v0.9.111 (operatör talebi 2026-07-20) — series lejantı HER ZAMAN default
// kapalı; isteyen ▶ ile açar. JVM/GC pod-bazlı grafiklerde çok seri var,
// lejant sayfayı uzatıyordu; default-collapsed ile grafikler kompakt kalır.
function TimeSeriesLegend({ rows, isVisible, onToggle }: {
  rows: LegendRow[];
  visTick: number;                                  // re-render trigger only
  isVisible: (dataIdx0: number) => boolean;
  onToggle: (dataIdx0: number, additive: boolean) => void;
}) {
  const [collapsed, setCollapsed] = useState(true);
  return (
    <div style={{ marginTop: 8 }}>
      <button type="button" onClick={() => setCollapsed(c => !c)}
        style={{
          background: 'none', border: 'none', cursor: 'pointer', padding: '2px 4px',
          fontSize: 11, color: 'var(--text2)', display: 'inline-flex', alignItems: 'center', gap: 4,
        }}
        title={collapsed ? 'Show series legend' : 'Hide series legend'}>
        <span style={{ fontSize: 9 }}>{collapsed ? '▶' : '▼'}</span>
        Series ({rows.length})
      </button>
      {!collapsed && (
      <div style={{ overflowX: 'auto', marginTop: 4 }}>
      <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 11 }}>
        <thead>
          <tr style={{ color: 'var(--text3)', textAlign: 'right' }}>
            <th style={{ textAlign: 'left', fontWeight: 500, padding: '2px 6px' }}>Series</th>
            <th style={{ fontWeight: 500, padding: '2px 6px' }}>Last</th>
            <th style={{ fontWeight: 500, padding: '2px 6px' }}>Min</th>
            <th style={{ fontWeight: 500, padding: '2px 6px' }}>Max</th>
            <th style={{ fontWeight: 500, padding: '2px 6px' }}>Avg</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((r, i) => {
            const on = isVisible(i);
            return (
              <tr key={r.label + i}
                onClick={e => onToggle(i, e.ctrlKey || e.metaKey)}
                style={{ cursor: 'pointer', opacity: on ? 1 : 0.4, borderTop: '1px solid var(--border)' }}
                title="Click to isolate this series · Ctrl/Cmd-click to toggle">
                <td style={{ padding: '3px 6px', maxWidth: 280, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                  <span style={{ display: 'inline-block', width: 8, height: 8, borderRadius: 2, background: r.color, marginRight: 6, verticalAlign: 'middle' }} />
                  <span style={{ verticalAlign: 'middle' }}>{r.label}</span>
                </td>
                <td className="mono" style={{ textAlign: 'right', padding: '3px 6px', fontVariantNumeric: 'tabular-nums' }}>{fmtSmart(r.last, r.unit)}</td>
                <td className="mono" style={{ textAlign: 'right', padding: '3px 6px', color: 'var(--text2)', fontVariantNumeric: 'tabular-nums' }}>{fmtSmart(r.min, r.unit)}</td>
                <td className="mono" style={{ textAlign: 'right', padding: '3px 6px', color: 'var(--text2)', fontVariantNumeric: 'tabular-nums' }}>{fmtSmart(r.max, r.unit)}</td>
                <td className="mono" style={{ textAlign: 'right', padding: '3px 6px', color: 'var(--text2)', fontVariantNumeric: 'tabular-nums' }}>{fmtSmart(r.avg, r.unit)}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
      </div>
      )}
    </div>
  );
}
