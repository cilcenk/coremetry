import { useEffect, useMemo, useRef } from 'react';
import uPlot from 'uplot';
import 'uplot/dist/uPlot.min.css';
import { escapeHTML } from '@/lib/utils';
import type { SpanMetricSeries } from '@/lib/types';
import { fmtSmart, fmtXTicks, seriesColor } from '@/lib/chartFmt';
import { placeTooltip } from '@/lib/chartTooltip';
import { useThemeTick } from '@/lib/useThemeTick';
import { chartBuildSignature } from '@/lib/chartBuildSig';
import { useChartEngine } from '@/lib/chart/engine';
import { buildCursorOpts } from '@/lib/chart/cursorOpts';
import { xRangePinned, type XPin } from '@/lib/chart/xRange';
import { yRefitScale } from '@/lib/chart/zoomState';
import { stepGapsRefiner, nearestFilledIdx } from '@/lib/chart/gapPolicy';
import { isAdditiveUnit } from '@/lib/chart/legendStats';
import { sortedTooltipRows } from '@/lib/chart/tooltipModel';
import { decidePinClick, applyPinStyle, clearPinStyle } from '@/lib/chart/tooltipPin';
import { toggleSeriesVisibility, isolateSeriesVisibility, resetSeriesVisibility } from '@/lib/chart/legendVisibility';
import { drawThresholds, drawTimeRegions, type ChartTimeRegion } from '@/lib/chart/overlays';

// v0.9.131 (chart-consolidation Adım 3) — TimeSeriesPanel artık placeTooltip'i
// doğrudan lib/chartTooltip'ten alıyor (MLC↔TSP bağımlılığı koptu, audit §4);
// buradaki re-export kaldırıldı.

// Multi-series line chart on uPlot. Renders the same hover-
// crosshair + per-series tooltip experience as the previous
// Chart.js version, but at 5-10× the speed.
//
// Three load-bearing decisions worth knowing:
//
//   1. `scales.x.time = true` — flips the x-axis from raw
//      numbers to "HH:MM" / "MMM DD" tick formatting. Without
//      this the axis renders 1715120400-style unix seconds,
//      which is what surfaced as "broken charts" after the
//      Chart.js migration.
//
//   2. Fixed pixel height (`height` prop, default 320). The
//      previous "height: 100%" container put uPlot at 0px on
//      pages where the parent wasn't itself sized. Callers
//      that want a custom height pass it explicitly; the
//      dashboard PanelRenderer wraps in a 280px box, the
//      Explore/Metrics pages pick 320px etc.
//
//   3. Click-to-isolate is implemented WITHOUT rebuilding the
//      chart. The previous version tracked hidden indices in
//      React state and put them in the useEffect deps, so
//      every legend click destroyed and recreated the plot —
//      ~30 ms judder per click. Now a click calls
//      setSeries(idx, { show }) on the live plot, no React
//      state, no rebuild.

// DeployMarker — lightweight shape the chart consumes for the
// dashed vertical "deploy line" overlay. Avoids leaking the
// fuller Deploy type into chart code so non-service callers
// (eg. Dashboards) can synthesise markers from any source.
export interface DeployMarker {
  timeUnixNs: number;
  label: string;        // service.version or whatever the caller wants
  description?: string; // tooltip detail (e.g. "153 spans since")
}

// Threshold — horizontal line at a y-value, optionally coloured
// by severity. Used for SLO targets, alert rule thresholds,
// "this is the limit" indicators. Same idea as Grafana's "alert
// threshold" overlay or Datadog's "warning / critical bands".
export interface Threshold {
  value: number;
  label?: string;            // e.g. "SLO 500ms"
  severity?: 'warn' | 'err'; // default 'warn'
}

// foldTopN — when more than N series would render, keep the N with
// the largest area (Σ|value|) and fold the long tail into a single
// muted "others" line so the legend + palette stay legible. Additive
// units (rps / counts / bytes) SUM the tail; rate / latency units
// (%, ms, s) AVERAGE it — summing percentages or percentiles is
// meaningless. Same input → same kept set → stable colours.
const OTHERS_KEY = 'others';
function foldTopN(series: SpanMetricSeries[], unit: string | undefined, n = 8): SpanMetricSeries[] {
  if (series.length <= n) return series;
  const u = (unit || '').trim().toLowerCase();
  const mean = u === '%' || u === 'ms' || u === 's';
  const ranked = series
    .map(s => ({ s, area: s.points.reduce((a, p) => a + Math.abs(p.value ?? 0), 0) }))
    .sort((a, b) => b.area - a.area);
  const keep = ranked.slice(0, n).map(r => r.s);
  const tail = ranked.slice(n).map(r => r.s);
  const sumByTime = new Map<number, number>();
  const cntByTime = new Map<number, number>();
  for (const s of tail) {
    for (const p of s.points) {
      if (p.value == null) continue;
      sumByTime.set(p.time, (sumByTime.get(p.time) ?? 0) + p.value);
      cntByTime.set(p.time, (cntByTime.get(p.time) ?? 0) + 1);
    }
  }
  const others: SpanMetricSeries = {
    ...tail[0],
    groupKey: [OTHERS_KEY],
    points: [...sumByTime.entries()]
      .sort((a, b) => a[0] - b[0])
      .map(([time, sum]) => ({ time, value: mean ? sum / (cntByTime.get(time) || 1) : sum })),
  };
  return [...keep, others];
}

// computeChartData (v0.8.520) — pure data prep shared by the build effect (the
// initial plot) AND the setData fast-path. Folds the tail, shifts the compare
// window, unions the time axis, and aligns every series to it. No DOM / theme
// reads live here (colors are resolved in the build effect), so it runs inside
// a useMemo keyed on the DATA inputs only: a 30s poll recomputes this once, and
// its output either seeds a rebuild (labels changed) or flows through setData
// (values changed, structure identical).
interface ChartData {
  eff: SpanMetricSeries[];
  allSeries: SpanMetricSeries[];
  labels: string[];            // allSeries labels, in order (raw, pre-suffix)
  data: uPlot.AlignedData;
  compareEnabled: boolean;
}
function computeChartData(
  series: SpanMetricSeries[],
  unit: string | undefined,
  compareSeries: SpanMetricSeries[] | undefined,
  compareOffsetNs: number | undefined,
  maxSeries: number,
): ChartData {
  const compareEnabled = (compareSeries?.length ?? 0) > 0 && (compareOffsetNs ?? 0) > 0;
  // Fold the long tail into "others" so >N-series charts stay legible.
  // maxSeries lets a caller raise N (e.g. JBoss datasource panels want
  // every datasource, no "others" cut — v0.9.148 operator-reported).
  const eff = foldTopN(series, unit, maxSeries);

  // Shift compare-series timestamps forward by the offset so a point from
  // "24h ago" lands at the same X position as the matching current-period
  // point ("where the line was at this same time-of-day yesterday").
  const offsetNs = compareEnabled ? (compareOffsetNs ?? 0) : 0;
  const shiftedCompare: SpanMetricSeries[] = compareEnabled
    ? (compareSeries ?? []).map(s => ({
        ...s,
        points: s.points.map(p => ({ ...p, time: p.time + offsetNs })),
      }))
    : [];

  // Unified time axis (union of bucket timestamps across every series,
  // including the shifted compare set). uPlot consumes this as a single x
  // array shared by all y arrays.
  const allTimes = new Set<number>();
  eff.forEach(s => s.points.forEach(p => allTimes.add(p.time)));
  shiftedCompare.forEach(s => s.points.forEach(p => allTimes.add(p.time)));
  const times = [...allTimes].sort((a, b) => a - b);
  const xs = times.map(t => t / 1e9); // ns → unix seconds

  // Per-series y values aligned to the union x axis. Missing points become
  // null → uPlot draws a gap.
  const allSeries = [...eff, ...shiftedCompare];
  const labels = allSeries.map(s => (s.groupKey.length ? s.groupKey.join(' / ') : 'value'));
  const ySeries: (number | null)[][] = allSeries.map(s => {
    const valByTime = new Map(s.points.map(p => [p.time, p.value]));
    return times.map(t => valByTime.get(t) ?? null);
  });
  const data: uPlot.AlignedData = [xs, ...ySeries] as uPlot.AlignedData;
  return { eff, allSeries, labels, data, compareEnabled };
}

export function MultiLineChart({
  series, unit, height = 320, deploys, thresholds, regions, syncKey, onZoom, onZoomReset,
  compareSeries, compareOffsetNs, compareLabel, logScale, onBucketClick, colorOf,
  selectedOps, onLegendClick, xRange, maxSeries,
}: {
  series: SpanMetricSeries[];
  unit?: string;
  height?: number;
  // Fold threshold — series past this count collapse into "others"
  // (default 8). Raise it for panels that must show every series with
  // no tail cut, e.g. JBoss datasource breakdowns (v0.9.148).
  maxSeries?: number;
  // Optional per-series colour override keyed by the joined label. Returns a
  // CSS colour (e.g. var(--accent) or #rrggbb) to win over the default stable
  // seriesColor palette, or undefined to fall through to it. Lets a caller
  // (the metric query editor) pin a query's colour. (v0.7.128)
  colorOf?: (label: string) => string | undefined;
  // Optional dashed vertical lines painted at the given times.
  // Hovering near a marker shows label + description in the
  // chart's tooltip panel.
  deploys?: DeployMarker[];
  // Optional horizontal threshold lines at given y values.
  // Painted as dashed lines; the area above the line is gently
  // tinted in the severity colour so a band of values that
  // breached the threshold is visually obvious without
  // requiring the operator to read tick marks.
  thresholds?: Threshold[];
  // Grafana-parite M3 — problem/anomali x-bölgeleri (arka-plan gölge + üst
  // şerit + etiket; lib/chart/overlays.ts). Servis RED panelleri açık
  // problem pencerelerini geçirir; valToPos canlı ölçeği okur → zoom-doğru.
  regions?: ChartTimeRegion[];
  // syncKey — when set, multiple charts on the same page sync
  // their cursor positions. Hovering one chart paints the
  // crosshair on every chart sharing the key. Datadog /
  // Grafana use this so a dashboard reads as one view rather
  // than 8 disconnected ones.
  syncKey?: string;
  // onZoom — called when the user click-drags a horizontal
  // range to zoom in. Receives unix seconds; the page can
  // update its TimeRange state and re-fetch. Without this the
  // drag still works as a visual zoom but doesn't propagate.
  onZoom?: (fromUnixSec: number, toUnixSec: number) => void;
  // onZoomReset (Grafana-parite M1) — çift-tık: sayfa geri-yığını bir adım
  // pop eder (Service/Dashboard handleZoomReset). Verilmezse mevcut davranış:
  // uPlot'un yerleşik dblclick autoscale'i (yerel zoom'u tam veri aralığına
  // döndürür), URL'e dokunmaz.
  onZoomReset?: () => void;
  // onBucketClick — v0.7.22. "Spike → exemplar": a plain click
  // (not a drag) on the chart resolves the clicked time-bucket
  // window in NANOSECONDS and hands it to the caller, which
  // typically fetches a representative bad trace for that bucket
  // and opens it. FULLY OPT-IN: when this prop is absent the
  // chart registers no extra click listener, keeps the default
  // cursor, and behaves byte-for-byte as before (MultiLineChart
  // is shared across Endpoints, dashboards, Metrics, etc — those
  // callers must not gain a click affordance). The window is
  // [center − step/2, center + step/2] where step is the data's
  // bucket width in seconds; see the click hook for the math.
  onBucketClick?: (fromNs: number, toNs: number) => void;
  // compareSeries — past-period data overlaid as dashed,
  // half-opacity ghost lines aligned to the current window.
  // The page fetched the same series N hours/days ago and
  // passes the offset so we can shift each point's timestamp
  // forward into the current X range. Same colour as the
  // matching current series so the eye reads "this is the
  // previous version of THAT line" without a separate legend.
  compareSeries?: SpanMetricSeries[];
  compareOffsetNs?: number;
  compareLabel?: string; // tooltip suffix e.g. "24h ago"
  // logScale — v0.5.484. SREs flip to log when the metric
  // spans orders of magnitude (HTTP status counts, queue
  // depths, percentile latencies that move from 5ms to 5s).
  // Linear by default; toggleable from the parent.
  logScale?: boolean;
  // Controlled legend selection shared across sibling charts (the 3
  // by-operation panels). null/undefined = all visible; a Set lists
  // the ONLY operations to show (isolate). When provided, legend clicks
  // call onLegendClick instead of mutating this chart locally, so the
  // parent can sync every sibling chart to one selection.
  selectedOps?: Set<string> | null;
  onLegendClick?: (label: string, additive: boolean) => void;
  // v0.9.83 (uPlot Aşama 2 madde 2) — x-eksenini sorgu penceresine sabitle
  // (unix sec); zoom isteği aynen geçer. Verilmezse eski davranış.
  xRange?: XPin | null;
}) {
  const hostRef = useRef<HTMLDivElement>(null);
  // plotRef, motorun döndürdüğü uPlot örneğidir (aşağıda useChartEngine ile
  // atanır) — isolate-click + selectedOps efektleri canlı örneğe buradan erişir.

  // Latest onBucketClick held in a ref so the click listener
  // (registered once per plot build) always calls the current
  // callback without the chart needing to rebuild when only the
  // callback identity changes. The listener is only attached at
  // all when onBucketClick is provided (opt-in).
  const bucketClickRef = useRef(onBucketClick);
  bucketClickRef.current = onBucketClick;
  // Legend labels per data-series index (current eff + compare),
  // refreshed on each build so the click handler can resolve a clicked
  // legend row to its operation name. onLegendClick held in a ref so the
  // once-per-build click listener always calls the latest callback.
  const labelsRef = useRef<string[]>([]);
  const onLegendClickRef = useRef(onLegendClick);
  onLegendClickRef.current = onLegendClick;
  // Latest controlled selection, read by the apply-effect below.
  const selectedRef = useRef<Set<string> | null | undefined>(selectedOps);
  selectedRef.current = selectedOps;

  // Mirror of the live "this index is currently visible" set.
  // Kept in a ref instead of useState so the click handler can
  // toggle without triggering a re-render of the React tree
  // around the chart.
  const visibleRef = useRef<boolean[]>([]);

  // onZoom held in a ref (v0.8.520) so the once-per-build setSelect hook always
  // calls the CURRENT drag-zoom callback. Callers pass a fresh arrow each render
  // (Dashboard/Service/Endpoints) — tracking onZoom by identity in the build
  // deps destroyed+recreated the plot on every parent render (canvas flicker,
  // lost cursor). Now the build deps track PRESENCE only (!!onZoom, via the
  // signature); the live callback flows through here. Same pattern as
  // bucketClickRef / onLegendClickRef above.
  const onZoomRef = useRef(onZoom);
  onZoomRef.current = onZoom;
  const xRangeRef = useRef(xRange); xRangeRef.current = xRange;

  // Theme tick — a data-theme toggle must re-resolve the canvas CSS-var colors,
  // which only happens on a rebuild. So the theme counter is a BUILD dep (never
  // the setData fast-path): a toggle re-creates the plot with fresh hex.
  const themeTick = useThemeTick();

  // ── Grafana-parite #2: tooltip pin ────────────────────────────────────────
  // pinRef: pinli veri index'i (null = pin yok); ikinci tık/Esc çözer.
  // Pin tetiği (MLC): onBucketClick YOKKEN düz tık; onBucketClick VARKEN
  // (spike→exemplar düz tıkın sahibi, v0.7.22) Alt+tık pinler — bucket-click
  // dinleyicisi Alt'lı tıkı atlar, iki jest çakışmaz. PİNLİYKEN düz tık
  // yine buraya düşer ve UNPIN eder (paylaşımlı "tık / Esc çözer" ipucu
  // yalan söylemesin — review 8/8 #7); bucket dinleyicisi pinli tıkı işlemez.
  // Legend tıkları ayrı subtree'de (u.over dışı), pin dinleyicisine ulaşmaz.
  const pinRef = useRef<number | null>(null);
  // mousedown→click yatay mesafe kaynağı — pin VE bucket-click dinleyicileri
  // aynı dragPx'i okur (u.select.width, setSelect hook'unda sıfırlandığından
  // click anında güvenilmez — review 8/8 #4).
  const downXRef = useRef<number | null>(null);
  const unpinTooltip = () => {
    pinRef.current = null;
    const tip = hostRef.current?.querySelector('.uplot-tooltip') as HTMLDivElement | null;
    if (tip) clearPinStyle(tip, 'opacity');
  };
  const attachPinListener = (u: uPlot) => {
    u.over.addEventListener('mousedown', e => { downXRef.current = e.clientX; });
    u.over.addEventListener('dblclick', () => unpinTooltip());
    u.over.addEventListener('click', e => {
      if (bucketClickRef.current && !e.altKey && pinRef.current == null) return; // düz tık bucket-click'in
      const tip = hostRef.current?.querySelector('.uplot-tooltip') as HTMLDivElement | null;
      if (!tip) return;
      const d = decidePinClick({
        pinnedIdx: pinRef.current, cursorIdx: u.cursor.idx,
        dragPx: downXRef.current == null ? 0 : Math.abs(e.clientX - downXRef.current),
        detail: e.detail,
      });
      if (d.action === 'unpin') unpinTooltip();
      else if (d.action === 'pin' && tip.style.opacity === '1') { pinRef.current = d.idx; applyPinStyle(tip); }
    });
  };
  // Esc → pin çöz.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape' && pinRef.current != null) unpinTooltip(); };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, []); // unpinTooltip yalnız ref'lere dokunur — stable

  // Memoised data bundle — the single prep pass feeding BOTH the build effect
  // and the setData fast-path. Keyed on the data inputs only, so a poll (fresh
  // `series` identity) recomputes it exactly once. bundleRef exposes the live
  // bundle to the build effect's once-registered hooks without listing data in
  // the build deps (which would defeat the fast-path).
  const bundle = useMemo(
    () => computeChartData(series, unit, compareSeries, compareOffsetNs, maxSeries ?? 8),
    [series, unit, compareSeries, compareOffsetNs, maxSeries],
  );
  const bundleRef = useRef(bundle);
  bundleRef.current = bundle;
  // Row-index → operation label, for the legend click handler + the controlled-
  // selection effect. Set from the bundle each render so both stay correct even
  // on a data-only update that doesn't rebuild.
  labelsRef.current = bundle.labels;

  // Build signature — the pure "rebuild vs setData" seam. Everything that, when
  // changed, forces a full uPlot re-create (structure, axes, hooks, overlays).
  // A data-only poll leaves this string identical → the build effect below
  // doesn't run and the fast-path takes over. See lib/chartBuildSig.ts.
  const buildSig = chartBuildSignature({
    labels: bundle.labels,
    unit, height, syncKey, logScale,
    hasZoom: !!onZoom,
    hasBucketClick: !!onBucketClick,
    compareOffsetNs, compareLabel,
    deploys, thresholds, regions,
    // v0.9.100 (Adım 4) — colorOf, motora göçte artık build-effect dep'i değil;
    // her label'ın çözülen override rengini imzaya kat (yoksa null; folded
    // "others" tail colorOf'u yok sayar → null). Böylece motorun [signature,
    // themeTick] rebuild tetikleyicisi eski colorOf-identity dep'ini karşılar.
    colorOverrides: bundle.labels.map(l => (l === OTHERS_KEY ? null : (colorOf?.(l) ?? null))),
  });

  // buildOptions (v0.9.100, chart-consolidation Adım 4) — renkleri REBUILD
  // ANINDA çöz (tema flip'te motor bu fn'i yeniden çağırır, CSS-var'lar taze)
  // ve tam uPlot.Options üret. Eski build effect'in opts'unun BİREBİRİ. new
  // uPlot / destroy / ResizeObserver / setData fast-path (v0.9.78/79 zoom-koruma)
  // İSKELETİ artık useChartEngine'de; visibleRef reset afterBuild'e taşındı.
  // `el` motorun buildOptions'ı yalnız hostRef.current varken çağırmasıyla
  // garanti (engine.ts §renderable guard) → non-null.
  const buildOptions = (width: number): uPlot.Options => {
    const el = hostRef.current!;
    const { eff, allSeries } = bundle;

    const css = getComputedStyle(document.documentElement);
    const text3 = css.getPropertyValue('--text3').trim() || '#484f58';
    const grid  = css.getPropertyValue('--bg2').trim()   || '#21262d';
    const mutedGray = css.getPropertyValue('--text3').trim() || '#8b9299';
    // v0.8.302 (quality bar U2) — threshold/deploy overlay colors come from
    // theme tokens (canvas needs resolved values, so read them like text3/
    // grid above instead of baking dark-palette hex).
    const errCol    = css.getPropertyValue('--err').trim()    || '#e84e4e';
    const warnCol   = css.getPropertyValue('--warn').trim()   || '#f0b352';
    const deployCol = css.getPropertyValue('--purple').trim() || '#a371f7';
    // Stable colour per series name; the folded "others" tail is always muted grey.
    const colorFor = (label: string) =>
      label === OTHERS_KEY ? mutedGray : (colorOf?.(label) ?? seriesColor(label));
    // Controlled visibility (isolate). null/undefined selection = all
    // visible; otherwise only the listed operations show.
    const isVis = (label: string) => selectedOps == null || selectedOps.has(label);
    // Grafana-style legend columns: Last / Min / Max / Avg over the
    // CURRENTLY VISIBLE x-range. uPlot re-invokes this on every redraw
    // (incl. after a zoom), so the stats track the zoomed window. The
    // cursor dataIdx is ignored on purpose — the floating tooltip owns
    // the live cursor readout; this table is the range summary.
    const fmt1 = (v: number) => fmtSmart(v, unit);
    // v0.9.108 (C1) — toplanabilir birimlerde (rps/sayaç/bytes) native stats
    // lejantı Σ Sum kolonunu da gösterir → OVC/TC'nin paylaşımlı StatsLegend'i
    // ile TAM kolon parity (Last/Min/Max/Avg/Σ). Toplanamaz birimler (%/ms/s)
    // Σ'yı atlar: yüzde/percentile toplamak anlamsız (StatsLegend ile aynı
    // kural, isAdditiveUnit ortak kaynak). Birim grafik-geneli → tüm seriler
    // aynı kolon setini döndürür (uПlot header tutarlı).
    const additive = isAdditiveUnit(unit);
    const mkValues = (u: uPlot, sidx: number): Record<string, string> => {
      const xa = u.data[0] as number[];
      const ya = u.data[sidx] as (number | null)[];
      const xmin = u.scales.x.min ?? -Infinity;
      const xmax = u.scales.x.max ?? Infinity;
      let mn = Infinity, mx = -Infinity, sum = 0, cnt = 0;
      let last: number | null = null;
      for (let i = 0; i < xa.length; i++) {
        if (xa[i] < xmin || xa[i] > xmax) continue;
        const v = ya[i];
        if (v == null) continue;
        if (v < mn) mn = v;
        if (v > mx) mx = v;
        sum += v; cnt++; last = v;
      }
      if (cnt === 0) {
        return additive
          ? { Last: '—', Min: '—', Max: '—', Avg: '—', 'Σ': '—' }
          : { Last: '—', Min: '—', Max: '—', Avg: '—' };
      }
      const base = { Last: fmt1(last as number), Min: fmt1(mn), Max: fmt1(mx), Avg: fmt1(sum / cnt) };
      return additive ? { ...base, 'Σ': fmt1(sum) } : base;
    };

    // Grafana-parite M1 — drag(+sync)+setSelect paylaşımlı buildCursorOpts'tan
    // (davranış birebir: 4px eşik, onZoomRef'ten canlı çağrı, setScale yalnız
    // onZoom varken; hasZoom presence build imzasında).
    const cz = buildCursorOpts({
      syncKey,
      setScale: !!onZoom,
      onZoom: onZoom ? (f, t) => onZoomRef.current?.(f, t) : undefined,
    });

    const opts: uPlot.Options = {
      // width motordan gelir (el.clientWidth || 320); ilk paint'te 0 ise motor
      // 320 verir, ResizeObserver sonraki layout'ta düzeltir (OVC/TC ile aynı).
      width,
      height,
      // Tells uPlot the x values are unix seconds → "HH:MM"
      // tick format instead of raw integers. Single most
      // important option for time-series charts.
      // v0.5.484 — log scale on y when caller opts in. distr:3
      // is uPlot's log10; the chart redraws against the same
      // data with log ticks. Zero/negative values land on the
      // axis floor — fine for the count/latency metrics SREs
      // toggle log for (always non-negative).
      scales: {
        x: { time: true, range: (u, mn, mx) => xRangePinned(u.data[0] as number[], xRangeRef.current, mn, mx) },
        y: logScale ? { distr: 3, log: 10 } : {},
      },
      // Hovering a legend row focuses that series and dims the others
      // to 0.3 alpha (Grafana / Datadog legend-hover behaviour). Works
      // with the native legend + cursor.focus.prox set below.
      focus: { alpha: 0.3 },
      series: [
        {},
        ...allSeries.map((s, idx) => {
          const isCompare = idx >= series.length;
          const label = s.groupKey.length ? s.groupKey.join(' / ') : 'value';
          // Curated palette → same series name lands on the
          // same colour across every chart in the app. Old
          // hashColor() picked any of 16M shades, so the same
          // 'frontend' service ended up green here, purple
          // there. seriesColor maps to one of 10 stable hues.
          // Compare lines share the colour of their current-
          // period twin (same label → same hash → same stop)
          // so the eye reads "ghost of THAT line" not "a new
          // unknown line".
          const color = colorFor(label);
          if (isCompare) {
            return {
              // Suffix the label so the legend disambiguates
              // the two lines. compareLabel arrives as e.g.
              // "24h ago" or "7d ago".
              label: `${label} · ${compareLabel ?? 'past'}`,
              // Half-opacity hex append + dashed stroke read
              // unambiguously as "ghost / reference line".
              stroke: color + '88',
              fill: 'transparent', // no fill on compare so it doesn't fight the current band
              width: 1.5,
              dash: [4, 4],
              points: { show: false },
              // v0.9.84 (madde 3) — kısa boşluk köprülenir, kesinti kırık.
              gaps: stepGapsRefiner,
              show: isVis(label),
              value: (_u: uPlot, v: number | null) => fmtSmart(v, unit) +
                (v != null ? ` (${compareLabel ?? 'past'})` : ''),
              values: mkValues,
            } satisfies uPlot.Series;
          }
          return {
            label,
            stroke: color,
            // Multi-series RED charts read cleaner with NO band fill;
            // a lone series keeps a faint ≤8% tint for shape (Grafana).
            fill: eff.length > 1 ? undefined : color + '14',
            width: 1.5,
            // No always-on markers — uPlot paints a point on the
            // focused series at the cursor, so dots appear on hover only.
            points: { show: false },
            // v0.9.84 (madde 3) — union x-ekseni + tek kaçmış scrape'in
            // yarattığı 1-bucket'lık delikler köprülenir; gerçek kesinti
            // (≥ 1.5×step null-run) kırık kalır.
            gaps: stepGapsRefiner,
            show: isVis(label),
            value: (_u: uPlot, v: number | null) => fmtSmart(v, unit),
            values: mkValues,
          } satisfies uPlot.Series;
        }),
      ],
      axes: [
        {
          stroke: text3,
          grid: { stroke: grid, width: 1 },
          ticks: { stroke: grid, width: 1 },
          // v0.5.366 — operator-reported: picking a multi-day
          // range made uPlot drop to month-only labels on the
          // x-axis, hiding the time-of-day signal. Force a
          // single explicit formatter that always shows the
          // time (+ the date on day boundaries) so the operator
          // never loses the intra-day shape at any zoom.
          values: (_u, splits) => fmtXTicks(splits),
          // v0.8.58 — min px between ticks so uPlot thins the
          // tick COUNT to the chart width. Without it, uPlot's
          // ~50px default packed the wide date+time labels so
          // tightly they overlapped on multi-day ranges
          // (operator-reported: "zamanlar üst üste biniyor").
          space: 70,
        },
        {
          stroke: text3,
          grid: { stroke: grid, width: 1 },
          ticks: { stroke: grid, width: 1 },
          // Smart axis formatter — promotes ms→s, count→k/M,
          // bytes→kB/MB, etc. The previous "v.toFixed(2)"
          // produced "12500.00" on a 12.5k-rps chart; now it
          // reads "12.5k rps".
          values: (_u, splits) => splits.map(v => fmtSmart(v, unit)),
          // Slightly wider axis to fit the trailing unit
          // ("234 ms" needs more room than "234").
          size: 56,
        },
      ],
      cursor: {
        // Tighter prox (v0.5.257 — was 30) so the operator can
        // step through nearby points without the cursor leaking
        // onto adjacent series. Datadog / Uptrace tooltips snap
        // at ~15px; 30 was a wider catch-net that made dense
        // multi-series charts feel "imprecise".
        x: true, y: true, focus: { prox: 15 },
        // v0.9.84 (madde 4) — hover en yakın DOLU örneğe snap (±2 bucket);
        // union ekseninin null'ları tooltip'te "—" üretmesin.
        dataIdx: (u, sidx, idx) => nearestFilledIdx(u.data[sidx] as (number | null)[], idx, 2),
        // Grafana-parite M1 — drag+sync paylaşımlı buildCursorOpts'tan;
        // MLC şekli birebir: x-only drag, setScale yalnız onZoom varken
        // (yerel görsel zoom + sayfaya devir), sync key'li kardeşlerle
        // crosshair senkronu (Grafana / Datadog dashboard pattern).
        ...cz.cursor,
      },
      // live:false — the columns are range stats (Last/Min/Max/Avg via
      // series.values), not the cursor value; the floating tooltip is the
      // live readout. The legend re-renders on zoom so stats stay scoped
      // to the visible window.
      legend: { show: true, live: false, markers: { width: 2 } },
      hooks: {
        // Drag-zoom callback — fires when the operator releases a horizontal
        // selection; paylaşımlı cursorOpts hook'u sec aralığını çıkarır,
        // sayfaya devreder ve gri bandı temizler. onZoom yokken [] (eski
        // davranış — uPlot `in hooks` kontrolü boş dizide no-op).
        setSelect: cz.setSelect ?? [],
        // Spike → exemplar click hook (v0.7.22). OPT-IN: only
        // registered when onBucketClick is set, so non-exemplar
        // callers are untouched. Attaches a plain-click listener
        // to uPlot's `over` element once at build time; resolves
        // the clicked time to the enclosing bucket window in ns
        // and hands it off. A click that's really the tail of a
        // drag-zoom is ignored so the two gestures don't fight —
        // measured as the pin path's mousedown→click distance
        // (downXRef; u.select is already reset by the setSelect
        // hook when the click lands, so its width is useless here
        // — review 8/8 #4: a drag-zoom release must NOT open the
        // exemplar drawer).
        ready: onBucketClick ? [
          (u) => {
            u.over.addEventListener('click', (ev: MouseEvent) => {
              // Grafana-parite #2 — Alt+tık tooltip PIN'in tetiği (bucket-click
              // varken düz tık burada kalır); Alt'lı tık bucket'ı atlar.
              if (ev.altKey) return;
              // Pinliyken düz tık pin dinleyicisinin (UNPIN); bucket'a düşmez
              // (review 8/8 #7 — "tık çözer" ipucu ile tutarlı).
              if (pinRef.current != null) return;
              // Çift-tıkın click'leri zoom-geri jestine ait (review 8/8 #3b).
              if (ev.detail > 1) return;
              const dragPx = downXRef.current == null ? 0 : Math.abs(ev.clientX - downXRef.current);
              if (dragPx >= 4) return; // was a drag, not a click
              const left = u.cursor.left ?? -1;
              if (left < 0) return;
              const xSec = u.posToVal(left, 'x'); // unix seconds at cursor
              if (!isFinite(xSec)) return;
              const xs = u.data[0];
              if (!xs || xs.length === 0) return;
              // Bucket width in seconds. Prefer the gap between the
              // two data points straddling the cursor (handles
              // irregular spacing); fall back to the first gap, then
              // to a 60s default for a single-point series so we
              // still open a sane window rather than a zero-width one.
              let stepSec = 60;
              if (xs.length >= 2) {
                // Find the nearest point and measure its local gap.
                let nearestI = 0;
                let best = Infinity;
                for (let i = 0; i < xs.length; i++) {
                  const d = Math.abs((xs[i] as number) - xSec);
                  if (d < best) { best = d; nearestI = i; }
                }
                const lo = nearestI > 0 ? (xs[nearestI] as number) - (xs[nearestI - 1] as number) : Infinity;
                const hi = nearestI < xs.length - 1 ? (xs[nearestI + 1] as number) - (xs[nearestI] as number) : Infinity;
                const gap = Math.min(lo, hi);
                if (isFinite(gap) && gap > 0) stepSec = gap;
                else {
                  const fallback = (xs[1] as number) - (xs[0] as number);
                  if (fallback > 0) stepSec = fallback;
                }
              }
              const fromSec = xSec - stepSec / 2;
              const toSec = xSec + stepSec / 2;
              // unix seconds → ns. Math.round avoids float drift on
              // the *1e9 scale-up so the server's BETWEEN matches.
              const fromNs = Math.round(fromSec * 1e9);
              const toNs = Math.round(toSec * 1e9);
              bucketClickRef.current?.(fromNs, toNs);
            });
          },
        ] : [],
        // Overlay draw hooks — paint problem/anomaly time-regions
        // (background-most), threshold lines (dashed horizontal
        // lines + tinted breach band) AND deploy markers (dashed
        // vertical lines) after uPlot's own series render so the
        // overlays sit on top of fills. Combined into a single
        // hook so we only register one handler.
        draw: (deploys && deploys.length > 0) || (thresholds && thresholds.length > 0) || (regions && regions.length > 0) ? [
          (u) => {
            const ctx = u.ctx;
            ctx.save();

            // ── Problem/anomali bölgeleri (Grafana-parite M3) ──
            // En arkada; threshold/deploy üstüne biner.
            if (regions && regions.length > 0) drawTimeRegions(u, regions);

            // ── Threshold lines ──────────────────────────────
            //
            // Grafana-parite M3 — eski yerel blok (TSP ile birebir kopyaydı)
            // paylaşımlı drawThresholds'a delege. Severity → renk eşlemesi
            // (warn → amber, err → red) burada kalır; band %7 globalAlpha,
            // sağ-kenar etiket — görsel birebir.
            if (thresholds && thresholds.length > 0) {
              drawThresholds(u, thresholds.map(th => ({
                value: th.value,
                label: th.label,
                color: th.severity === 'err' ? errCol : warnCol,
              })));
            }

            // ── Deploy markers ───────────────────────────────
            if (deploys && deploys.length > 0) {
              const xMin = u.scales.x.min ?? 0;
              const xMax = u.scales.x.max ?? 0;
              ctx.strokeStyle = deployCol;
              ctx.fillStyle = deployCol;
              ctx.lineWidth = 1.2;
              ctx.setLineDash([5, 4]);
              ctx.font = '10px ui-monospace, monospace';
              for (const d of deploys) {
                const t = d.timeUnixNs / 1e9;
                if (t < xMin || t > xMax) continue;
                const x = u.valToPos(t, 'x', true);
                ctx.beginPath();
                ctx.moveTo(x, u.bbox.top);
                ctx.lineTo(x, u.bbox.top + u.bbox.height);
                ctx.stroke();
                const lbl = d.label.length > 12 ? d.label.slice(0, 11) + '…' : d.label;
                ctx.setLineDash([]);
                ctx.fillText(lbl, x + 3, u.bbox.top + 11);
                ctx.setLineDash([5, 4]);
              }
            }

            ctx.restore();
          },
        ] : [],
        // Grafana-style hover tooltip — the floating panel
        // matches what Datadog / Honeycomb / Grafana all do
        // (hover anywhere on the chart and see all series'
        // values at that x). uPlot's native legend already
        // updates the bottom table with `live: true`, but
        // operators expect the panel to show up next to the
        // cursor so they don't have to look down. Single DOM
        // mutation per move event, no overlay canvas.
        setCursor: [
          (u) => {
            const tip = el.querySelector('.uplot-tooltip') as HTMLDivElement | null;
            // Y-axis crosshair value pill — small label pinned
            // to the y-axis edge at the cursor's exact y
            // position. Datadog / Grafana paint this so the
            // operator can read "the cursor is at 234ms"
            // without snapping to a series datum. Different
            // surface than the floating tooltip (which shows
            // EVERY series' value at the cursor's x).
            const yPill = el.querySelector('.uplot-ypill') as HTMLDivElement | null;
            const cTop = u.cursor.top ?? -1;
            if (yPill) {
              if (cTop < u.bbox.top || cTop > u.bbox.top + u.bbox.height) {
                yPill.style.opacity = '0';
              } else {
                const yVal = u.posToVal(cTop, 'y');
                if (isFinite(yVal)) {
                  yPill.textContent = fmtSmart(yVal, unit);
                  yPill.style.top = `${cTop - 8}px`;
                  yPill.style.opacity = '1';
                } else {
                  yPill.style.opacity = '0';
                }
              }
            }
            if (!tip) return;
            // Grafana-parite #2 — pinliyken tooltip donuk: içerik/pozisyon
            // dokunulmaz (crosshair + yPill yaşamaya devam eder).
            if (pinRef.current != null) return;
            const idx = u.cursor.idx;
            if (idx == null || idx < 0) {
              tip.style.opacity = '0';
              return;
            }
            const xVal = u.data[0][idx];
            if (xVal == null) {
              tip.style.opacity = '0';
              return;
            }
            // Format X (unix seconds → HH:MM:SS).
            const d = new Date((xVal as number) * 1000);
            const hh = d.getHours().toString().padStart(2, '0');
            const mm = d.getMinutes().toString().padStart(2, '0');
            const ss = d.getSeconds().toString().padStart(2, '0');
            // Per-series rows — Grafana-parite #2: sıralama + format artık
            // paylaşımlı sortedTooltipRows'ta (null-drop + değer DESC stable
            // + fmtSmart birim). Davranış birebir eski yerel kopya: aynı DESC
            // sıra, aynı fmtSmart(v, unit) çıktısı; boş seri satırdan düşer.
            // Live eff — the fast-path may have swapped the data without a
            // rebuild. Count + labels are guaranteed unchanged by the build
            // signature, so this only ever reads the current window's groupKeys
            // while u.data supplies the fresh values.
            const effRows = bundleRef.current.eff;
            const rows = sortedTooltipRows(effRows.map((s, i) => {
              const label = s.groupKey.length ? s.groupKey.join(' / ') : 'value';
              // v0.9.84 (madde 4) — seri başına snap'lenmiş idx (dataIdx).
              const si = u.cursor.idxs?.[i + 1] ?? idx;
              return {
                label, color: colorFor(label),
                // Grafana-parite #2 — lejanttan gizlenen seri tooltip'ten de
                // düşer (value null → model satırı atar; OVC/TC/TSP paritesi,
                // review 8/8 #6) — bold-nearest seçimi de artık yalnız çizili
                // satırlar üzerinde koşar. visibleRef ↔ eff index-hizalı
                // (compare satırları allSeries'te eff'ten SONRA gelir).
                value: visibleRef.current[i] === false ? null
                  : ((u.data[i + 1] as (number | null)[] | undefined)?.[si] ?? null),
                unit,
              };
            }));
            if (rows.length === 0) {
              tip.style.opacity = '0';
              return;
            }
            // Find the nearest deploy marker within ~12px of
            // the cursor's x — close enough that the operator
            // is clearly hovering "the deploy line" not
            // somewhere else. Surfaces version + description
            // at the top of the tooltip; same colour as the
            // dashed line itself for visual continuity.
            let deployRow = '';
            if (deploys && deploys.length > 0) {
              const cursorX = u.cursor.left ?? -1;
              let nearest: DeployMarker | null = null;
              let bestDx = 12;
              for (const d of deploys) {
                const t = d.timeUnixNs / 1e9;
                const px = u.valToPos(t, 'x', true);
                const dx = Math.abs(px - cursorX);
                if (dx < bestDx) {
                  bestDx = dx;
                  nearest = d;
                }
              }
              if (nearest) {
                const lbl = escapeHTML(nearest.label);
                const desc = nearest.description ? ' — ' + escapeHTML(nearest.description) : '';
                deployRow =
                  `<div style="display:flex;gap:8px;align-items:center;line-height:1.5;margin-bottom:4px;padding-bottom:4px;border-bottom:1px solid var(--border)">` +
                    `<span style="display:inline-block;width:8px;height:8px;background:var(--purple, #a371f7);border-radius:2px;flex-shrink:0"></span>` +
                    `<span style="flex:1;overflow:hidden;text-overflow:ellipsis;white-space:nowrap" title="${lbl}${desc}">deploy ${lbl}</span>` +
                  `</div>`;
              }
            }
            // Bold the series nearest the cursor's Y so the operator can
            // tell which line they're tracking among many.
            const yAtCursor = u.posToVal(u.cursor.top ?? -1, 'y');
            let boldLabel = '';
            if (isFinite(yAtCursor) && rows.length) {
              let best = Infinity;
              for (const r of rows) {
                const dy = Math.abs(r.value - yAtCursor);
                if (dy < best) { best = dy; boldLabel = r.label; }
              }
            }
            tip.innerHTML =
              `<div style="font-weight:600;margin-bottom:4px">${hh}:${mm}:${ss}</div>` +
              deployRow +
              rows.map(r => {
                const lbl = escapeHTML(r.label);
                const wt = r.label === boldLabel ? 'font-weight:700;' : '';
                return `<div style="display:flex;gap:8px;align-items:center;line-height:1.5;${wt}">` +
                  `<span style="display:inline-block;width:8px;height:8px;background:${r.color};border-radius:2px;flex-shrink:0"></span>` +
                  `<span style="flex:1;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;max-width:240px" title="${lbl}">${lbl}</span>` +
                  `<span style="font-family:ui-monospace,monospace;font-variant-numeric:tabular-nums">${r.text}</span>` +
                `</div>`;
              }).join('');
            tip.style.opacity = '1';
            // Position beside the cursor, flipping per-axis and never landing
            // under the pointer — see placeTooltip. cursor.left/top are
            // over-relative; pass the over box's offset + size + the container
            // box so placement and clamping share one basis.
            const over = u.over;
            const { x, y } = placeTooltip(
              u.cursor.left ?? 0, u.cursor.top ?? 0,
              tip.offsetWidth, tip.offsetHeight,
              over.clientWidth, over.clientHeight,
              over.offsetLeft, over.offsetTop,
              el.clientWidth, el.clientHeight,
            );
            tip.style.left = `${x}px`;
            tip.style.top  = `${y}px`;
          },
        ],
      },
    };

    return opts;
  };

  // Yaşam döngüsü motorda (v0.9.100, chart-consolidation Adım 4). new uPlot /
  // destroy / ResizeObserver / setData fast-path + v0.9.78/79 drag-zoom-koruma
  // İSKELETİ engine.ts::useChartEngine'de tek kopya. afterBuild: rebuild
  // isolation'ı sıfırlar (allSeries hepsi görünür) — eski build effect'in
  // visibleRef reset'i; data-only fast-path setData ile series `show`
  // flag'lerini KORUR, o yüzden reset yalnız rebuild'de. MLC'de DOM deploy
  // bayrağı yok → afterData/onResize gereksiz. colorOf artık imzada
  // (colorOverrides), o yüzden motorun [signature, themeTick] tetikleyicisi
  // eski [buildSig, colorOf, themeTick]'i birebir karşılar.
  const plotRef = useChartEngine(hostRef, {
    signature: buildSig,
    height,
    renderable: bundle.eff.length > 0 || bundle.compareEnabled,
    data: bundle.data,
    buildOptions,
    // Grafana-parite #2 — rebuild: pin çözülür (tooltip DOM'u yaşar ama
    // içerik bayatlardı) + pin tık dinleyicisi taze u.over'a bağlanır;
    // visibleRef reset'i eski davranışın aynısı (resetSeriesVisibility'ye
    // delege — kaynak tek).
    afterBuild: u => {
      if (pinRef.current != null) unpinTooltip();
      visibleRef.current = resetSeriesVisibility(bundle.allSeries.length);
      attachPinListener(u);
    },
    // Grafana-parite M1 — çift-tık: sayfa geri-yığınına devret (spec canlı
    // okunur; verilmezse motor no-op, uPlot yerleşik autoscale'i sürer).
    onZoomReset,
    // Grafana-parite #2 — zoomlu fast-path y-refit'i yalnız GÖRÜNÜR serilerle
    // (OVC deseni; review 8/8 #5). Motor varsayılanı TÜM 1..n kolonlarını
    // katar ve izole edilmiş seride y-ölçeğini gizli serinin max'ına
    // fırlatırdı. Hepsi görünürken varsayılanın birebiri; index hizası:
    // u.data kolonu i ↔ visibleRef[i-1] (allSeries sırası, compare dahil).
    refitScales: (u, data) => {
      const idxs: number[] = [];
      for (let i = 1; i < data.length; i++) if (visibleRef.current[i - 1] !== false) idxs.push(i);
      if (idxs.length) u.setScale('y', yRefitScale(data as (number | null)[][], idxs));
    },
  }, themeTick);

  // Click-to-isolate: hide every other series on first click,
  // restore all on second. We bypass React state — toggling
  // the live plot's visibility via setSeries(idx, {show})
  // re-renders the canvas in <1ms without rebuilding.
  useEffect(() => {
    const el = hostRef.current;
    if (!el) return;
    const onClick = (e: MouseEvent) => {
      const target = e.target as HTMLElement;
      const li = target.closest('.u-legend tr.u-series') as HTMLElement | null;
      if (!li || !plotRef.current) return;
      // The first <tr> in the legend is the x-axis row; data
      // series start at index 1. Subtract 1 to land on a real
      // series.
      // Map the clicked legend row → data-series index ROBUSTLY. The
      // multi-value (Last/Min/Max/Avg) legend inserts a value-header row,
      // and uPlot may or may not render the x-series row depending on
      // version/live mode — both shift naive child-index math and would
      // isolate the WRONG operation. So index among ONLY tr.u-series
      // rows and derive the leading offset (1 if the x-row is present,
      // else 0) from the known data-series count.
      const seriesRows = Array.from(li.parentElement?.querySelectorAll('tr.u-series') ?? []);
      const offset = Math.max(0, seriesRows.length - labelsRef.current.length); // x-row present → 1
      const dataIdx = seriesRows.indexOf(li) - offset;
      if (dataIdx < 0) return;
      e.stopPropagation();
      e.preventDefault();

      // Controlled mode (sibling-synced legend): hand the click to the
      // parent, which recomputes the shared selection; every sibling
      // chart then re-applies it via the selectedOps effect below.
      // Mutating this plot locally would let the three charts drift.
      if (onLegendClickRef.current) {
        const label = labelsRef.current[dataIdx];
        if (label != null) onLegendClickRef.current(label, e.ctrlKey || e.metaKey);
        return;
      }

      const u = plotRef.current;
      const visible = visibleRef.current;
      // v0.5.364 — Ctrl/Cmd+click is additive (flip THIS series only, so an
      // operator can hand-pick a subset); plain click stays isolate-on-click
      // (second click on the isolated one restores all). Both iterate the
      // FULL visible[] (current + compare — the v0.5.364 fix).
      // Grafana-parite #2 — karar artık paylaşımlı legendVisibility
      // çekirdeğinde (aynı semantik + "hepsi gizliyse hepsini geri getir"
      // kuralı additive uçta); uygulama eskisi gibi setSeries, rebuild yok.
      const additive = e.ctrlKey || e.metaKey;
      const next = additive
        ? toggleSeriesVisibility(visible, dataIdx)
        : isolateSeriesVisibility(visible, dataIdx);
      for (let i = 0; i < next.length; i++) {
        visible[i] = next[i];
        u.setSeries(i + 1, { show: next[i] });
      }
    };
    el.addEventListener('click', onClick, true);
    return () => el.removeEventListener('click', onClick, true);
    // v0.5.364 — also depend on compareSeries length so the
    // closed-over series/totalCount stays accurate when the
    // operator toggles compare on/off; pre-fix the handler kept
    // a stale length reference and click did nothing for the
    // appended compare rows. plotRef is useChartEngine's stable
    // ref (never re-identifies) → intentionally NOT a dep (v0.9.100).
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [series.length, compareSeries?.length]);

  // Apply the controlled selection to the live plot WITHOUT a rebuild
  // (zoom + cursor survive). Matches by operation label so sibling
  // charts with different folded top-8 sets still sync by name.
  useEffect(() => {
    const u = plotRef.current;
    if (!u) return;
    const labels = labelsRef.current;
    for (let i = 0; i < labels.length; i++) {
      const vis = selectedOps == null || selectedOps.has(labels[i]);
      u.setSeries(i + 1, { show: vis });
      if (visibleRef.current[i] != null) visibleRef.current[i] = vis;
    }
    // plotRef = useChartEngine'in stabil ref'i → deps'e girmez (v0.9.100).
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectedOps]);

  // Container does NOT pin its height — uPlot creates a canvas
  // of `height` pixels plus a legend table beneath it, and we
  // want the component to take whatever total vertical space
  // chart+legend need.
  //
  // The .uplot-tooltip child is the Grafana-style floating
  // panel updated by the setCursor hook above. opacity:0 by
  // default; the hook flips it on when the cursor enters the
  // chart and updates content + position on every move.
  return (
    <div ref={hostRef} className="mlc-chart" style={{
      position: 'relative', width: '100%',
      // Subtle "this chart is clickable" affordance — only when
      // the spike→exemplar hook is wired (opt-in). Absent the
      // prop, no cursor override (identical to prior behaviour).
      cursor: onBucketClick ? 'pointer' : undefined,
    }}>
      {onBucketClick && (
        <div style={{
          position: 'absolute', top: 4, right: 6, zIndex: 6,
          fontSize: 9, color: 'var(--text3)', pointerEvents: 'none',
          textTransform: 'uppercase', letterSpacing: 0.4, opacity: 0.7,
        }}>
          click → exemplar
        </div>
      )}
      <div className="uplot-tooltip" style={{
        // Theme-aware tokens — the previous hardcoded
        // rgba(20,24,30) painted dark on dark in dark mode
        // and dark-on-light (text invisible) in light mode.
        // var(--bg2) + var(--text) flip with [data-theme].
        position: 'absolute', pointerEvents: 'none',
        background: 'var(--bg2)',
        border: '1px solid var(--border)',
        borderRadius: 4,
        padding: '8px 10px',
        fontSize: 11, color: 'var(--text)',
        opacity: 0, transition: 'opacity .08s',
        zIndex: 5,
        boxShadow: '0 4px 14px rgba(0,0,0,0.35)',
        maxWidth: 320,
      }} />
      {/* Y-axis crosshair pill — pinned to the left edge,
          updated in setCursor. Reads the cursor's exact y
          value (not snapped to a series datum) so the
          operator can answer "what is this y-coordinate" at
          a glance. */}
      <div className="uplot-ypill" style={{
        position: 'absolute', pointerEvents: 'none',
        left: 4, transform: 'translateY(-50%)',
        background: 'var(--bg3)',
        border: '1px solid var(--border)',
        borderRadius: 3,
        padding: '1px 5px',
        fontSize: 10, color: 'var(--text)',
        fontFamily: 'ui-monospace, monospace',
        opacity: 0, transition: 'opacity .08s',
        zIndex: 6,
        whiteSpace: 'nowrap',
      }} />
    </div>
  );
}
