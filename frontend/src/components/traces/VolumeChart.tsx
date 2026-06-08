// VolumeChart.tsx — Datadog/Dynatrace-grade span-volume histogram.
//
// Aggregated per-bucket series from /api/spans/metric over the SELECTED window
// (count = total spans, errors = error spans, p50 = median latency ms) — TRUE
// volume, not the table-page sample (v0.8.72). v0.8.78 upgrade: a left span-count
// Y axis with gridlines, a bottom time X axis (HH:MM, 5 ticks), a legend ABOVE
// the bars, a dual-axis p50 latency line on its own ms scale, and a rich hover
// (bar highlight + dashed crosshair + p50 marker + floating tooltip). Each bar
// flexes to its span count with the error share painted red at the bottom.

import { useEffect, useMemo, useRef, useState } from 'react';
import type { SpanMetricSeries } from '@/lib/types';
import { fmtDur } from './shared';

const PAD = { l: 46, r: 50, b: 18, t: 2 };
const HOT_RATE = 0.03; // bucket errRate above this tints the bar
const GRID = [0, 0.25, 0.5, 0.75, 1]; // Y ticks / gridlines

interface Bucket {
  t: number;       // bucket start (ms)
  ok: number;
  err: number;
  total: number;
  p50: number;     // ms
  errRate: number; // 0..1
}

// k-format: 9500 → "9.5k", 120000 → "120k".
function kfmt(n: number): string {
  if (n >= 1000) {
    const k = n / 1000;
    return (k >= 100 ? Math.round(k).toString() : k.toFixed(1).replace(/\.0$/, '')) + 'k';
  }
  return Math.round(n).toString();
}
function hhmm(ms: number): string {
  const d = new Date(ms);
  return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`;
}

export function VolumeChart({
  count, errors, p50, height = 140, onBrush,
}: {
  count: SpanMetricSeries[] | null;
  errors: SpanMetricSeries[] | null;
  p50: SpanMetricSeries[] | null;
  height?: number;
  // Drag across the bars to select a time window. The volume chart is the
  // page's brush/overview tool — dragging narrows the range to the brushed
  // buckets (restored in v0.8.86; lost in the v0.8.78 canvas→DOM rewrite).
  onBrush?: (fromMs: number, toMs: number) => void;
}) {
  const wrapRef = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(900);
  const [hover, setHover] = useState<number | null>(null);
  const [drag, setDrag] = useState<{ a: number; b: number } | null>(null);

  useEffect(() => {
    const el = wrapRef.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setWidth(el.clientWidth || 900));
    ro.observe(el);
    setWidth(el.clientWidth || 900);
    return () => ro.disconnect();
  }, []);

  const { buckets, maxCount, maxP50, bucketMin } = useMemo(() => {
    const cPts = count?.[0]?.points ?? [];
    if (!cPts.length) return { buckets: [] as Bucket[], maxCount: 0, maxP50: 0, bucketMin: 0 };
    const eMap = new Map((errors?.[0]?.points ?? []).map(p => [p.time, p.value]));
    const pMap = new Map((p50?.[0]?.points ?? []).map(p => [p.time, p.value]));
    const bs: Bucket[] = cPts.map(p => {
      const tot = p.value;
      const e = Math.min(eMap.get(p.time) ?? 0, tot);
      return { t: p.time / 1e6, ok: tot - e, err: e, total: tot, p50: pMap.get(p.time) ?? 0, errRate: tot ? e / tot : 0 };
    });
    let mc = 0, mp = 0;
    for (const b of bs) { mc = Math.max(mc, b.total); mp = Math.max(mp, b.p50); }
    const dt = bs.length > 1 ? Math.round((bs[1].t - bs[0].t) / 60000) : 1;
    return { buckets: bs, maxCount: mc || 1, maxP50: mp || 1, bucketMin: Math.max(1, dt) };
  }, [count, errors, p50]);

  const plotW = Math.max(1, width - PAD.l - PAD.r);
  const plotH = Math.max(1, height - PAD.b - PAD.t);
  const n = buckets.length;

  // p50 line points (in plot-local px), for the SVG overlay + hover marker.
  const linePts = useMemo(() => buckets.map((b, i) => ({
    x: PAD.l + (n > 1 ? (i + 0.5) / n * plotW : plotW / 2),
    y: PAD.t + plotH - (b.p50 / maxP50) * plotH,
    p50: b.p50,
  })), [buckets, n, plotW, plotH, maxP50]);

  const hb = hover != null ? buckets[hover] : null;

  // Commit the brush on mouseup anywhere (so releasing outside the chart still
  // works). Refs keep the listener reading the latest buckets/onBrush without
  // re-subscribing on every drag tick.
  const onBrushRef = useRef(onBrush); onBrushRef.current = onBrush;
  const bucketsRef = useRef(buckets); bucketsRef.current = buckets;
  const bucketMinRef = useRef(bucketMin); bucketMinRef.current = bucketMin;
  const dragging = drag !== null;
  useEffect(() => {
    if (!dragging) return;
    const up = () => {
      setDrag(d => {
        const bs = bucketsRef.current;
        if (d && onBrushRef.current && bs.length) {
          const lo = Math.min(d.a, d.b), hi = Math.max(d.a, d.b);
          if (bs[lo] && bs[hi]) onBrushRef.current(bs[lo].t, bs[hi].t + bucketMinRef.current * 60000);
        }
        return null;
      });
    };
    window.addEventListener('mouseup', up);
    return () => window.removeEventListener('mouseup', up);
  }, [dragging]);

  return (
    <div style={{ background: 'var(--bg2)', border: '1px solid var(--border)', borderRadius: 8, padding: 12, marginBottom: 10 }}>
      {/* Legend — ABOVE the bars, outside the plot. */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 14, marginBottom: 8, fontSize: 10.5, color: 'var(--text-faint)' }}>
        <span style={{ display: 'inline-flex', alignItems: 'center', gap: 5 }}>
          <span style={{ width: 9, height: 9, borderRadius: 2, background: 'var(--accent)' }} /> ok spans
        </span>
        <span style={{ display: 'inline-flex', alignItems: 'center', gap: 5 }}>
          <span style={{ width: 9, height: 9, borderRadius: 2, background: 'var(--err)' }} /> errors
        </span>
        <span style={{ display: 'inline-flex', alignItems: 'center', gap: 5 }}>
          <span style={{ width: 14, height: 2, background: 'var(--orange)' }} /> p50 latency
        </span>
        <span style={{ marginLeft: 'auto', fontFamily: 'var(--font-mono, ui-monospace)' }}>spans / {bucketMin}m bucket</span>
      </div>

      <div ref={wrapRef} style={{ position: 'relative', height }}
        onMouseLeave={() => setHover(null)}>
        {buckets.length === 0 ? (
          <div style={{ height, display: 'flex', alignItems: 'center', justifyContent: 'center', color: 'var(--text-faint)', fontSize: 12 }}>
            No traces in view to bucket.
          </div>
        ) : (
          <>
            {/* Y axis labels + horizontal gridlines (0 fully opaque, rest faint). */}
            {GRID.map((g, i) => {
              const y = PAD.t + plotH - g * plotH;
              return (
                <div key={i}>
                  <div style={{
                    position: 'absolute', left: 0, top: y, width: PAD.l - 6, textAlign: 'right',
                    transform: 'translateY(-50%)', fontSize: 9, color: 'var(--text-faint)',
                    fontFamily: 'var(--font-mono, ui-monospace)', pointerEvents: 'none',
                  }}>{kfmt(maxCount * g)}</div>
                  <div style={{
                    position: 'absolute', left: PAD.l, top: y, width: plotW, height: 1,
                    background: 'var(--border)', opacity: g === 0 ? 1 : 0.35, pointerEvents: 'none',
                  }} />
                </div>
              );
            })}

            {/* Bars — flex row across the plot area; height ∝ span count, error at bottom red. */}
            <div style={{
              position: 'absolute', left: PAD.l, top: PAD.t, width: plotW, height: plotH,
              display: 'flex', alignItems: 'flex-end', gap: 2,
            }}>
              {buckets.map((b, i) => {
                const h = (b.total / maxCount) * plotH;
                const errH = h * b.errRate;
                const hot = b.errRate > HOT_RATE;
                const on = hover === i;
                return (
                  <div key={i}
                    onMouseDown={onBrush ? (e) => { e.preventDefault(); setDrag({ a: i, b: i }); } : undefined}
                    onMouseEnter={() => { setHover(i); setDrag(d => (d ? { ...d, b: i } : d)); }}
                    style={{
                      flex: 1, height: h, display: 'flex', flexDirection: 'column', justifyContent: 'flex-end',
                      borderRadius: '2px 2px 0 0', overflow: 'hidden', cursor: onBrush ? 'crosshair' : 'default',
                      outline: on && !drag ? '1px solid var(--accent)' : 'none',
                      background: hot ? 'color-mix(in srgb, var(--err) 14%, transparent)' : 'transparent',
                    }}>
                    <div style={{ height: Math.max(0, h - errH), background: 'var(--accent)', opacity: on ? 0.95 : 0.7 }} />
                    {errH > 0 && <div style={{ height: errH, background: 'var(--err)' }} />}
                  </div>
                );
              })}
            </div>

            {/* Brush selection band — full plot height across the dragged buckets. */}
            {drag && (() => {
              const lo = Math.min(drag.a, drag.b), hi = Math.max(drag.a, drag.b);
              return (
                <div style={{
                  position: 'absolute', left: PAD.l + (lo / n) * plotW, top: PAD.t,
                  width: ((hi - lo + 1) / n) * plotW, height: plotH, pointerEvents: 'none',
                  background: 'color-mix(in srgb, var(--accent) 14%, transparent)',
                  border: '1px solid var(--accent)', borderRadius: 2,
                }} />
              );
            })()}

            {/* p50 latency line — own ms scale, drawn as an SVG overlay. */}
            <svg width={width} height={height} style={{ position: 'absolute', inset: 0, pointerEvents: 'none', overflow: 'visible' }}>
              <path d={linePts.map((p, i) => `${i ? 'L' : 'M'}${p.x},${p.y}`).join(' ')}
                fill="none" stroke="var(--orange)" strokeWidth="1.6" vectorEffect="non-scaling-stroke" />
              {hb && hover != null && linePts[hover] && (
                <circle cx={linePts[hover].x} cy={linePts[hover].y} r="3.2" fill="var(--orange)" stroke="var(--bg2)" strokeWidth="1" />
              )}
            </svg>
            {/* p50 max axis label (top-right). */}
            <div style={{ position: 'absolute', right: 2, top: 0, fontSize: 9, color: 'var(--orange)', fontFamily: 'var(--font-mono, ui-monospace)', pointerEvents: 'none' }}>
              p50 {fmtDur(maxP50)}
            </div>

            {/* Hover crosshair — vertical dashed line at the bucket centre. */}
            {hover != null && linePts[hover] && (
              <div style={{
                position: 'absolute', left: linePts[hover].x, top: PAD.t, width: 1, height: plotH,
                borderLeft: '1px dashed var(--text-faint)', pointerEvents: 'none',
              }} />
            )}

            {/* Bottom X axis — 5 time ticks, first left- / last right-aligned. */}
            {[0, 0.25, 0.5, 0.75, 1].map((f, i) => {
              const idx = Math.min(n - 1, Math.round(f * (n - 1)));
              const left = PAD.l + (n > 1 ? (idx + 0.5) / n * plotW : plotW / 2);
              return (
                <div key={i} style={{
                  position: 'absolute', top: height - PAD.b + 2, left,
                  transform: i === 0 ? 'none' : i === 4 ? 'translateX(-100%)' : 'translateX(-50%)',
                  fontSize: 9, color: 'var(--text-faint)', fontFamily: 'var(--font-mono, ui-monospace)', pointerEvents: 'none',
                }}>{hhmm(buckets[idx].t)}</div>
              );
            })}

            {/* Floating tooltip — replaces the native title. Flips left near the right edge. */}
            {hb && hover != null && linePts[hover] && (() => {
              const cx = linePts[hover].x;
              const flip = cx > width - 180;
              return (
                <div style={{
                  position: 'absolute', top: 2, left: cx, zIndex: 5, pointerEvents: 'none',
                  transform: flip ? 'translateX(calc(-100% - 10px))' : 'translateX(10px)',
                  background: 'var(--bg1)', border: '1px solid var(--border)', borderRadius: 6,
                  boxShadow: '0 4px 14px rgba(0,0,0,0.25)', padding: '6px 9px', fontSize: 11, whiteSpace: 'nowrap',
                }}>
                  <div style={{ color: 'var(--text-faint)', fontFamily: 'var(--font-mono, ui-monospace)', marginBottom: 2 }}>{hhmm(hb.t)}</div>
                  <div><b>{kfmt(hb.total)}</b> spans · <b style={{ color: 'var(--err)' }}>{(hb.errRate * 100).toFixed(1)}%</b> err
                    {' · '}<span style={{ color: 'var(--orange)' }}>p50 {hb.p50 ? fmtDur(hb.p50) : '—'}</span>
                  </div>
                </div>
              );
            })()}
          </>
        )}
      </div>
    </div>
  );
}
