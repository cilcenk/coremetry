import { useEffect, useMemo, useRef } from 'react';
import uPlot from 'uplot';
import 'uplot/dist/uPlot.min.css';
import { escapeHTML } from '@/lib/utils';
import type { SpanMetricSeries } from '@/lib/types';
import { fmtSmart, fmtXTicks, seriesColor } from '@/lib/chartFmt';
import { placeTooltip } from '@/lib/chartTooltip';
import { useThemeTick } from '@/lib/useThemeTick';
import { chartBuildSignature } from '@/lib/chartBuildSig';

// Re-exported so existing importers (TimeSeriesPanel) keep working after the
// placement logic moved into the pure, unit-tested lib/chartTooltip module.
export { placeTooltip } from '@/lib/chartTooltip';

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
): ChartData {
  const compareEnabled = (compareSeries?.length ?? 0) > 0 && (compareOffsetNs ?? 0) > 0;
  // Fold the long tail into "others" so >8-series charts stay legible.
  const eff = foldTopN(series, unit);

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
  series, unit, height = 320, deploys, thresholds, syncKey, onZoom,
  compareSeries, compareOffsetNs, compareLabel, logScale, onBucketClick, colorOf,
  selectedOps, onLegendClick,
}: {
  series: SpanMetricSeries[];
  unit?: string;
  height?: number;
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
}) {
  const containerRef = useRef<HTMLDivElement>(null);
  const plotRef = useRef<uPlot | null>(null);

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

  // Theme tick — a data-theme toggle must re-resolve the canvas CSS-var colors,
  // which only happens on a rebuild. So the theme counter is a BUILD dep (never
  // the setData fast-path): a toggle re-creates the plot with fresh hex.
  const themeTick = useThemeTick();

  // Memoised data bundle — the single prep pass feeding BOTH the build effect
  // and the setData fast-path. Keyed on the data inputs only, so a poll (fresh
  // `series` identity) recomputes it exactly once. bundleRef exposes the live
  // bundle to the build effect's once-registered hooks without listing data in
  // the build deps (which would defeat the fast-path).
  const bundle = useMemo(
    () => computeChartData(series, unit, compareSeries, compareOffsetNs),
    [series, unit, compareSeries, compareOffsetNs],
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
    deploys, thresholds,
  });

  // Build / re-create effect (v0.8.520). Runs ONLY when the build signature,
  // the colorOf override, or the theme flips — NOT on a data-only poll (that
  // rides the setData fast-path below). Reads the current data bundle through
  // bundleRef so a rebuild always paints fresh data without listing it as a dep.
  useEffect(() => {
    const el = containerRef.current;
    if (!el) return;
    plotRef.current?.destroy();
    plotRef.current = null;
    const { eff, allSeries, data, compareEnabled } = bundleRef.current;
    // visibleRef tracks BOTH current and compare series so the legend toggle
    // still works on either set (compare lines sit after current lines). A
    // rebuild resets isolation to all-visible; the data-only fast-path
    // deliberately preserves it (setData keeps series `show` flags), so an
    // isolated view now survives a 30s poll.
    visibleRef.current = allSeries.map(() => true);
    if (eff.length === 0 && !compareEnabled) return;

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
      if (cnt === 0) return { Last: '—', Min: '—', Max: '—', Avg: '—' };
      return { Last: fmt1(last as number), Min: fmt1(mn), Max: fmt1(mx), Avg: fmt1(sum / cnt) };
    };

    const opts: uPlot.Options = {
      // clientWidth is sometimes 0 at first paint (StrictMode
      // double-mount or display:none parent). Fall back to
      // 600px so the initial render still produces a visible
      // canvas; the ResizeObserver below corrects the width on
      // the next layout pass.
      width: el.clientWidth || 600,
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
        x: { time: true },
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
        // Drag-zoom: x-axis only. uPlot's built-in select
        // mechanism + setScale=true handles the visual zoom;
        // onZoom (below, in hooks.setSelect) propagates the
        // chosen range to the page so it can update its
        // TimeRange and re-fetch. Click-only behaviour is
        // unchanged (drag is from > 5 px movement).
        drag: { x: true, y: false, setScale: !!onZoom },
        // Sync cursors across charts that share a key. Two
        // chart instances on the same page with the same
        // syncKey will paint the same crosshair simultaneously
        // when the operator hovers either one — Grafana /
        // Datadog dashboard pattern.
        sync: syncKey ? { key: syncKey } : undefined,
      },
      // live:false — the columns are range stats (Last/Min/Max/Avg via
      // series.values), not the cursor value; the floating tooltip is the
      // live readout. The legend re-renders on zoom so stats stay scoped
      // to the visible window.
      legend: { show: true, live: false, markers: { width: 2 } },
      hooks: {
        // Drag-zoom callback — fires when the operator
        // releases a horizontal selection. Convert from uPlot
        // pixel select to data-space (unix seconds) and hand
        // off to the page; the page is responsible for
        // updating its time-range state and refetching.
        setSelect: onZoom ? [
          (u) => {
            const sel = u.select;
            if (!sel || sel.width < 4) return; // ignore tiny accidental drags
            const x0 = u.posToVal(sel.left, 'x');
            const x1 = u.posToVal(sel.left + sel.width, 'x');
            if (!isFinite(x0) || !isFinite(x1)) return;
            onZoomRef.current?.(Math.min(x0, x1), Math.max(x0, x1));
            // Reset the visual selection after the parent
            // takes over the new range — otherwise the grey
            // band sticks around until the next click.
            u.setSelect({ left: 0, width: 0, top: 0, height: 0 }, false);
          },
        ] : [],
        // Spike → exemplar click hook (v0.7.22). OPT-IN: only
        // registered when onBucketClick is set, so non-exemplar
        // callers are untouched. Attaches a plain-click listener
        // to uPlot's `over` element once at build time; resolves
        // the clicked time to the enclosing bucket window in ns
        // and hands it off. A click that's really the tail of a
        // drag-zoom (select width > 4px) is ignored so the two
        // gestures don't fight.
        ready: onBucketClick ? [
          (u) => {
            u.over.addEventListener('click', () => {
              if (u.select && u.select.width > 4) return; // was a drag, not a click
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
        // Overlay draw hooks — paint deploy markers (dashed
        // vertical lines) AND threshold lines (dashed horizontal
        // lines + tinted breach band) after uPlot's own series
        // render so the overlays sit on top of fills. Combined
        // into a single hook so we only register one handler.
        draw: (deploys && deploys.length > 0) || (thresholds && thresholds.length > 0) ? [
          (u) => {
            const ctx = u.ctx;
            ctx.save();

            // ── Threshold lines ──────────────────────────────
            //
            // For each threshold, paint a horizontal dashed line
            // and a faint tinted band ABOVE the line (the
            // "breach zone"). Severity = warn → amber, err →
            // red. Label sits at the right edge.
            if (thresholds && thresholds.length > 0) {
              const yMin = u.scales.y.min ?? 0;
              const yMax = u.scales.y.max ?? 0;
              ctx.font = '10px ui-monospace, monospace';
              for (const th of thresholds) {
                if (th.value < yMin || th.value > yMax) continue;
                const colour = th.severity === 'err' ? errCol : warnCol;
                const y = u.valToPos(th.value, 'y', true);
                // Tinted breach band above the line — token color at 7% via
                // globalAlpha (canvas fillStyle can't color-mix reliably).
                ctx.globalAlpha = 0.07;
                ctx.fillStyle = colour;
                ctx.fillRect(u.bbox.left, u.bbox.top, u.bbox.width, y - u.bbox.top);
                ctx.globalAlpha = 1;
                // The line itself.
                ctx.strokeStyle = colour;
                ctx.fillStyle = colour;
                ctx.lineWidth = 1.2;
                ctx.setLineDash([6, 4]);
                ctx.beginPath();
                ctx.moveTo(u.bbox.left, y);
                ctx.lineTo(u.bbox.left + u.bbox.width, y);
                ctx.stroke();
                // Label at the right edge.
                if (th.label) {
                  ctx.setLineDash([]);
                  const labelW = ctx.measureText(th.label).width;
                  ctx.fillText(
                    th.label,
                    u.bbox.left + u.bbox.width - labelW - 4,
                    y - 4,
                  );
                }
              }
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
            // Build per-series rows. Skip null values so
            // gaps in one series don't push down rows from
            // others. Sort by value desc so the hottest
            // series shows on top — same behaviour we had
            // in the Chart.js tooltip.
            type Row = { label: string; color: string; v: number };
            const rows: Row[] = [];
            // Live eff — the fast-path may have swapped the data without a
            // rebuild. Count + labels are guaranteed unchanged by the build
            // signature, so this only ever reads the current window's groupKeys
            // while u.data supplies the fresh values.
            const effRows = bundleRef.current.eff;
            for (let i = 0; i < effRows.length; i++) {
              const yArr = u.data[i + 1];
              if (!yArr) continue;
              const v = yArr[idx];
              if (v == null) continue;
              const s = effRows[i];
              const label = s.groupKey.length ? s.groupKey.join(' / ') : 'value';
              rows.push({ label, color: colorFor(label), v: v as number });
            }
            if (rows.length === 0) {
              tip.style.opacity = '0';
              return;
            }
            rows.sort((a, b) => b.v - a.v);
            const fmt = (n: number) => fmtSmart(n, unit);
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
                const dy = Math.abs(r.v - yAtCursor);
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
                  `<span style="font-family:ui-monospace,monospace;font-variant-numeric:tabular-nums">${fmt(r.v)}</span>` +
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

    plotRef.current = new uPlot(opts, data, el);

    const ro = new ResizeObserver(() => {
      if (plotRef.current && el) {
        // height stays fixed (the prop). Width tracks the
        // parent — sidebar collapse, browser resize, dashboard
        // panel re-grid all flow through here without a
        // chart rebuild.
        plotRef.current.setSize({ width: el.clientWidth, height });
      }
    });
    ro.observe(el);

    return () => {
      ro.disconnect();
      plotRef.current?.destroy();
      plotRef.current = null;
    };
    // Deps (v0.8.520): the pure build signature (series structure + unit +
    // height + overlays + zoom/bucket PRESENCE + compare alignment), plus the
    // colorOf override identity and the theme tick. Everything callback-shaped
    // (onZoom / onBucketClick / onLegendClick) is tracked by presence only and
    // read live through refs, so a fresh arrow each render never churns a
    // rebuild. Series DATA POINTS are absent on purpose — they ride the setData
    // fast-path below.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [buildSig, colorOf, themeTick]);

  // Data fast-path (v0.8.520 / proposal #15). A 30s poll changes only the
  // series' point VALUES, not their count/labels/options, so buildSig is
  // unchanged and the build effect above does NOT run. Here we push the fresh
  // data into the LIVE plot with setData() — the same <1ms redraw path
  // click-to-isolate uses — instead of destroy()+new uPlot() (~30ms of canvas
  // flicker that dropped the hover cursor and reset isolation every 30s).
  // Guard on column count: a count change means the build effect is
  // (re)creating the plot this same commit and owns the data; setData() with a
  // mismatched width would throw. resetScales stays uPlot's default (true) so
  // the y-axis auto-refits exactly as the old rebuild did.
  useEffect(() => {
    const u = plotRef.current;
    if (!u) return;
    if (u.data.length !== bundle.data.length) return;
    u.setData(bundle.data);
  }, [bundle]);

  // Click-to-isolate: hide every other series on first click,
  // restore all on second. We bypass React state — toggling
  // the live plot's visibility via setSeries(idx, {show})
  // re-renders the canvas in <1ms without rebuilding.
  useEffect(() => {
    const el = containerRef.current;
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
      // v0.5.364 — Ctrl/Cmd+click is additive: flip THIS series'
      // visibility without touching the others, so an operator
      // can hand-pick a subset (e.g. "show only ops A + C of 12").
      // Plain click stays isolate-on-click for the one-line view
      // operators reach for most often.
      const additive = e.ctrlKey || e.metaKey;
      if (additive) {
        const next = !visible[dataIdx];
        visible[dataIdx] = next;
        u.setSeries(dataIdx + 1, { show: next });
        return;
      }
      // Decide: are we currently in "all visible" or "isolated
      // to one"? If the clicked one is the only visible (or
      // the click is on a hidden series), we're in isolation
      // territory and the next click restores everything.
      // v0.5.364 — iterate the full visible[] (current +
      // compare). Pre-fix the loop stopped at series.length so
      // a compare-on isolate left every compare line visible
      // alongside the isolated current one.
      const totalCount = visible.length;
      const onlyThisVisible = visible[dataIdx]
        && visible.every((v, i) => i === dataIdx ? true : !v);
      if (onlyThisVisible) {
        for (let i = 0; i < totalCount; i++) {
          visible[i] = true;
          u.setSeries(i + 1, { show: true });
        }
      } else {
        for (let i = 0; i < totalCount; i++) {
          const show = i === dataIdx;
          visible[i] = show;
          u.setSeries(i + 1, { show });
        }
      }
    };
    el.addEventListener('click', onClick, true);
    return () => el.removeEventListener('click', onClick, true);
    // v0.5.364 — also depend on compareSeries length so the
    // closed-over series/totalCount stays accurate when the
    // operator toggles compare on/off; pre-fix the handler kept
    // a stale length reference and click did nothing for the
    // appended compare rows.
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
    <div ref={containerRef} className="mlc-chart" style={{
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
