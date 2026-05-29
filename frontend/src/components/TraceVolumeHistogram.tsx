import { useEffect, useMemo, useRef, useState } from 'react';
import uPlot from 'uplot';
import 'uplot/dist/uPlot.min.css';
import { EventMarkers } from './EventMarkers';
import { api } from '@/lib/api';
import type { TimeRange } from '@/lib/types';
import { timeRangeToNs } from '@/lib/utils';
import { fmtSmart, fmtXTicks } from '@/lib/chartFmt';

// TraceVolumeHistogram renders a stacked-bar strip showing total
// span volume bucketed across the active time range, with the error
// share painted in red on top of the OK share. The visual ratio of
// red to gray on any bar IS the error rate at that moment, so an
// operator scanning the strip immediately sees error spikes without
// reading any percentage.
//
// Two parallel /api/spans/metric calls (count + errors) populate the
// chart; differencing yields the OK count per bucket. We pick a step
// that gives ~40 buckets across the range, capped to the API's
// minimum step (1s) and a reasonable max (5m) so a 24h window doesn't
// produce thousands of bars.
//
// Filters argument matches the /traces page's current DSL/filters so
// the histogram tracks the same predicate as the table below.
export function TraceVolumeHistogram({ range, dsl, filters, search, onZoom }: {
  range: TimeRange;
  dsl?: string;
  filters?: string;
  // v0.6.32 — search applied at WHERE level on the span-metric
  // backend so the histogram total matches the search-narrowed
  // table. Replaces the v0.6.31 "searchActive" chip-only
  // workaround; the chart now actually reflects the search
  // selection.
  search?: string;
  // v0.5.322 — drag-to-select hook. Called after the operator
  // finishes a horizontal drag inside the chart. Args are unix
  // seconds (matches uPlot's native x-scale unit on this chart).
  // Parent (Traces page) flips the page TimeRange to a custom
  // range so the trace list + filters re-fetch for that slice.
  onZoom?: (fromUnixSec: number, toUnixSec: number) => void;
}) {
  const containerRef = useRef<HTMLDivElement>(null);
  const plotRef = useRef<uPlot | null>(null);
  const dataRef = useRef<{ ok: number[]; err: number[]; p99: (number | null)[]; ts: number[] }>({ ok: [], err: [], p99: [], ts: [] });
  const [stats, setStats] = useState<{ total: number; errors: number; p99Max: number } | null>(null);
  const [error, setError] = useState<string | null>(null);

  // v0.5.478 — lifted out of useEffect so the EventMarkers
  // overlay (rendered next to the uPlot container) can use
  // the same resolved bounds. Memoised on range identity to
  // avoid retriggering the data fetch on render churn.
  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);

  useEffect(() => {
    let cancelled = false;
    // Aim for ~40 buckets across the visible window. The /api/spans/metric
    // endpoint expects step in seconds.
    const windowSec = Math.max(60, Math.round((to - from) / 1e9));
    const targetBuckets = 40;
    let step = Math.round(windowSec / targetBuckets);
    if (step < 1)   step = 1;
    if (step > 300) step = 300;

    setError(null);
    Promise.all([
      api.spanMetric({ agg: 'count',  dsl, filters, search, from, to, step }),
      api.spanMetric({ agg: 'errors', dsl, filters, search, from, to, step }),
      // v0.6.64 — p99 latency per bucket, overlaid as a line on a secondary
      // (right) axis: bars = how many spans, line = how slow. Dynatrace-style
      // volume+latency combo the operator asked for on the Traces page.
      api.spanMetric({ agg: 'p99',    dsl, filters, search, from, to, step }),
    ]).then(([total, errs, p99]) => {
      if (cancelled) return;
      const totalPoints = total?.[0]?.points ?? [];
      const errorPoints = errs?.[0]?.points ?? [];
      const errMap = new Map(errorPoints.map(p => [p.time, p.value]));
      const p99Map = new Map((p99?.[0]?.points ?? []).map(p => [p.time, p.value]));
      const ts: number[] = [];
      // v0.6.35 — inverse-overlay layout. Operator-reported: the
      // prior stacked-paths approach (ok bar from 0..ok, err bar
      // from ok..ok+err via cross-series baseline lookup) read
      // as "overlapping bars" on service+operation-filtered
      // queries. Switching to "draw total bar in gray, then
      // paint error bar from 0..err on top in red" produces the
      // SAME visual stack but as a single visible bar per bucket
      // (red bottom = errors, gray top = ok). No cross-series
      // state — robust to whatever uPlot quirk caused the prior
      // visual artifact.
      const okData:  number[] = []; // here = TOTAL spans; drawn in gray, full bar
      const errData: number[] = []; // errors; drawn in red, from 0, OVER the bottom of gray
      const p99Data: (number | null)[] = []; // p99 ms; right-axis line (null = empty bucket → gap)
      let totalAll = 0, errAll = 0, p99Max = 0;
      for (const p of totalPoints) {
        const e = errMap.get(p.time) ?? 0;
        const lat = p99Map.get(p.time) ?? 0;
        ts.push(p.time / 1e9); // ns → unix seconds
        okData.push(p.value);                  // full bar height = total spans
        errData.push(Math.min(e, p.value));    // clamp — err can't exceed total
        p99Data.push(lat > 0 ? lat : null);    // gap when the bucket had no spans
        if (lat > p99Max) p99Max = lat;
        totalAll += p.value;
        errAll   += e;
      }
      setStats({ total: totalAll, errors: errAll, p99Max });
      dataRef.current = { ok: okData, err: errData, p99: p99Data, ts };
      drawChart();
    }).catch((e) => {
      if (!cancelled) setError(e instanceof Error ? e.message : String(e));
    });

    return () => { cancelled = true; };
  }, [range, dsl, filters, search]);

  function drawChart() {
    const el = containerRef.current;
    if (!el) return;
    plotRef.current?.destroy();
    plotRef.current = null;
    const { ok, err, p99, ts } = dataRef.current;
    if (ts.length === 0) return;

    // Stacked bars via path-builder factories. Data arrays are
    // RAW (ok and err separately, not pre-summed); each series
    // declares its own baseline:
    //   - series 1 (ok)     → baseline 0      → bar from 0 to ok
    //   - series 2 (errors) → baseline 'ok'   → bar from ok to ok+err
    // The y-axis range still needs the stacked total so high
    // error bars don't get clipped — passed below via the y-scale
    // range callback.
    // v0.6.35 — y-max is just the total since err is now an
    // OVERLAY from 0 (drawn on top of the gray total bar),
    // not a stack ABOVE the ok series. ok array carries
    // total counts in the new layout.
    const stackedMax = Math.max(...ok, 0);

    const opts: uPlot.Options = {
      width: el.clientWidth || 600,
      height: 110,
      // v0.5.322 — horizontal drag-to-select. dist > 4px so a
      // simple click doesn't accidentally zoom; uplot=false on
      // x-only drag prevents the chart from auto-zooming
      // internally (we own the range update via the setSelect
      // hook below + onZoom callback to the parent).
      cursor: {
        x: true, y: false, focus: { prox: 30 },
        drag: { x: true, y: false, dist: 4, uni: 4, setScale: false },
      },
      legend: { show: false },
      scales: {
        x: { time: true },
        // Range explicitly to the stacked total so the error
        // bar (drawn from ok to ok+err) isn't clipped by the
        // auto-range only seeing ok or err individually.
        y: { range: () => [0, stackedMax * 1.05 || 1] },
        // v0.6.64 — secondary LOG latency axis for the p99 line, so a
        // multi-second tail-latency spike doesn't flatten the typical band
        // (the lesson from the /metrics histogram Volume axis).
        lat: { distr: 3 },
      },
      axes: [
        {
          stroke: '#7d8693',
          grid: { stroke: 'rgba(125,140,160,0.07)', width: 1 },
          ticks: { stroke: 'rgba(125,140,160,0.07)', width: 1 },
          font: '10px ui-monospace, monospace',
          // v0.5.377 — operator-reported: wide windows (7d / 30d)
          // dropped to month-only x-axis labels via uPlot's
          // default time formatter, which read as "the date
          // range doesn't match what I selected". Match the
          // MultiLineChart v0.5.366 fix: force MM-DD HH:MM at
          // every zoom so the calendar date AND clock stay
          // visible regardless of window width.
          values: (_u, splits) => fmtXTicks(splits),
        },
        {
          stroke: '#7d8693',
          grid: { stroke: 'rgba(125,140,160,0.10)', width: 1 },
          ticks: { stroke: 'rgba(125,140,160,0.10)', width: 1 },
          font: '10px ui-monospace, monospace',
          // Wider y-axis (was 35) so "12.5k" / "1.2M" labels
          // fit without clipping. The previous 35-pixel column
          // truncated longer counts, which the user read as
          // "no count visible".
          size: 50,
          values: (_u, splits) => splits.map(v => v == null ? '' : fmtSmart(v)),
        },
        {
          // v0.6.64 — right-side p99 latency axis (log). Amber to match the
          // p99 line; no grid so it doesn't fight the count gridlines.
          scale: 'lat',
          side: 1,
          stroke: '#d6a23c',
          grid: { show: false },
          ticks: { stroke: 'rgba(214,162,60,0.15)', width: 1 },
          font: '10px ui-monospace, monospace',
          size: 54,
          values: (_u, splits) => splits.map(v => v == null ? '' : fmtSmart(v, 'ms')),
        },
      ],
      series: [
        {},
        // OK bar — was 55% opacity slate which blended into
        // the panel background and made the bars look like a
        // faint smudge. Bumped to a more solid neutral so the
        // bar shape is readable without competing visually
        // with the error red on top.
        {
          label: 'ok',
          stroke: '#5b6776',
          fill: '#5b6776',
          paths: barsPath(0),
          points: { show: false },
        },
        // v0.6.35 — error bar drawn from 0 to err[i] as a
        // SEPARATE overlay on top of the total bar. Painted
        // last so it covers the bottom portion of the gray
        // total. Net visual: red bottom = errors, gray top =
        // remaining ok share — same stacked appearance as the
        // prior cross-series-baseline approach, but with no
        // cross-series state to go wrong.
        {
          label: 'errors',
          stroke: '#e84e4e',
          fill: '#e84e4e',
          paths: barsPath(0),
          points: { show: false },
        },
        // v0.6.64 — p99 latency line on the right (log) axis. spanGaps so a
        // sparse trace stream still draws a continuous trend; points so an
        // isolated bucket's p99 stays visible (the histogram sparse-line
        // lesson). bars = how many, line = how slow.
        {
          label: 'p99',
          stroke: '#e0a83b',
          width: 1.6,
          scale: 'lat',
          spanGaps: true,
          points: { show: true, size: 4, fill: '#e0a83b' },
        },
      ],
      hooks: {
        // v0.5.322 — drag-select handler. Fires when the user
        // releases a horizontal drag. Convert pixel select.left
        // + select.width to x-axis values (unix seconds), then
        // bubble up via onZoom. Reset the visual select rect so
        // the chart doesn't keep the selection highlight after
        // the parent moves the range (next render fetches fresh).
        setSelect: [
          (u) => {
            const sel = u.select;
            if (!onZoom || !sel || sel.width < 4) return;
            const fromSec = u.posToVal(sel.left, 'x');
            const toSec   = u.posToVal(sel.left + sel.width, 'x');
            if (!Number.isFinite(fromSec) || !Number.isFinite(toSec)) return;
            if (toSec <= fromSec) return;
            onZoom(fromSec, toSec);
            // Wipe the rect so the highlight doesn't persist.
            u.setSelect({ left: 0, top: 0, width: 0, height: 0 }, false);
          },
        ],
        // Custom tooltip on cursor move. Cheap — single DOM
        // mutation per move event, no overlay canvas.
        setCursor: [
          (u) => {
            const idx = u.cursor.idx;
            const tip = el.querySelector('.tvh-tip') as HTMLDivElement | null;
            if (!tip) return;
            if (idx == null || idx < 0 || idx >= ts.length) {
              tip.style.opacity = '0';
              return;
            }
            const okN = ok[idx];
            const errN = err[idx];
            const latN = p99[idx];
            const tot = okN + errN;
            const rate = tot > 0 ? (errN / tot * 100).toFixed(2) : '0.00';
            const d = new Date(ts[idx] * 1000);
            const hh = d.getHours().toString().padStart(2, '0');
            const mm = d.getMinutes().toString().padStart(2, '0');
            tip.innerHTML =
              `<div style="font-weight:600;margin-bottom:2px">${hh}:${mm}</div>` +
              `<div>total · ${tot.toLocaleString()}</div>` +
              `<div>errors · ${errN.toLocaleString()}</div>` +
              `<div>error rate · ${rate}%</div>` +
              `<div>p99 · ${latN != null ? fmtSmart(latN, 'ms') : '—'}</div>`;
            tip.style.opacity = '1';
            tip.style.left = `${(u.cursor.left ?? 0) + 12}px`;
            tip.style.top  = `${(u.cursor.top  ?? 0) + 12}px`;
          },
        ],
      },
    };

    // Data layout: ts (x), ok (y for slate bar), err (y for
    // stacked red bar). The path builder for the err series
    // reads u.data[1] (ok) as its baseline.
    plotRef.current = new uPlot(opts, [ts, ok, err, p99], el);

    const ro = new ResizeObserver(() => {
      if (plotRef.current && el) {
        plotRef.current.setSize({ width: el.clientWidth, height: 110 });
      }
    });
    ro.observe(el);
    return () => ro.disconnect();
  }

  // Cleanup on unmount.
  useEffect(() => () => { plotRef.current?.destroy(); plotRef.current = null; }, []);

  const errRate = stats && stats.total > 0
    ? (stats.errors / stats.total * 100).toFixed(2)
    : null;

  return (
    <div style={{
      background: 'var(--bg2)',
      border: '1px solid var(--border)',
      borderRadius: 8,
      padding: 12,
      marginBottom: 10,
    }}>
      {/* Header row — Datadog/Uptrace/Honeycomb pattern: title on
          the left, headline KPIs (total + errors + rate) as quietly
          styled stat tiles on the right. The error-rate tile turns
          red when non-zero so a glance is enough. */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 14, marginBottom: 8, padding: '0 2px' }}>
        <span style={{
          fontSize: 11, color: 'var(--text2)', fontWeight: 700,
          letterSpacing: '0.5px', textTransform: 'uppercase',
        }}>
          Span volume
        </span>
        <span style={{ flex: 1 }} />
        {stats && (
          <>
            <Stat label="total"  value={stats.total.toLocaleString()} />
            <Stat label="errors" value={stats.errors.toLocaleString()}
                  tone={stats.errors > 0 ? 'err' : 'mute'} />
            <Stat label="error rate" value={errRate ? `${errRate}%` : '—'}
                  tone={stats && stats.errors > 0 ? 'err' : 'mute'} emphasised />
            <Stat label="p99 max" value={stats.p99Max ? fmtSmart(stats.p99Max, 'ms') : '—'} />
            <span style={{ display: 'inline-flex', alignItems: 'center', gap: 4, fontSize: 10, color: 'var(--text3)' }}>
              <span style={{ width: 8, height: 8, background: '#5b6776', borderRadius: 2 }} />
              ok
              <span style={{ width: 8, height: 8, background: '#e84e4e', borderRadius: 2, marginLeft: 6 }} />
              error
              <span style={{ width: 12, height: 2, background: '#e0a83b', display: 'inline-block', marginLeft: 6 }} />
              p99
            </span>
          </>
        )}
      </div>
      <div style={{ height: 110, position: 'relative' }}>
        {error && (
          <div style={{ color: 'var(--err)', fontSize: 11, padding: 8 }}>{error}</div>
        )}
        <div ref={containerRef} style={{ width: '100%', height: '100%', position: 'relative' }} />
        {/* v0.5.478 — operator event markers (deploy / config /
            incident / maintenance) overlaid as vertical lines at
            their respective timestamps. Self-fetches; hides
            cleanly when there are no events in the window. */}
        <EventMarkers fromNs={from} toNs={to} />
        {/* Custom tooltip — uPlot's setCursor hook positions and
            populates this; opacity 0 until the cursor enters the
            chart area. */}
        <div className="tvh-tip" style={{
          // Theme-aware tokens so the tooltip stays readable
          // in both dark and light modes.
          position: 'absolute', pointerEvents: 'none',
          background: 'var(--bg2)',
          border: '1px solid var(--border)',
          borderRadius: 4,
          padding: '6px 9px',
          fontSize: 11, color: 'var(--text)',
          opacity: 0, transition: 'opacity .08s',
          whiteSpace: 'nowrap', zIndex: 5,
        }} />
      </div>
    </div>
  );
}

// barsPath returns a uPlot path-builder factory that emits a
// rectangle per X bucket. baselineSeriesIdx selects what y-value
// the bar's BOTTOM sits at:
//   0   → bottom = y(0); standalone bar
//   1.. → bottom = y(u.data[N][i]); stacked on top of series N
// uPlot's built-in bars preset can't easily share state between
// stacked series, so this factory lets us build "ok bar from 0"
// and "error bar from ok[i]" with one helper.
function barsPath(baselineSeriesIdx: number): uPlot.Series.PathBuilder {
  return (u, sidx, idx0, idx1) => {
    const xs = u.data[0];
    const ys = u.data[sidx];
    const baseline = baselineSeriesIdx > 0 ? u.data[baselineSeriesIdx] : null;
    const path = new Path2D();
    if (!xs || !ys) return null;
    const xPos = (v: number) => Math.round(u.valToPos(v, 'x', true));
    const yPos = (v: number) => Math.round(u.valToPos(v, 'y', true));
    const span = idx1 > idx0 ? xPos(xs[1] as number) - xPos(xs[0] as number) : 8;
    // 75% of the bucket width — the previous 92% made bars
    // touch each other so the chart read as one continuous
    // smear rather than 40 distinct bars. Datadog / Grafana
    // bar charts use a similar 0.7-0.8 range for clear bar
    // separation.
    const w = Math.max(2, Math.floor(span * 0.75));
    for (let i = idx0; i <= idx1; i++) {
      const yv = ys[i];
      if (yv == null) continue;
      const baseValue = baseline ? Number(baseline[i] ?? 0) : 0;
      const topValue = baseValue + Number(yv);
      const xC = xPos(xs[i] as number);
      const y0 = yPos(baseValue);
      const y1 = yPos(topValue);
      // Skip zero-height bars to keep the canvas clean.
      if (y0 === y1) continue;
      path.rect(xC - w / 2, y1, w, y0 - y1);
    }
    return { stroke: path, fill: path };
  };
}

// Stat — small label-over-value tile used in the histogram header.
// Matches the style typical APM dashboards use for inline KPIs:
// uppercase muted label, bold value below, optional emphasis colour
// when the metric is "alert-worthy".
function Stat({ label, value, tone, emphasised }: {
  label: string; value: string; tone?: 'err' | 'mute' | 'ok'; emphasised?: boolean;
}) {
  const valueColor =
    tone === 'err' ? 'var(--err)'
    : tone === 'ok'  ? 'var(--ok)'
    : 'var(--text)';
  return (
    <span style={{
      display: 'inline-flex',
      flexDirection: 'column',
      lineHeight: 1.2,
      gap: 1,
      paddingLeft: 12,
      borderLeft: '1px solid var(--border)',
    }}>
      <span style={{
        fontSize: 9, color: 'var(--text3)',
        fontWeight: 600, letterSpacing: '0.5px', textTransform: 'uppercase',
      }}>
        {label}
      </span>
      <span style={{
        fontSize: emphasised ? 14 : 13,
        fontWeight: 600, color: valueColor,
        fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
        fontVariantNumeric: 'tabular-nums',
      }}>
        {value}
      </span>
    </span>
  );
}

// HH:MM formatter used for the X-axis bucket labels.
function formatBucket(ns: number): string {
  const d = new Date(ns / 1e6);
  return `${d.getHours().toString().padStart(2, '0')}:${d.getMinutes().toString().padStart(2, '0')}`;
}
