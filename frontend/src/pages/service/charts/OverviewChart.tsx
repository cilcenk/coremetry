import { useEffect, useMemo, useRef } from 'react';
import uPlot from 'uplot';
import 'uplot/dist/uPlot.min.css';
import { useThemeTick } from '@/lib/useThemeTick';
import { overviewChartBuildSignature } from '@/lib/chartBuildSig';

// OverviewChart (v0.7.94) — the compact RED chart for the Service Overview.
// A purpose-built uPlot wrapper matching the design handoff: ~150px, clean
// (no axes chrome beyond 0/50/100 gridlines), a dashed-purple deploy marker
// with a ▼ flag, and a hover crosshair + per-series tooltip. Replaces the
// reuse of the full MultiLineChart, which is built for full-width detail
// panels and threw a uPlot ResizeObserver/teardown race when squeezed into
// the 3-column card grid.
//
// Robustness: the ResizeObserver callback bails if the instance was
// destroyed (ref nulled on cleanup), which is what the MultiLineChart reuse
// tripped on under StrictMode's double-mount in a 0-width card.
//
// v0.8.531 (perf #5/#15) — rebuild-vs-setData split. The build effect keyed on
// `times`/`series`, so every 30s RED poll destroyed + recreated the uPlot
// (canvas flicker, dropped hover). Now it keys on a pure STRUCTURE signature
// (series shape + mode + unit + deploy + height) and the theme tick; a
// data-only refresh rides u.setData(). The y range + splits re-fit from the
// LIVE scale so setData re-scales exactly as the old rebuild did; the DOM ▼
// flag is repositioned on the fast-path so it tracks the shifted window.

export interface OvChartSeries {
  label: string;
  color: string;  // a CSS var() string, resolved at draw time
  data: number[];
}

interface Props {
  times: number[];            // unix seconds, ascending — shared x axis
  series: OvChartSeries[];
  height?: number;            // default 150
  mode?: 'line' | 'area' | 'stacked';
  unit?: string;              // " ms", "%", " req/s" …
  deployAtSec?: number | null; // deploy time (unix sec) → dashed vline + flag
  deployLabel?: string;       // e.g. "v1.0.0"
  // v0.8.534 — drag-select zoom → parent range. Fires (fromSec, toSec)
  // on release of a horizontal selection; the parent maps it to the
  // global ?range= so EVERY Overview chart + the top picker sync (mirrors
  // MultiLineChart / ServiceCharts). Absent → uPlot's default local
  // setScale zoom (isolated), the pre-v0.8.534 behaviour.
  onZoom?: (fromSec: number, toSec: number) => void;
}

function cssVar(v: string): string {
  // Resolve a var(--x) token to its computed value for canvas strokes.
  const m = /^var\((--[\w-]+)\)$/.exec(v.trim());
  if (!m) return v;
  return getComputedStyle(document.documentElement).getPropertyValue(m[1]).trim() || v;
}

// y range derived from the LIVE data extremes (0-based, 10% headroom, floor 1)
// — a function so u.setData() re-fits the axis on the fast-path. In stacked
// mode uPlot's auto dataMax is the top cumulative line (largest layer), which
// matches the old explicit "max of cum[last]".
function yRange(_u: uPlot, _min: number, max: number): [number, number] {
  return [0, max > 0 ? max * 1.1 : 1];
}

export function OverviewChart({
  times, series, height = 150, mode = 'line', unit = '', deployAtSec = null, deployLabel = 'deploy', onZoom,
}: Props) {
  const hostRef = useRef<HTMLDivElement>(null);
  // onZoom in a ref (v0.8.520 pattern) so the once-per-build setSelect hook
  // always calls the latest without re-registering (identity-stable deps).
  const onZoomRef = useRef(onZoom); onZoomRef.current = onZoom;
  const ttRef = useRef<HTMLDivElement>(null);
  const flagRef = useRef<HTMLDivElement>(null);
  const plotRef = useRef<uPlot | null>(null);
  // Re-resolve CSS-var stroke/grid colors when the theme flips (cssVar()
  // bakes concrete hex at build time, so the canvas would otherwise stay
  // stale on light↔dark toggle until remount).
  const themeTick = useThemeTick();

  // uPlot aligned data (stacked-aware) — memoised on the data inputs + mode so a
  // poll recomputes once and either seeds a rebuild or rides setData. The
  // tooltip reads RAW per-series values, so keep the raw series in a ref too.
  const built = useMemo(() => {
    const stacked = mode === 'stacked';
    let matrix: number[][];
    if (stacked) {
      const cum: number[][] = [];
      for (let i = 0; i < series.length; i++) {
        const below = cum[i - 1];
        cum[i] = series[i].data.map((v, j) => (below ? below[j] : 0) + (v ?? 0));
      }
      matrix = cum;
    } else {
      matrix = series.map(s => s.data);
    }
    return { data: [times, ...matrix] as uPlot.AlignedData };
  }, [times, series, mode]);
  const builtRef = useRef(built); builtRef.current = built;
  // Raw series for the tooltip (stacked draws cumulative into u.data, so the
  // tooltip must read the untransformed values); fresh on the fast-path.
  const rawRef = useRef({ series }); rawRef.current = { series };
  // Repositions the DOM ▼ flag; assigned in the build effect, called by the
  // fast-path so the flag follows the window without a rebuild.
  const placeFlagRef = useRef<() => void>(() => {});

  // Build signature — series shape + mode + unit + deploy + height. Point VALUES
  // ride setData; `renderable` (≥2 x points) flips the sig for empty→data.
  const buildSig = overviewChartBuildSignature({
    series,
    height, mode, unit, deployAtSec, deployLabel,
    renderable: times.length >= 2 && series.length > 0,
    hasZoom: !!onZoom,
  });

  useEffect(() => {
    const el = hostRef.current;
    if (!el || times.length < 2 || series.length === 0) return;

    const colors = series.map(s => cssVar(s.color));
    const gridc = cssVar('var(--border)');
    const text3 = cssVar('var(--text3)');
    const purple = cssVar('var(--purple)');

    const stacked = mode === 'stacked';

    // Dashed-purple deploy marker, drawn under the series (re-paints on every
    // redraw incl. setData, so the canvas line tracks the live x-scale).
    const deployPlugin: uPlot.Plugin = {
      hooks: {
        draw: u => {
          if (deployAtSec == null) return;
          const ctx = u.ctx;
          const x = Math.round(u.valToPos(deployAtSec, 'x', true));
          if (x < u.bbox.left || x > u.bbox.left + u.bbox.width) return;
          ctx.save();
          ctx.strokeStyle = purple;
          ctx.globalAlpha = 0.8;
          ctx.lineWidth = 1.4 * devicePixelRatio;
          ctx.setLineDash([4 * devicePixelRatio, 3 * devicePixelRatio]);
          ctx.beginPath();
          ctx.moveTo(x, u.bbox.top);
          ctx.lineTo(x, u.bbox.top + u.bbox.height);
          ctx.stroke();
          ctx.restore();
        },
      },
    };

    const opts: uPlot.Options = {
      width: el.clientWidth || 320,
      height,
      cursor: {
        x: true, y: false, points: { show: true, size: 7 },
        // v0.8.534 — x-only drag-zoom with instant local rescale (setScale
        // preserves the pre-v0.8.534 default; the Incident impact chart, which
        // passes no onZoom, keeps its standalone zoom). When onZoom IS wired
        // (Overview), the setSelect hook below ALSO propagates the range to the
        // page so the sibling charts + global picker re-sync via ?range=,
        // mirroring MultiLineChart / ServiceCharts.
        drag: { x: true, y: false, setScale: true },
      },
      legend: { show: false },
      scales: { x: { time: true }, y: { range: yRange } },
      axes: [
        { stroke: text3, grid: { show: false }, ticks: { show: false }, size: 22, font: '10px ui-monospace, monospace' },
        {
          stroke: text3, size: 34, font: '10px ui-monospace, monospace',
          grid: { stroke: gridc, width: 1, dash: [3, 4] },
          ticks: { show: false },
          // splits + decimal count derive from the LIVE scale max so a setData
          // re-fit updates the gridlines (the old build-time `max` went stale).
          splits: u => { const mx = u.scales.y.max ?? 1; return [0, mx / 2, mx]; },
          values: (u, sp) => { const mx = u.scales.y.max ?? 1; return sp.map(v => (v >= 1000 ? `${(v / 1000).toFixed(1)}k` : v.toFixed(mx < 10 ? 1 : 0))); },
        },
      ],
      series: [
        {},
        ...series.map((s, i) => ({
          label: s.label,
          stroke: colors[i],
          width: 1.8,
          points: { show: false },
          // area → gradient fill to baseline; stacked → only the BOTTOM
          // series fills to baseline (flat translucent), the rest are filled
          // by the bands between adjacent cumulative lines below.
          ...(mode === 'area'
            ? { fill: (u: uPlot, si: number) => {
                const ctx = u.ctx;
                const g = ctx.createLinearGradient(0, u.bbox.top, 0, u.bbox.top + u.bbox.height);
                g.addColorStop(0, colors[si - 1] + '47');  // ~28% alpha
                g.addColorStop(1, colors[si - 1] + '00');
                return g;
              } }
            : stacked && i === 0
            ? { fill: colors[0] + '47' }
            : {}),
        })),
      ],
      // Stacked bands: fill between cumulative line k and k-1 in series k's
      // colour (uPlot series are 1-based; data series i is uPlot series i+1).
      bands: stacked
        ? series.slice(1).map((_s, k) => ({ series: [k + 2, k + 1] as [number, number], fill: colors[k + 1] + '47' }))
        : undefined,
      hooks: {
        // v0.8.534 — drag-zoom release → hand the selected [from,to] (unix
        // sec) to the parent, which updates ?range=; reset the grey band so
        // it doesn't stick. Only registered when onZoom is set.
        setSelect: onZoom ? [
          (u: uPlot) => {
            const sel = u.select;
            if (!sel || sel.width < 4) return; // tiny accidental drag
            const x0 = u.posToVal(sel.left, 'x');
            const x1 = u.posToVal(sel.left + sel.width, 'x');
            if (!isFinite(x0) || !isFinite(x1)) return;
            onZoomRef.current?.(Math.min(x0, x1), Math.max(x0, x1));
            u.setSelect({ left: 0, width: 0, top: 0, height: 0 }, false);
          },
        ] : undefined,
        setCursor: [
          u => {
            const tt = ttRef.current;
            if (!tt) return;
            const idx = u.cursor.idx;
            if (idx == null || u.cursor.left == null || u.cursor.left < 0) { tt.style.display = 'none'; return; }
            const xs = u.data[0] as number[];
            const tSec = xs[idx] as number;
            if (tSec == null) { tt.style.display = 'none'; return; }
            const mx = u.scales.y.max ?? 1;
            // Read RAW values from the ref (stacked draws cumulative into u.data)
            // — fresh on the fast-path; labels/colours are structural (close over).
            const raw = rawRef.current.series;
            const rows = series.map((s, i) =>
              `<div class="ov-tt-r"><span class="ov-lbl"><i class="ov-sw" style="background:${colors[i]}"></i>${s.label}</span><b>${(raw[i]?.data[idx] ?? 0).toFixed(mx < 10 ? 2 : 0)}${unit}</b></div>`,
            ).join('');
            const ts = new Date(tSec * 1000).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
            tt.innerHTML = `<div class="ov-tt-t">${ts}</div>${rows}`;
            tt.style.display = 'block';
            tt.style.left = `${u.cursor.left}px`;
            tt.style.top = `${Math.max(8, (u.cursor.top ?? 20))}px`;
          },
        ],
      },
      plugins: [deployPlugin],
    };

    plotRef.current?.destroy();
    plotRef.current = new uPlot(opts, builtRef.current.data, el);

    // Position the ▼ deploy flag (DOM, above the canvas) at the marker x.
    const placeFlag = () => {
      const u = plotRef.current, flag = flagRef.current;
      if (!u || !flag) return;
      if (deployAtSec == null) { flag.style.display = 'none'; return; }
      const x = u.valToPos(deployAtSec, 'x', false);
      if (x < 0 || x > u.over.clientWidth) { flag.style.display = 'none'; return; }
      flag.style.display = 'block';
      flag.style.left = `${x}px`;
    };
    placeFlagRef.current = placeFlag;
    placeFlag();

    const ro = new ResizeObserver(() => {
      // Bail if the instance was torn down (StrictMode double-mount / unmount
      // race in a 0-width card) — calling setSize on a destroyed uPlot is
      // what threw "Cannot read properties of undefined (reading 'forEach')".
      const u = plotRef.current;
      if (!u || !el.clientWidth) return;
      u.setSize({ width: el.clientWidth, height });
      placeFlag();
    });
    ro.observe(el);

    return () => {
      ro.disconnect();
      plotRef.current?.destroy();
      plotRef.current = null;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [buildSig, themeTick]);

  // Data fast-path (v0.8.531) — a poll changes only the point VALUES, not the
  // series shape/mode/unit/deploy, so buildSig is unchanged and the build effect
  // does NOT run. Push the fresh (stacked-aware) data with setData() (resetScales
  // default true → the range function re-fits y) and reposition the DOM ▼ flag.
  // Guard on column count so a series add/remove stays a rebuild.
  useEffect(() => {
    const u = plotRef.current;
    if (!u) return;
    if (u.data.length !== built.data.length) return;
    u.setData(built.data);
    placeFlagRef.current();
  }, [built]);

  return (
    <div className="ov-chart-wrap" style={{ position: 'relative' }}>
      <div ref={hostRef} style={{ width: '100%' }} />
      <div ref={ttRef} className="ov-tt" style={{ display: 'none' }} />
      {deployAtSec != null && (
        <div ref={flagRef} className="ov-deploy-flag" style={{ top: 0, display: 'none' }}>▼ {deployLabel}</div>
      )}
    </div>
  );
}
