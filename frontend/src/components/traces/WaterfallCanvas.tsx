// WaterfallCanvas.tsx — the trace-detail span waterfall (Tempo/Jaeger-grade).
//
// A <canvas> waterfall that scales to deep traces (thousands of spans) where a
// DOM-row-per-span layout janks. Spans are laid out DFS (parent then children
// by start time) so the call hierarchy reads top-to-bottom. Each bar is
// service-coloured (the shared svcColor map); errored spans render red.
//
// Features:
//   • critical-path toggle — spans on the path keep full colour + a 2px red
//     inset; non-critical bars dim to 0.62 so the eye follows the path.
//   • minimap — for deep traces (> MINIMAP_THRESHOLD rows) a compressed
//     overview strip on the right with a draggable viewport box.
//   • selection — click a row to select it (drives the sticky span panel);
//     the selected row gets an accent rail. matchIds (in-trace search) dims
//     non-matches.
//
// The canvas is virtualised: only rows intersecting the scroll viewport are
// painted, so 10k-span traces stay at 60fps.

import { useEffect, useMemo, useRef, useState } from 'react';
import { spanHasError } from '@/lib/otel';
import { displaySpanName, fmtNs } from '@/lib/utils';
import type { SpanRow } from '@/lib/types';
import { svcColor } from './shared';

const ROW_H = 26;
const LABEL_W = 300;          // left label gutter (px)
const MINIMAP_W = 88;
const MINIMAP_THRESHOLD = 60; // show the minimap past this many spans

interface LaidSpan {
  span: SpanRow;
  depth: number;
  index: number;      // DFS order
}

// dfsOrder walks the span DAG depth-first (roots by start, then children by
// start) so the waterfall row order matches the call hierarchy. Orphans (parent
// not in the trace) are treated as roots so nothing is dropped.
function dfsOrder(spans: SpanRow[]): LaidSpan[] {
  const byParent = new Map<string, SpanRow[]>();
  const ids = new Set(spans.map(s => s.spanId));
  for (const s of spans) {
    const pid = s.parentSpanId && ids.has(s.parentSpanId) ? s.parentSpanId : '';
    const list = byParent.get(pid);
    if (list) list.push(s); else byParent.set(pid, [s]);
  }
  for (const list of byParent.values()) list.sort((a, b) => a.startTime - b.startTime);
  const out: LaidSpan[] = [];
  let index = 0;
  const walk = (pid: string, depth: number) => {
    for (const s of byParent.get(pid) ?? []) {
      out.push({ span: s, depth, index: index++ });
      walk(s.spanId, depth + 1);
    }
  };
  walk('', 0);
  return out;
}

export function WaterfallCanvas({
  spans, selectedId, onSelect, criticalPathIds, matchIds,
}: {
  spans: SpanRow[];
  selectedId: string | null;
  onSelect: (id: string | null) => void;
  criticalPathIds?: Set<string>;
  matchIds?: Set<string>;
}) {
  const wrapRef = useRef<HTMLDivElement>(null);
  const scrollRef = useRef<HTMLDivElement>(null);
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const minimapRef = useRef<HTMLCanvasElement>(null);
  const [width, setWidth] = useState(900);
  const [scrollTop, setScrollTop] = useState(0);
  const [viewportH, setViewportH] = useState(480);

  const laid = useMemo(() => dfsOrder(spans), [spans]);
  const { minT, totalNs } = useMemo(() => {
    if (spans.length === 0) return { minT: 0, totalNs: 1 };
    let lo = Infinity, hi = -Infinity;
    for (const s of spans) {
      if (s.startTime < lo) lo = s.startTime;
      if (s.endTime > hi) hi = s.endTime;
    }
    return { minT: lo, totalNs: Math.max(1, hi - lo) };
  }, [spans]);

  const contentH = laid.length * ROW_H;
  const showMinimap = laid.length > MINIMAP_THRESHOLD;
  const barAreaX = LABEL_W;
  const barAreaW = Math.max(40, width - LABEL_W - (showMinimap ? MINIMAP_W + 10 : 0));

  // Track the wrapper width.
  useEffect(() => {
    const el = wrapRef.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setWidth(el.clientWidth || 900));
    ro.observe(el);
    setWidth(el.clientWidth || 900);
    return () => ro.disconnect();
  }, []);

  // Viewport height: cap at a sensible max so the page stays usable; scroll
  // inside for deep traces.
  useEffect(() => {
    setViewportH(Math.min(560, Math.max(220, contentH)));
  }, [contentH]);

  // Map a span to its bar x/width inside the bar area.
  const barX = (s: SpanRow) => barAreaX + ((s.startTime - minT) / totalNs) * barAreaW;
  const barW = (s: SpanRow) => Math.max(2, ((Math.max(0, s.endTime - s.startTime)) / totalNs) * barAreaW);

  // Paint the visible window of rows.
  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    const dpr = Math.min(window.devicePixelRatio || 1, 2);
    canvas.width = Math.round(width * dpr);
    canvas.height = Math.round(viewportH * dpr);
    const ctx = canvas.getContext('2d');
    if (!ctx) return;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, width, viewportH);

    const cs = getComputedStyle(canvas);
    const cText = cs.getPropertyValue('--text2').trim() || '#aaa';
    const cText3 = cs.getPropertyValue('--text3').trim() || '#888';
    const cErr = cs.getPropertyValue('--err').trim() || '#dc2626';
    const cAccent = cs.getPropertyValue('--accent').trim() || '#3b82f6';
    const cBorder = cs.getPropertyValue('--border').trim() || '#3338';

    const first = Math.max(0, Math.floor(scrollTop / ROW_H) - 2);
    const last = Math.min(laid.length, Math.ceil((scrollTop + viewportH) / ROW_H) + 2);

    ctx.font = '11px ui-monospace, SFMono-Regular, Menlo, monospace';
    ctx.textBaseline = 'middle';

    for (let i = first; i < last; i++) {
      const { span, depth } = laid[i];
      const y = i * ROW_H - scrollTop;
      const rowMid = y + ROW_H / 2;
      const selected = span.spanId === selectedId;
      const onCritical = criticalPathIds?.has(span.spanId);
      const dimmed = (criticalPathIds && !onCritical) || (matchIds && !matchIds.has(span.spanId));

      // selected row rail + tint
      if (selected) {
        ctx.fillStyle = cAccent;
        ctx.globalAlpha = 0.10;
        ctx.fillRect(0, y, width, ROW_H);
        ctx.globalAlpha = 1;
        ctx.fillStyle = cAccent;
        ctx.fillRect(0, y, 2, ROW_H);
      }

      // label (depth-indented, service · name)
      const svc = span.serviceName || 'unknown';
      const label = `${svc} · ${displaySpanName(span)}`;
      ctx.globalAlpha = dimmed ? 0.5 : 1;
      ctx.fillStyle = cText;
      const labelX = 8 + Math.min(depth, 8) * 10;
      const maxLabel = LABEL_W - labelX - 8;
      ctx.fillText(truncate(ctx, label, maxLabel), labelX, rowMid);

      // bar
      const x = barX(span);
      const w = barW(span);
      const err = spanHasError(span);
      ctx.globalAlpha = dimmed ? 0.62 : 0.92;
      ctx.fillStyle = err ? cErr : svcColor(svc);
      roundRect(ctx, x, y + 6, w, ROW_H - 12, 3);
      ctx.fill();

      // critical-path inset (2px red inset border)
      if (onCritical) {
        ctx.globalAlpha = 1;
        ctx.strokeStyle = cErr;
        ctx.lineWidth = 2;
        roundRect(ctx, x + 1, y + 7, Math.max(1, w - 2), ROW_H - 14, 2);
        ctx.stroke();
      }

      // duration label to the right of the bar
      ctx.globalAlpha = dimmed ? 0.5 : 0.85;
      ctx.fillStyle = cText3;
      ctx.font = '10px ui-monospace, SFMono-Regular, Menlo, monospace';
      ctx.fillText(fmtNs(span.endTime - span.startTime), Math.min(x + w + 5, width - 50), rowMid);
      ctx.font = '11px ui-monospace, SFMono-Regular, Menlo, monospace';
      ctx.globalAlpha = 1;
    }

    // label-gutter divider
    ctx.strokeStyle = cBorder;
    ctx.lineWidth = 1;
    ctx.beginPath();
    ctx.moveTo(LABEL_W + 0.5, 0);
    ctx.lineTo(LABEL_W + 0.5, viewportH);
    ctx.stroke();
  }, [laid, scrollTop, viewportH, width, selectedId, criticalPathIds, matchIds, minT, totalNs, barAreaW]);

  // Minimap — full-trace overview with a draggable viewport box.
  useEffect(() => {
    if (!showMinimap) return;
    const canvas = minimapRef.current;
    if (!canvas) return;
    const dpr = Math.min(window.devicePixelRatio || 1, 2);
    canvas.width = Math.round(MINIMAP_W * dpr);
    canvas.height = Math.round(viewportH * dpr);
    const ctx = canvas.getContext('2d');
    if (!ctx) return;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, MINIMAP_W, viewportH);

    const cs = getComputedStyle(canvas);
    const cErr = cs.getPropertyValue('--err').trim() || '#dc2626';
    const cAccent = cs.getPropertyValue('--accent').trim() || '#3b82f6';
    const cBorder = cs.getPropertyValue('--border').trim() || '#3338';

    const scaleY = viewportH / Math.max(1, laid.length);
    const innerW = MINIMAP_W - 8;
    for (let i = 0; i < laid.length; i++) {
      const { span } = laid[i];
      const x = 4 + ((span.startTime - minT) / totalNs) * innerW;
      const w = Math.max(1, ((span.endTime - span.startTime) / totalNs) * innerW);
      const y = i * scaleY;
      ctx.fillStyle = spanHasError(span) ? cErr : svcColor(span.serviceName || 'unknown');
      ctx.globalAlpha = 0.75;
      ctx.fillRect(x, y, w, Math.max(0.6, scaleY));
    }
    ctx.globalAlpha = 1;

    // viewport box
    const boxTop = (scrollTop / Math.max(1, contentH)) * viewportH;
    const boxH = (viewportH / Math.max(1, contentH)) * viewportH;
    ctx.strokeStyle = cAccent;
    ctx.lineWidth = 1.5;
    ctx.strokeRect(1, boxTop, MINIMAP_W - 2, Math.min(viewportH, boxH));
    ctx.strokeStyle = cBorder;
    ctx.lineWidth = 1;
    ctx.strokeRect(0.5, 0.5, MINIMAP_W - 1, viewportH - 1);
  }, [showMinimap, laid, scrollTop, viewportH, contentH, minT, totalNs]);

  const onCanvasClick = (e: React.MouseEvent) => {
    const r = canvasRef.current?.getBoundingClientRect();
    if (!r) return;
    const y = e.clientY - r.top + scrollTop;
    const idx = Math.floor(y / ROW_H);
    if (idx < 0 || idx >= laid.length) return;
    const span = laid[idx].span;
    onSelect(span.spanId === selectedId ? null : span.spanId);
  };

  const jumpFromMinimap = (e: React.MouseEvent) => {
    const r = minimapRef.current?.getBoundingClientRect();
    if (!r || !scrollRef.current) return;
    const frac = (e.clientY - r.top) / r.height;
    const target = frac * contentH - viewportH / 2;
    scrollRef.current.scrollTop = Math.max(0, Math.min(contentH - viewportH, target));
  };

  if (spans.length === 0) {
    return <div style={{ padding: 16, color: 'var(--text3)', fontSize: 12 }}>No spans to render.</div>;
  }

  return (
    <div ref={wrapRef} style={{ position: 'relative', border: '1px solid var(--border)', borderRadius: 8, overflow: 'hidden', background: 'var(--bg1)' }}>
      <div
        ref={scrollRef}
        onScroll={e => setScrollTop((e.target as HTMLDivElement).scrollTop)}
        style={{ height: viewportH, overflowY: 'auto', overflowX: 'hidden', position: 'relative' }}>
        {/* spacer sizes the scroll range; canvas is sticky over the viewport */}
        <div style={{ height: contentH, position: 'relative' }}>
          <canvas
            ref={canvasRef}
            onClick={onCanvasClick}
            style={{ position: 'sticky', top: 0, width: '100%', height: viewportH, display: 'block', cursor: 'pointer' }}
          />
        </div>
      </div>
      {showMinimap && (
        <canvas
          ref={minimapRef}
          onMouseDown={jumpFromMinimap}
          onMouseMove={e => { if (e.buttons === 1) jumpFromMinimap(e); }}
          title="Trace minimap — click or drag to jump"
          style={{
            position: 'absolute', top: 0, right: 0, width: MINIMAP_W, height: viewportH,
            borderLeft: '1px solid var(--border)', background: 'var(--bg2)', cursor: 'ns-resize',
          }}
        />
      )}
    </div>
  );
}

function truncate(ctx: CanvasRenderingContext2D, text: string, maxW: number): string {
  if (ctx.measureText(text).width <= maxW) return text;
  let lo = 0, hi = text.length;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if (ctx.measureText(text.slice(0, mid) + '…').width <= maxW) lo = mid + 1;
    else hi = mid;
  }
  return text.slice(0, Math.max(0, lo - 1)) + '…';
}

function roundRect(ctx: CanvasRenderingContext2D, x: number, y: number, w: number, h: number, r: number) {
  const rr = Math.min(r, w / 2, h / 2);
  ctx.beginPath();
  ctx.moveTo(x + rr, y);
  ctx.arcTo(x + w, y, x + w, y + h, rr);
  ctx.arcTo(x + w, y + h, x, y + h, rr);
  ctx.arcTo(x, y + h, x, y, rr);
  ctx.arcTo(x, y, x + w, y, rr);
  ctx.closePath();
}
