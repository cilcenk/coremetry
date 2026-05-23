import { useEffect, useRef } from 'react';
import uPlot from 'uplot';
import 'uplot/dist/uPlot.min.css';
import { escapeHTML } from '@/lib/utils';
import type { SpanMetricSeries } from '@/lib/types';
import { fmtSmart, fmtXTicks, seriesColor } from '@/lib/chartFmt';

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

export function MultiLineChart({
  series, unit, height = 320, deploys, thresholds, syncKey, onZoom,
  compareSeries, compareOffsetNs, compareLabel,
}: {
  series: SpanMetricSeries[];
  unit?: string;
  height?: number;
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
}) {
  const containerRef = useRef<HTMLDivElement>(null);
  const plotRef = useRef<uPlot | null>(null);

  // Mirror of the live "this index is currently visible" set.
  // Kept in a ref instead of useState so the click handler can
  // toggle without triggering a re-render of the React tree
  // around the chart.
  const visibleRef = useRef<boolean[]>([]);

  useEffect(() => {
    const el = containerRef.current;
    if (!el) return;
    plotRef.current?.destroy();
    plotRef.current = null;
    // visibleRef tracks BOTH current and compare series so the
    // legend toggle still works on either set. Compare lines
    // sit after current lines in the series array.
    const compareEnabled = (compareSeries?.length ?? 0) > 0 && (compareOffsetNs ?? 0) > 0;
    visibleRef.current = [...series.map(() => true), ...(compareSeries ?? []).map(() => true)];
    if (series.length === 0 && !compareEnabled) return;

    // Shift compare-series timestamps forward by the offset so
    // a point from "24h ago" lands at the same X position as
    // the matching current-period point. The visual story is
    // "where the line was at this same time-of-day yesterday".
    const offsetNs = compareEnabled ? (compareOffsetNs ?? 0) : 0;
    const shiftedCompare: SpanMetricSeries[] = compareEnabled
      ? (compareSeries ?? []).map(s => ({
          ...s,
          points: s.points.map(p => ({ ...p, time: p.time + offsetNs })),
        }))
      : [];

    // Unified time axis (union of bucket timestamps across
    // every series, including the shifted compare set). uPlot
    // consumes this as a single x array shared by all y arrays.
    const allTimes = new Set<number>();
    series.forEach(s => s.points.forEach(p => allTimes.add(p.time)));
    shiftedCompare.forEach(s => s.points.forEach(p => allTimes.add(p.time)));
    const times = [...allTimes].sort((a, b) => a - b);
    const xs = times.map(t => t / 1e9); // ns → unix seconds

    // Per-series y values aligned to the union x axis. Missing
    // points become null → uPlot draws a gap.
    const allSeries = [...series, ...shiftedCompare];
    const ySeries: (number | null)[][] = allSeries.map(s => {
      const valByTime = new Map(s.points.map(p => [p.time, p.value]));
      return times.map(t => valByTime.get(t) ?? null);
    });

    const css = getComputedStyle(document.documentElement);
    const text3 = css.getPropertyValue('--text3').trim() || '#484f58';
    const grid  = css.getPropertyValue('--bg2').trim()   || '#21262d';

    const data: uPlot.AlignedData = [xs, ...ySeries] as uPlot.AlignedData;

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
      scales: { x: { time: true } },
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
          const color = seriesColor(label);
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
              value: (_u: uPlot, v: number | null) => fmtSmart(v, unit) +
                (v != null ? ` (${compareLabel ?? 'past'})` : ''),
            } satisfies uPlot.Series;
          }
          return {
            label,
            stroke: color,
            fill: color + '22',
            width: 2,
            // Point markers (v0.5.257 — was: ≤100 only). Bumped
            // ceiling to ≤500 so denser charts still surface
            // individual datapoints — operator can hover any
            // bucket to read the exact value without squinting
            // at the smoothed line. Auto-tunes size down past
            // 200 so we don't end up with a continuous "fat
            // sausage" look on busy charts.
            points: {
              show: times.length <= 500,
              size: times.length <= 100 ? 5 : times.length <= 200 ? 3.5 : 2.5,
            },
            value: (_u: uPlot, v: number | null) => fmtSmart(v, unit),
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
          // single explicit formatter that always shows both
          // date and time so the operator never loses the
          // intra-day shape. Narrow windows still read cleanly
          // because the same MM-DD HH:MM works at any zoom.
          values: (_u, splits) => fmtXTicks(splits),
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
      legend: { show: true, live: true, markers: { width: 2 } },
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
            onZoom(Math.min(x0, x1), Math.max(x0, x1));
            // Reset the visual selection after the parent
            // takes over the new range — otherwise the grey
            // band sticks around until the next click.
            u.setSelect({ left: 0, width: 0, top: 0, height: 0 }, false);
          },
        ] : undefined,
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
                const colour = th.severity === 'err' ? '#e84e4e' : '#f0b352';
                const y = u.valToPos(th.value, 'y', true);
                // Tinted breach band above the line.
                ctx.fillStyle = th.severity === 'err'
                  ? 'rgba(232,78,78,0.07)'
                  : 'rgba(240,179,82,0.07)';
                ctx.fillRect(u.bbox.left, u.bbox.top, u.bbox.width, y - u.bbox.top);
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
              ctx.strokeStyle = '#a371f7';
              ctx.fillStyle = '#a371f7';
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
        ] : undefined,
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
            for (let i = 0; i < series.length; i++) {
              const yArr = u.data[i + 1];
              if (!yArr) continue;
              const v = yArr[idx];
              if (v == null) continue;
              const s = series[i];
              const label = s.groupKey.length ? s.groupKey.join(' / ') : 'value';
              rows.push({ label, color: seriesColor(label), v: v as number });
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
                    `<span style="display:inline-block;width:8px;height:8px;background:#a371f7;border-radius:2px;flex-shrink:0"></span>` +
                    `<span style="flex:1;overflow:hidden;text-overflow:ellipsis;white-space:nowrap" title="${lbl}${desc}">deploy ${lbl}</span>` +
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
                  `<span style="font-family:ui-monospace,monospace;font-variant-numeric:tabular-nums">${fmt(r.v)}</span>` +
                `</div>`;
              }).join('');
            tip.style.opacity = '1';
            // Position: by default 12px right + below the
            // cursor. If the tooltip would clip past the
            // right edge, flip to the left side. Same flip
            // for the bottom edge so the tooltip never goes
            // outside the chart canvas.
            const cw = el.clientWidth;
            const tw = tip.offsetWidth;
            const th = tip.offsetHeight;
            const left = u.cursor.left ?? 0;
            const top  = u.cursor.top  ?? 0;
            const x = left + 12 + tw > cw ? left - tw - 12 : left + 12;
            const y = top + 12 + th > height ? top - th - 12 : top + 12;
            tip.style.left = `${Math.max(0, x)}px`;
            tip.style.top  = `${Math.max(0, y)}px`;
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
  }, [series, unit, height, deploys, thresholds, syncKey, onZoom, compareSeries, compareOffsetNs, compareLabel]);

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
      const dataIdx = Array.from(li.parentElement?.children ?? []).indexOf(li) - 1;
      if (dataIdx < 0) return;
      e.stopPropagation();
      e.preventDefault();

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
    <div ref={containerRef} style={{
      position: 'relative', width: '100%',
    }}>
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
