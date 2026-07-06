import { useMemo, useState } from 'react';
import { hashColor, fmtNum } from '@/lib/utils';
import type { AggSpanNode } from '@/lib/types';

// Aggregate flamegraph for a service across many traces. Same
// idea as a profiler flame chart, applied to span trees instead
// of stack frames:
//
//   • Width of a node = "weight" = count * avgMs across the
//     sampled traces. So the visually-widest band is where the
//     service actually spends most of its time, end-to-end.
//   • Top-down icicle: root at the top, children stacked below.
//     A child's width = its weight as a fraction of the parent's
//     weight.
//   • Click any node to zoom into that subtree. Breadcrumbs at
//     the top let you climb back out.
//
// Why this matters: a single trace shows you what happened on
// that one request. The aggregate flame across 200 traces shows
// you the *signature* of a service — "every request spends 40%
// in DB and 30% in this RPC; the GC band is suspiciously fat",
// — which is the Datadog APM signature view in everything but
// the implementation. Catches latency drift that no single trace
// surfaces.

interface Box {
  node: AggSpanNode;
  x: number;       // px from left of root
  width: number;   // px
  depth: number;
}

const ROW_H = 22;
const PAD_TOP = 4;

// Synthetic node used as the implicit root when the input has
// multiple roots — ensures the flame has one top bar covering
// the full width even when the sampled traces had several
// distinct entry points.
const SYNTHETIC_ROOT: AggSpanNode = {
  service: '(all)',
  operation: '(all)',
  count: 0,
  avgMs: 0,
  maxMs: 0,
  errorCount: 0,
  avgStartMs: 0,
  children: [],
};

function nodeWeight(n: AggSpanNode): number {
  // count × avgMs = total milliseconds spent on this path
  // across the sampled traces. Falls back to 1 so a zero-time
  // node still renders as a thin sliver instead of vanishing.
  const v = (n.count || 0) * (n.avgMs || 0);
  return v > 0 ? v : 1;
}

function subtreeWeight(n: AggSpanNode): number {
  // Self + children weight. Pure self for leaves; for internal
  // nodes we use max(self, sum-of-children) so a parent with
  // long self-time isn't clipped by its children. In practice
  // self ≈ sum-of-children for span aggregations, but the max
  // is the safer fallback.
  const self = nodeWeight(n);
  if (!n.children || n.children.length === 0) return self;
  const sum = n.children.reduce((s, c) => s + subtreeWeight(c), 0);
  return Math.max(self, sum);
}

export function AggregateFlame({ roots, totalWidth = 1100 }: {
  roots: AggSpanNode[];
  totalWidth?: number;
}) {
  // Wrap multi-root input in a synthetic root so the rendering
  // loop has a single top-level frame to size against.
  const wrapped = useMemo<AggSpanNode>(() => {
    if (roots.length === 1) return roots[0];
    return { ...SYNTHETIC_ROOT, children: roots };
  }, [roots]);

  const [focus, setFocus] = useState<AggSpanNode>(wrapped);
  // Reset focus when the data changes underneath us — otherwise
  // a stale node reference leaves the user stuck in a non-existent
  // subtree.
  useMemo(() => { setFocus(wrapped); }, [wrapped]);

  const [hover, setHover] = useState<{ x: number; y: number; node: AggSpanNode } | null>(null);

  const boxes = useMemo(() => layout(focus, totalWidth), [focus, totalWidth]);
  const maxDepth = boxes.reduce((m, b) => Math.max(m, b.depth), 0);
  const height = (maxDepth + 1) * ROW_H + PAD_TOP * 2;

  const path: AggSpanNode[] = [];
  buildPath(wrapped, focus, path);

  const focusWeight = subtreeWeight(focus);

  return (
    <div style={{
      background: 'var(--bg1)', border: '1px solid var(--border)',
      borderRadius: 8, padding: 12,
    }}>
      <div style={{
        marginBottom: 8, fontSize: 12, color: 'var(--text2)',
        display: 'flex', flexWrap: 'wrap', gap: 4, alignItems: 'center',
      }}>
        {path.map((n, i) => (
          <span key={i}>
            {i > 0 && <span style={{ color: 'var(--text3)' }}> › </span>}
            <button onClick={() => setFocus(n)}
              style={{
                background: 'transparent', border: 0,
                color: i === path.length - 1 ? 'var(--text)' : 'var(--accent2)',
                fontFamily: 'monospace', fontSize: 12, cursor: 'pointer', padding: 0,
              }}>
              {n.operation || n.service}
            </button>
          </span>
        ))}
        <span style={{ flex: 1 }} />
        <span style={{ color: 'var(--text3)' }}>
          {fmtNum(focus.count || 0)} spans · total {(focusWeight / 1000).toFixed(2)}s aggregated
        </span>
      </div>

      <div style={{ overflow: 'auto', position: 'relative' }}
           onMouseLeave={() => setHover(null)}>
        <svg width={totalWidth} height={height} style={{ display: 'block', fontFamily: 'monospace' }}>
          {boxes.map((b, i) => {
            const isErr = b.node.errorCount > 0;
            // v0.8.302 — error bars use the theme token (SVG resolves var()),
            // so they match the app-wide error red on every palette.
            const baseColor = isErr ? 'var(--err)' : hashColor(b.node.service || b.node.operation);
            const w = Math.max(0.5, b.width);
            const x = b.x;
            const y = PAD_TOP + b.depth * ROW_H;
            const showText = w > 56;
            const pct = focusWeight > 0
              ? (subtreeWeight(b.node) / focusWeight) * 100
              : 0;
            const label = b.node.operation || b.node.service || '(unnamed)';
            return (
              <g key={i} onClick={() => setFocus(b.node)}
                onMouseEnter={ev => setHover({ x: ev.clientX, y: ev.clientY, node: b.node })}
                style={{ cursor: 'pointer' }}>
                <rect x={x} y={y} width={w} height={ROW_H - 2}
                  fill={baseColor}
                  fillOpacity={isErr ? 0.92 : 0.85}
                  stroke="var(--bg1)" strokeWidth={1} />
                {showText && (
                  <text x={x + 5} y={y + 14}
                        fill="#0d1117" fontSize={11} fontWeight={600}>
                    {clipText(label, w - 10)}{w > 110 ? ` · ${pct.toFixed(0)}%` : ''}
                  </text>
                )}
              </g>
            );
          })}
        </svg>
        {hover && (
          <div style={{
            position: 'fixed', left: hover.x + 14, top: hover.y - 10,
            background: 'var(--bg2)', border: '1px solid var(--border)',
            padding: '8px 12px', borderRadius: 6, fontSize: 12,
            pointerEvents: 'none', zIndex: 100, maxWidth: 480,
            color: 'var(--text)',
            boxShadow: '0 4px 14px rgba(0,0,0,0.35)',
          }}>
            <div style={{ fontWeight: 600, wordBreak: 'break-all' }}>
              {hover.node.operation || '(unnamed)'}
            </div>
            <div style={{ color: 'var(--text2)', fontSize: 11, marginBottom: 4 }}>
              {hover.node.service}
              {hover.node.kind ? ` · ${hover.node.kind}` : ''}
            </div>
            <div style={{ color: 'var(--text2)' }}>
              ×{fmtNum(hover.node.count)} ·
              avg <b style={{ color: 'var(--text)' }}>{hover.node.avgMs.toFixed(2)}ms</b> ·
              max <b style={{ color: 'var(--text)' }}>{hover.node.maxMs.toFixed(2)}ms</b>
            </div>
            <div style={{ color: 'var(--text3)', fontSize: 11, marginTop: 2 }}>
              total {(nodeWeight(hover.node) / 1000).toFixed(2)}s aggregated
              {focusWeight > 0
                ? ` · ${((subtreeWeight(hover.node) / focusWeight) * 100).toFixed(2)}% of focus`
                : ''}
            </div>
            {hover.node.errorCount > 0 && (
              <div style={{ color: 'var(--err)', fontSize: 11, marginTop: 4 }}>
                {hover.node.errorCount} error{hover.node.errorCount === 1 ? '' : 's'}
                {' '}({((hover.node.errorCount / Math.max(1, hover.node.count)) * 100).toFixed(0)}%)
              </div>
            )}
          </div>
        )}
      </div>
    </div>
  );
}

function layout(root: AggSpanNode, totalWidth: number): Box[] {
  const out: Box[] = [];
  walk(root, 0, totalWidth, 0, out);
  return out;
}

function walk(n: AggSpanNode, x: number, w: number, depth: number, out: Box[]) {
  out.push({ node: n, x, width: w, depth });
  if (!n.children || n.children.length === 0) return;
  // Sort children by weight desc so the heaviest subtrees cluster
  // on the left — same convention as Datadog / pprof flames so an
  // operator's eyes land on the dominant path first.
  const kids = [...n.children].sort((a, b) => subtreeWeight(b) - subtreeWeight(a));
  const total = kids.reduce((s, c) => s + subtreeWeight(c), 0) || 1;
  let cx = x;
  for (const c of kids) {
    const cw = (subtreeWeight(c) / total) * w;
    walk(c, cx, cw, depth + 1, out);
    cx += cw;
  }
}

function buildPath(node: AggSpanNode, target: AggSpanNode, acc: AggSpanNode[]): boolean {
  acc.push(node);
  if (node === target) return true;
  for (const c of node.children ?? []) {
    if (buildPath(c, target, acc)) return true;
  }
  acc.pop();
  return false;
}

function clipText(s: string, maxW: number): string {
  // Rough: ~6.5px per monospace char.
  const max = Math.floor(maxW / 6.5);
  if (s.length <= max) return s;
  return s.slice(0, Math.max(1, max - 1)) + '…';
}
