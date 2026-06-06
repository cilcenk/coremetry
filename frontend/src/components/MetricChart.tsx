import { useEffect, useRef } from 'react';
import uPlot from 'uplot';
import 'uplot/dist/uPlot.min.css';
import type { MetricPoint } from '@/lib/types';
import { fmtSmart, seriesColor } from '@/lib/chartFmt';
import { escapeHTML } from '@/lib/utils';
import { placeTooltip } from '@/components/MultiLineChart';

// uPlot single-series chart. Replaces Chart.js for the metric
// drill-down view. Three load-bearing options:
//
//   1. scales.x.time = true → x ticks formatted as HH:MM
//      instead of raw unix seconds. Without this the axis
//      reads "1715120400" — what surfaced as "broken" charts
//      after the original migration.
//
//   2. width: el.clientWidth || 600 → first paint always has
//      something to render even when the parent is briefly
//      0-width (StrictMode double-mount, display:none parent
//      transition). ResizeObserver corrects on the next
//      layout pass.
//
//   3. height stays fixed (default 300, prop override). The
//      previous shape used "height: 100%" which collapsed to
//      0 on pages that didn't stretch their parent.

export function MetricChart({
  name, points, height = 300, unit, syncKey, onZoom,
}: {
  name: string;
  points: MetricPoint[];
  height?: number;
  unit?: string;
  syncKey?: string;
  onZoom?: (fromUnixSec: number, toUnixSec: number) => void;
}) {
  const containerRef = useRef<HTMLDivElement>(null);
  const plotRef = useRef<uPlot | null>(null);

  useEffect(() => {
    const el = containerRef.current;
    if (!el) return;

    plotRef.current?.destroy();
    plotRef.current = null;

    if (points.length === 0) return;

    const css = getComputedStyle(document.documentElement);
    const text3 = css.getPropertyValue('--text3').trim() || '#484f58';
    const grid  = css.getPropertyValue('--bg2').trim()   || '#21262d';

    const xs: number[] = new Array(points.length);
    const ys: number[] = new Array(points.length);
    for (let i = 0; i < points.length; i++) {
      xs[i] = points[i].time / 1e9;  // ns → unix seconds
      ys[i] = points[i].value;
    }

    // Same series across charts → same colour. seriesColor is
    // deterministic on the metric name so an operator who
    // sees "http_requests_total" in one chart sees it in the
    // same hue everywhere.
    const lineColor = seriesColor(name);
    const opts: uPlot.Options = {
      width: el.clientWidth || 600,
      height,
      scales: { x: { time: true } },
      series: [
        {},
        {
          label: name,
          stroke: lineColor,
          fill: lineColor + '1a', // ~10% alpha
          width: 2,
          points: { show: points.length <= 100, size: 5 },
        },
      ],
      axes: [
        {
          stroke: text3,
          grid: { stroke: grid, width: 1 },
          ticks: { stroke: grid, width: 1 },
        },
        {
          stroke: text3,
          grid: { stroke: grid, width: 1 },
          ticks: { stroke: grid, width: 1 },
          // Smart axis formatter — promotes ms→s, count→k/M,
          // bytes→kB/MB. With a unit hint the axis reads
          // "234ms" not "234.5678".
          values: (_u, splits) => splits.map(v => fmtSmart(v, unit)),
          size: 56,
        },
      ],
      cursor: {
        x: true, y: true, focus: { prox: 30 },
        // Drag-zoom (x-axis only). Forwarded to the page via
        // onZoom so the parent can update its TimeRange.
        drag: { x: true, y: false, setScale: !!onZoom },
        // Cursor sync — charts on the same dashboard share a
        // sync key so hovering one paints the crosshair on
        // every chart sharing the key.
        sync: syncKey ? { key: syncKey } : undefined,
      },
      legend: { show: true, live: true, markers: { width: 2 } },
      hooks: {
        // Drag-zoom callback — forward unix-sec range to the
        // parent so it can update its TimeRange state.
        setSelect: onZoom ? [
          (u) => {
            const sel = u.select;
            if (!sel || sel.width < 4) return;
            const x0 = u.posToVal(sel.left, 'x');
            const x1 = u.posToVal(sel.left + sel.width, 'x');
            if (!isFinite(x0) || !isFinite(x1)) return;
            onZoom(Math.min(x0, x1), Math.max(x0, x1));
            u.setSelect({ left: 0, width: 0, top: 0, height: 0 }, false);
          },
        ] : undefined,
        // Floating tooltip near the cursor — same UX as
        // MultiLineChart (Grafana / Datadog style). Single
        // series so the panel just shows one row.
        setCursor: [
          (u) => {
            const tip = el.querySelector('.uplot-tooltip') as HTMLDivElement | null;
            // Y-axis crosshair value pill — pinned to the
            // left edge, follows the cursor's y position.
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
            const yVal = u.data[1] ? u.data[1][idx] : null;
            if (xVal == null || yVal == null) {
              tip.style.opacity = '0';
              return;
            }
            const d = new Date((xVal as number) * 1000);
            const hh = d.getHours().toString().padStart(2, '0');
            const mm = d.getMinutes().toString().padStart(2, '0');
            const ss = d.getSeconds().toString().padStart(2, '0');
            const valStr = fmtSmart(yVal as number, unit);
            const safeName = escapeHTML(name);
            tip.innerHTML =
              `<div style="font-weight:600;margin-bottom:2px">${hh}:${mm}:${ss}</div>` +
              `<div style="display:flex;gap:8px;align-items:center">` +
                `<span style="display:inline-block;width:8px;height:8px;background:${lineColor};border-radius:2px"></span>` +
                `<span style="flex:1;overflow:hidden;text-overflow:ellipsis;white-space:nowrap" title="${safeName}">${safeName}</span>` +
                `<span style="font-family:ui-monospace,monospace;font-variant-numeric:tabular-nums">${valStr}</span>` +
              `</div>`;
            tip.style.opacity = '1';
            // Place BESIDE the cursor (never under it). placeTooltip
            // (v0.7.11) replaces the old `Math.max(0, …)` clamp that, on a
            // narrow chart, pulled a flipped panel back over the pointer —
            // the exact value-hidden-under-cursor the operator reported.
            const { x, y } = placeTooltip(
              u.cursor.left ?? 0, u.cursor.top ?? 0,
              tip.offsetWidth, tip.offsetHeight,
              el.clientWidth, height,
            );
            tip.style.left = `${x}px`;
            tip.style.top  = `${y}px`;
          },
        ],
      },
    };

    plotRef.current = new uPlot(opts, [xs, ys], el);

    const ro = new ResizeObserver(() => {
      if (plotRef.current && el) {
        plotRef.current.setSize({ width: el.clientWidth, height });
      }
    });
    ro.observe(el);

    return () => {
      ro.disconnect();
      plotRef.current?.destroy();
      plotRef.current = null;
    };
  }, [name, points, height, unit, syncKey, onZoom]);

  // Container width-only; uPlot owns the canvas height. The
  // .uplot-tooltip child is updated by the setCursor hook
  // above (Grafana-style floating value panel). The y-pill
  // shows the exact y value at the cursor's row-coordinate.
  return (
    <div ref={containerRef} style={{ position: 'relative', width: '100%' }}>
      <div className="uplot-tooltip" style={{
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
