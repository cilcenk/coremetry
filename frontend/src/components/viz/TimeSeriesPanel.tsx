import { useEffect, useMemo, useRef, useState } from 'react';
import uPlot from 'uplot';
import 'uplot/dist/uPlot.min.css';
import { downsampleXY } from '@/lib/perf/lttb';
import { fmtSmart, fmtXTicks, seriesColor } from '@/lib/chartFmt';
import { escapeHTML } from '@/lib/utils';
import { placeTooltip } from '@/components/MultiLineChart';

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

const MAX_POINTS = 2000;

export interface TSSeries {
  label: string;
  points: { time: number /* ns */; value: number | null }[];
  color?: string;          // CSS colour override; falls back to stable seriesColor(label)
  unit?: string;           // per-series unit (drives the axis it's bound to)
  axis?: 'left' | 'right'; // which y-axis this series reads against (default 'left')
  dash?: number[];         // canvas dash pattern (explore-v2: the formula series)
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
  thresholds?: TSThreshold[];
  height: number;
  mode?: TSMode;               // default 'line'
  logScale?: boolean;          // log10 on the y-axes
  syncKey?: string;            // shared cursor key across panels
  // Optional drag-zoom propagation. Receives unix SECONDS. When omitted the
  // drag still zooms visually (setScale) and double-click resets.
  onZoom?: (fromUnixSec: number, toUnixSec: number) => void;
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
}

// Resolve a var(--x) token (or a raw colour) to a concrete hex/rgb for canvas
// strokes. uPlot draws to a 2D canvas which can't read CSS vars directly.
function resolveColor(c: string): string {
  const m = /^var\((--[\w-]+)\)$/.exec(c.trim());
  if (!m) return c;
  return getComputedStyle(document.documentElement).getPropertyValue(m[1]).trim() || c;
}

// Append an alpha byte to a #rrggbb colour for fills. Non-hex colours pass
// through unchanged (uPlot still strokes/fills, just opaque-ish — acceptable).
function withAlpha(hex: string, aa: string): string {
  return /^#[0-9a-fA-F]{6}$/.test(hex) ? hex + aa : hex;
}

// toHex — coerce a resolved colour to #rrggbb (for the breach-band tint), else ''.
function toHex(c: string): string {
  return /^#[0-9a-fA-F]{6}$/.test(c.trim()) ? c.trim() : '';
}

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
  series, deploys, thresholds, height, mode = 'line', logScale, syncKey, onZoom, hideLegend,
  zoomWindow, hiddenLabels, focusedLabel, onCursorTime,
}: TimeSeriesPanelProps) {
  const containerRef = useRef<HTMLDivElement>(null);
  const plotRef = useRef<uPlot | null>(null);
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
  const hiddenRef = useRef(hiddenLabels);
  hiddenRef.current = hiddenLabels;
  const focusedRef = useRef(focusedLabel);
  focusedRef.current = focusedLabel;
  // Held in a ref so the cursor publish never enters the rebuild dep list —
  // a 60fps mousemove path must not rebuild the chart.
  const cursorTimeRef = useRef(onCursorTime);
  cursorTimeRef.current = onCursorTime;

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

  useEffect(() => {
    const el = containerRef.current;
    if (!el) return;
    plotRef.current?.destroy();
    plotRef.current = null;

    visibleRef.current = series.map(s => !(hiddenRef.current?.has(s.label)));

    const { times, ySeries } = prepared;
    if (series.length === 0 || times.length === 0) return;

    const css = getComputedStyle(document.documentElement);
    const text3 = css.getPropertyValue('--text3').trim() || '#484f58';
    const grid = css.getPropertyValue('--bg2').trim() || '#21262d';

    // ── Stacked transform ────────────────────────────────────────────────
    // Plot cumulative running sums; fill the band between adjacent cumulative
    // lines. Tooltip + legend still read RAW values (we keep ySeries around).
    // A null in a stacked layer counts as 0 for the sum so a gap in one layer
    // doesn't punch through the layers above it.
    const stacked = mode === 'stacked';
    const cum: (number | null)[][] = [];
    if (stacked) {
      for (let i = 0; i < ySeries.length; i++) {
        const below = cum[i - 1];
        cum[i] = ySeries[i].map((v, j) => (below?.[j] ?? 0) + (v ?? 0));
      }
    }

    const xs = times;
    const drawMatrix = stacked ? cum : ySeries;
    const data: uPlot.AlignedData = [xs, ...drawMatrix] as uPlot.AlignedData;

    const colors = series.map(s => resolveColor(s.color ?? seriesColor(s.label)));
    const yScaleKey = (s: TSSeries) => (s.axis === 'right' ? 'yr' : 'y');

    const opts: uPlot.Options = {
      width: el.clientWidth || 600,
      height,
      scales: {
        x: { time: true },
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
        drag: { x: true, y: false, setScale: true },
        sync: syncKey ? { key: syncKey } : undefined,
      },
      legend: { show: false }, // our own interactive legend table renders below
      focus: { alpha: 0.35 }, // dim non-focused series (cursor prox + focusedLabel)
      // NOTE: uPlot's fire() does `if (evName in hooks)` — a key explicitly
      // set to undefined still passes the `in` check and crashes on
      // hooks[evName].forEach. Only set keys that actually have handlers.
      hooks: {
        ...(onZoom ? { setSelect: [
          (u) => {
            const sel = u.select;
            if (!sel || sel.width < 4) return;
            const x0 = u.posToVal(sel.left, 'x');
            const x1 = u.posToVal(sel.left + sel.width, 'x');
            if (!isFinite(x0) || !isFinite(x1)) return;
            onZoom(Math.min(x0, x1), Math.max(x0, x1));
            u.setSelect({ left: 0, width: 0, top: 0, height: 0 }, false);
          },
        ] } : {}),
        // Overlay draw — thresholds (line + breach band) then deploy annotations.
        ...(((thresholds && thresholds.length > 0) || (deploys && deploys.length > 0)) ? { draw: [
          (u) => {
            const ctx = u.ctx;
            ctx.save();

            if (thresholds && thresholds.length > 0) {
              const yMin = u.scales.y.min ?? 0;
              const yMax = u.scales.y.max ?? 0;
              ctx.font = '10px ui-monospace, monospace';
              for (const th of thresholds) {
                if (th.value < yMin || th.value > yMax) continue;
                const colour = resolveColor(th.color ?? 'var(--warn)') || '#f0b352';
                const y = u.valToPos(th.value, 'y', true);
                ctx.fillStyle = withAlpha(toHex(colour), '14') || 'rgba(240,179,82,0.07)';
                ctx.fillRect(u.bbox.left, u.bbox.top, u.bbox.width, y - u.bbox.top);
                ctx.strokeStyle = colour;
                ctx.fillStyle = colour;
                ctx.lineWidth = 1.2;
                ctx.setLineDash([6, 4]);
                ctx.beginPath();
                ctx.moveTo(u.bbox.left, y);
                ctx.lineTo(u.bbox.left + u.bbox.width, y);
                ctx.stroke();
                if (th.label) {
                  ctx.setLineDash([]);
                  const labelW = ctx.measureText(th.label).width;
                  ctx.fillText(th.label, u.bbox.left + u.bbox.width - labelW - 4, y - 4);
                }
              }
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
            if (idx == null || idx < 0) { tip.style.opacity = '0'; return; }
            const xVal = u.data[0][idx];
            if (xVal == null) { tip.style.opacity = '0'; return; }
            const d = new Date((xVal as number) * 1000);
            const hh = d.getHours().toString().padStart(2, '0');
            const mm = d.getMinutes().toString().padStart(2, '0');
            const ss = d.getSeconds().toString().padStart(2, '0');

            // Per-series rows from RAW values (stacked panels show real layer
            // value, not cumulative). Skip nulls + hidden series.
            type Row = { label: string; color: string; v: number; unit: string };
            const rows: Row[] = [];
            for (let i = 0; i < series.length; i++) {
              if (!visibleRef.current[i]) continue;
              const v = stacked ? ySeries[i][idx] : (u.data[i + 1] as (number | null)[])?.[idx];
              if (v == null) continue;
              rows.push({ label: series[i].label, color: colors[i], v: v as number, unit: series[i].unit ?? anyUnit });
            }
            if (rows.length === 0) { tip.style.opacity = '0'; return; }
            rows.sort((a, b) => b.v - a.v);

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

            tip.innerHTML =
              `<div style="font-weight:600;margin-bottom:4px">${hh}:${mm}:${ss}</div>` +
              deployRow +
              rows.map(r => {
                const lbl = escapeHTML(r.label);
                return `<div style="display:flex;gap:8px;align-items:center;line-height:1.5">` +
                  `<span style="display:inline-block;width:8px;height:8px;background:${r.color};border-radius:2px;flex-shrink:0"></span>` +
                  `<span style="flex:1;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;max-width:240px" title="${lbl}">${lbl}</span>` +
                  `<span style="font-family:ui-monospace,monospace;font-variant-numeric:tabular-nums">${fmtSmart(r.v, r.unit)}</span>` +
                `</div>`;
              }).join('');
            tip.style.opacity = '1';
            const { x, y } = placeTooltip(
              u.cursor.left ?? 0, u.cursor.top ?? 0,
              tip.offsetWidth, tip.offsetHeight,
              el.clientWidth, height,
            );
            tip.style.left = `${x}px`;
            tip.style.top = `${y}px`;
          },
        ],
      },
    };

    plotRef.current = new uPlot(opts, data, el);

    // Re-apply parent-controlled zoom + focus onto the fresh uPlot (the
    // controlled-prop effects below only fire when the PROPS change).
    if (zoomRef.current) {
      plotRef.current.setScale('x', { min: zoomRef.current.from, max: zoomRef.current.to });
    }
    if (focusedRef.current != null) {
      const fi = series.findIndex(s => s.label === focusedRef.current);
      if (fi >= 0) plotRef.current.setSeries(fi + 1, { focus: true });
    }

    const ro = new ResizeObserver(() => {
      const u = plotRef.current;
      if (u && el.clientWidth) u.setSize({ width: el.clientWidth, height });
    });
    ro.observe(el);

    return () => {
      ro.disconnect();
      plotRef.current?.destroy();
      plotRef.current = null;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [series, prepared, height, mode, logScale, syncKey, !!onZoom, deploys, thresholds, hasRight, leftUnit, rightUnit, anyUnit]);

  // Double-click resets the zoom to the full data range (Grafana parity).
  useEffect(() => {
    const el = containerRef.current;
    if (!el) return;
    const onDbl = () => {
      const u = plotRef.current;
      if (!u) return;
      const xs = u.data[0];
      if (!xs || xs.length === 0) return;
      u.setScale('x', { min: xs[0] as number, max: xs[xs.length - 1] as number });
    };
    el.addEventListener('dblclick', onDbl);
    return () => el.removeEventListener('dblclick', onDbl);
  }, []);

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
  }, [focusedLabel, series]);

  // Legend interaction — isolate-on-click (second click restores all);
  // Ctrl/Cmd-click toggles only that series. Bypasses React state for the
  // chart; bumps visTick so the legend re-paints its dim state.
  const toggleSeries = (dataIdx0: number, additive: boolean) => {
    const u = plotRef.current;
    const visible = visibleRef.current;
    if (!u) return;
    if (additive) {
      const next = !visible[dataIdx0];
      visible[dataIdx0] = next;
      u.setSeries(dataIdx0 + 1, { show: next });
    } else {
      const onlyThisVisible = visible[dataIdx0] && visible.every((v, i) => (i === dataIdx0 ? true : !v));
      if (onlyThisVisible) {
        for (let i = 0; i < visible.length; i++) { visible[i] = true; u.setSeries(i + 1, { show: true }); }
      } else {
        for (let i = 0; i < visible.length; i++) { const show = i === dataIdx0; visible[i] = show; u.setSeries(i + 1, { show }); }
      }
    }
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
function TimeSeriesLegend({ rows, isVisible, onToggle }: {
  rows: LegendRow[];
  visTick: number;                                  // re-render trigger only
  isVisible: (dataIdx0: number) => boolean;
  onToggle: (dataIdx0: number, additive: boolean) => void;
}) {
  return (
    <div style={{ marginTop: 8, overflowX: 'auto' }}>
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
  );
}
