// VolumeChart.tsx — time-bucketed throughput histogram (Tempo/Datadog-grade).
//
// Per time bucket: a STACKED bar of ok (grey, var(--text3)) + error (red,
// var(--err)), with a p99 duration LINE overlaid (orange, var(--warn)) on a
// secondary axis. Error-heavy buckets get a red tint so a burst of failures is
// obvious without reading the legend. Header stats: TOTAL / ERRORS / ERROR RATE
// / P99 MAX.
//
// Buckets are derived from the CURRENTLY-fetched + filtered trace rows via the
// Phase-0 `aggregate` transform (count per bucket; p99 per bucket) — so the
// chart always matches the table. Canvas (not uPlot) because we need the
// stacked-bar + dual-axis line + per-bar red tint in one paint; uPlot's bars
// plugin doesn't compose those cleanly.

import { useEffect, useMemo, useRef, useState } from 'react';
import { aggregateBuckets } from '@/lib/perf/transforms';
import { fmtNum } from '@/lib/utils';
import type { TraceRow } from '@/lib/types';
import { fmtDur } from './shared';

const PAD = { l: 8, r: 8, t: 10, b: 18 };

interface Bucket {
  t: number;       // bucket start (ms)
  ok: number;
  err: number;
  p99: number;     // ms
}

// chooseBucketMs picks a bucket width that yields ~40-60 bars across the span.
function chooseBucketMs(spanMs: number): number {
  const target = 48;
  const raw = spanMs / target;
  const steps = [
    1000, 2000, 5000, 10_000, 15_000, 30_000,
    60_000, 120_000, 300_000, 600_000, 900_000,
    1_800_000, 3_600_000, 7_200_000, 21_600_000, 43_200_000, 86_400_000,
  ];
  for (const s of steps) if (s >= raw) return s;
  return steps[steps.length - 1];
}

export function VolumeChart({
  rows, height = 168,
}: {
  rows: TraceRow[];
  height?: number;
}) {
  const wrapRef = useRef<HTMLDivElement>(null);
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const [width, setWidth] = useState(900);
  const [hover, setHover] = useState<{ b: Bucket; x: number } | null>(null);
  const bucketsRef = useRef<{ b: Bucket; x0: number; x1: number }[]>([]);

  useEffect(() => {
    const el = wrapRef.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setWidth(el.clientWidth || 900));
    ro.observe(el);
    setWidth(el.clientWidth || 900);
    return () => ro.disconnect();
  }, []);

  // Bucket the live rows. We run the pure aggregate transform inline (the row
  // count per page is small — a few hundred — well under the worker threshold)
  // for ok-count, err-count and p99(duration) on a shared bucket grid.
  const { buckets, totals } = useMemo(() => {
    if (rows.length === 0) {
      return { buckets: [] as Bucket[], totals: { total: 0, err: 0, p99Max: 0, maxCount: 0, maxP99: 0 } };
    }
    let lo = Infinity, hi = -Infinity;
    for (const r of rows) {
      const t = r.startTime / 1e6;
      if (t < lo) lo = t;
      if (t > hi) hi = t;
    }
    const bucketMs = chooseBucketMs(Math.max(1, hi - lo));

    const times: number[] = [];
    const okVals: number[] = [];
    const errTimes: number[] = [];
    const errVals: number[] = [];
    const durTimes: number[] = [];
    const durVals: number[] = [];
    for (const r of rows) {
      const t = r.startTime / 1e6;
      times.push(t); okVals.push(1);
      durTimes.push(t); durVals.push(r.durationMs);
      if (r.hasError) { errTimes.push(t); errVals.push(1); }
    }
    const okAgg = aggregateBuckets(times, okVals, bucketMs, 'count');
    const errAgg = aggregateBuckets(errTimes, errVals, bucketMs, 'count');
    const p99Agg = aggregateBuckets(durTimes, durVals, bucketMs, 'p99');

    const errByT = new Map<number, number>();
    errAgg.x.forEach((t, i) => errByT.set(t, errAgg.y[i]));
    const p99ByT = new Map<number, number>();
    p99Agg.x.forEach((t, i) => p99ByT.set(t, p99Agg.y[i]));

    const out: Bucket[] = okAgg.x.map((t, i) => {
      const totalCount = okAgg.y[i];
      const err = errByT.get(t) ?? 0;
      return { t, ok: Math.max(0, totalCount - err), err, p99: p99ByT.get(t) ?? 0 };
    });

    let total = 0, err = 0, p99Max = 0, maxCount = 0, maxP99 = 0;
    for (const b of out) {
      total += b.ok + b.err;
      err += b.err;
      p99Max = Math.max(p99Max, b.p99);
      maxCount = Math.max(maxCount, b.ok + b.err);
      maxP99 = Math.max(maxP99, b.p99);
    }
    return { buckets: out, totals: { total, err, p99Max, maxCount, maxP99 } };
  }, [rows]);

  // Paint.
  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    const dpr = Math.min(window.devicePixelRatio || 1, 2);
    canvas.width = Math.round(width * dpr);
    canvas.height = Math.round(height * dpr);
    const ctx = canvas.getContext('2d');
    if (!ctx) return;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, width, height);
    bucketsRef.current = [];
    if (buckets.length === 0) return;

    const cs = getComputedStyle(canvas);
    const cOk = cs.getPropertyValue('--text3').trim() || '#888';
    const cErr = cs.getPropertyValue('--err').trim() || '#dc2626';
    const cP99 = cs.getPropertyValue('--warn').trim() || '#d97706';
    const cBorder = cs.getPropertyValue('--border').trim() || '#3338';

    const plotW = width - PAD.l - PAD.r;
    const plotH = height - PAD.t - PAD.b;
    const n = buckets.length;
    const slot = plotW / n;
    const barW = Math.max(1, Math.min(slot - 1.5, slot * 0.82));
    const maxCount = totals.maxCount || 1;
    const maxP99 = totals.maxP99 || 1;

    // baseline
    ctx.strokeStyle = cBorder;
    ctx.lineWidth = 1;
    ctx.beginPath();
    ctx.moveTo(PAD.l, PAD.t + plotH + 0.5);
    ctx.lineTo(width - PAD.r, PAD.t + plotH + 0.5);
    ctx.stroke();

    // stacked bars
    buckets.forEach((b, i) => {
      const total = b.ok + b.err;
      const x = PAD.l + i * slot + (slot - barW) / 2;
      const x1 = x + barW;
      bucketsRef.current.push({ b, x0: x, x1 });
      const errHeavy = total > 0 && b.err / total >= 0.25;
      if (errHeavy) {
        // tint the bucket column red to flag a failure burst
        ctx.fillStyle = cErr;
        ctx.globalAlpha = 0.08;
        ctx.fillRect(PAD.l + i * slot, PAD.t, slot, plotH);
        ctx.globalAlpha = 1;
      }
      const okH = (b.ok / maxCount) * plotH;
      const errH = (b.err / maxCount) * plotH;
      // ok (grey) on the bottom, error (red) stacked above
      ctx.fillStyle = cOk;
      ctx.globalAlpha = 0.55;
      ctx.fillRect(x, PAD.t + plotH - okH, barW, okH);
      ctx.fillStyle = cErr;
      ctx.globalAlpha = 0.9;
      ctx.fillRect(x, PAD.t + plotH - okH - errH, barW, errH);
      ctx.globalAlpha = 1;
    });

    // p99 line overlay (secondary axis, orange)
    ctx.strokeStyle = cP99;
    ctx.lineWidth = 1.5;
    ctx.beginPath();
    let started = false;
    buckets.forEach((b, i) => {
      const cx = PAD.l + i * slot + slot / 2;
      const cy = PAD.t + plotH - (b.p99 / maxP99) * plotH;
      if (b.p99 <= 0) { started = false; return; }
      if (!started) { ctx.moveTo(cx, cy); started = true; }
      else ctx.lineTo(cx, cy);
    });
    ctx.stroke();
  }, [buckets, totals, width, height]);

  const onMove = (e: React.MouseEvent) => {
    const r = canvasRef.current?.getBoundingClientRect();
    if (!r) return;
    const x = e.clientX - r.left;
    const hit = bucketsRef.current.find(bk => x >= bk.x0 - 2 && x <= bk.x1 + 2);
    setHover(hit ? { b: hit.b, x: (hit.x0 + hit.x1) / 2 } : null);
  };

  const errRate = totals.total > 0 ? (totals.err / totals.total) * 100 : 0;

  return (
    <div style={{
      background: 'var(--bg2)', border: '1px solid var(--border)',
      borderRadius: 8, padding: 12, marginBottom: 10,
    }}>
      {/* Header stats */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 18, marginBottom: 8, flexWrap: 'wrap' }}>
        <Stat label="TOTAL" value={fmtNum(totals.total)} />
        <Stat label="ERRORS" value={fmtNum(totals.err)} tone={totals.err > 0 ? 'err' : undefined} />
        <Stat label="ERROR RATE" value={`${errRate.toFixed(2)}%`} tone={errRate > 1 ? 'err' : undefined} />
        <Stat label="P99 MAX" value={totals.p99Max ? fmtDur(totals.p99Max) : '—'} tone="warn" />
        <span style={{ flex: 1 }} />
        <span style={{ display: 'inline-flex', alignItems: 'center', gap: 4, fontSize: 10, color: 'var(--text3)' }}>
          <span style={{ width: 8, height: 8, background: 'var(--text3)', borderRadius: 2, opacity: 0.55 }} /> ok
          <span style={{ width: 8, height: 8, background: 'var(--err)', borderRadius: 2, marginLeft: 8 }} /> error
          <span style={{ width: 12, height: 2, background: 'var(--warn)', marginLeft: 8 }} /> p99
        </span>
      </div>
      <div ref={wrapRef} style={{ position: 'relative', height }}>
        {buckets.length === 0 ? (
          <div style={{ height, display: 'flex', alignItems: 'center', justifyContent: 'center', color: 'var(--text3)', fontSize: 12 }}>
            No traces in view to bucket.
          </div>
        ) : (
          <canvas ref={canvasRef}
            style={{ width: '100%', height, display: 'block' }}
            onMouseMove={onMove}
            onMouseLeave={() => setHover(null)} />
        )}
        {hover && (
          <div style={{
            position: 'absolute', pointerEvents: 'none', zIndex: 5, top: 4,
            left: `min(${hover.x}px, calc(100% - 150px))`, transform: 'translateX(8px)',
            background: 'var(--bg2)', border: '1px solid var(--border)',
            borderRadius: 4, padding: '6px 9px', fontSize: 11, color: 'var(--text)',
            whiteSpace: 'nowrap', boxShadow: '0 4px 14px rgba(0,0,0,0.25)',
          }}>
            <div className="mono" style={{ color: 'var(--text2)', marginBottom: 2 }}>
              {new Date(hover.b.t).toLocaleTimeString()}
            </div>
            <div>ok <b>{hover.b.ok}</b> · err <b style={{ color: 'var(--err)' }}>{hover.b.err}</b></div>
            <div style={{ color: 'var(--warn)' }}>p99 {hover.b.p99 ? fmtDur(hover.b.p99) : '—'}</div>
          </div>
        )}
      </div>
    </div>
  );
}

function Stat({ label, value, tone }: { label: string; value: string; tone?: 'err' | 'warn' }) {
  const color = tone === 'err' ? 'var(--err)' : tone === 'warn' ? 'var(--warn)' : 'var(--text)';
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 1 }}>
      <span style={{ fontSize: 9.5, color: 'var(--text3)', fontWeight: 700, letterSpacing: '0.5px' }}>{label}</span>
      <span className="mono" style={{ fontSize: 15, fontWeight: 700, color }}>{value}</span>
    </div>
  );
}
