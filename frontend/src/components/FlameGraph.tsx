'use client';
import { useMemo, useState } from 'react';
import type { FlameNode } from '@/lib/types';
import { hashColor } from '@/lib/utils';

interface Box {
  node: FlameNode;
  x: number;       // px from left of root
  width: number;   // px
  depth: number;
}

const ROW_H = 18;
const PAD_TOP = 4;

/**
 * Top-down flame graph. Width = self+children value (proportional to root).
 * Click a frame → zoom into that subtree. Click root crumb → zoom out.
 */
export function FlameGraph({ root, totalWidth = 1100 }: { root: FlameNode; totalWidth?: number }) {
  const [focus, setFocus] = useState<FlameNode>(root);
  const [hover, setHover] = useState<{ x: number; y: number; node: FlameNode } | null>(null);

  const boxes = useMemo(() => layout(focus, totalWidth), [focus, totalWidth]);
  const maxDepth = boxes.reduce((m, b) => Math.max(m, b.depth), 0);
  const height = (maxDepth + 1) * ROW_H + PAD_TOP * 2;

  // Path from root → focus, for breadcrumbs
  const path: FlameNode[] = [];
  buildPath(root, focus, path);

  return (
    <div style={{ background: 'var(--bg1)', border: '1px solid var(--border)', borderRadius: 8, padding: 12 }}>
      {/* Breadcrumbs */}
      <div style={{ marginBottom: 8, fontSize: 12, color: 'var(--text2)', display: 'flex', flexWrap: 'wrap', gap: 4 }}>
        {path.map((n, i) => (
          <span key={i}>
            {i > 0 && <span style={{ color: 'var(--text3)' }}> › </span>}
            <button onClick={() => setFocus(n)}
              style={{
                background: 'transparent', border: 0, color: i === path.length - 1 ? 'var(--text)' : 'var(--accent2)',
                fontFamily: 'monospace', fontSize: 12, cursor: 'pointer', padding: 0,
              }}>{n.name}</button>
          </span>
        ))}
        <span style={{ marginLeft: 'auto', color: 'var(--text3)' }}>
          total: {focus.value.toLocaleString()} samples
        </span>
      </div>

      <div style={{ overflow: 'auto', position: 'relative' }}
           onMouseLeave={() => setHover(null)}>
        <svg width={totalWidth} height={height} style={{ display: 'block', fontFamily: 'monospace' }}>
          {boxes.map((b, i) => {
            const color = b.node === focus ? '#E30613' : hashColor(b.node.name);
            const w = Math.max(0.5, b.width);
            const x = b.x;
            const y = PAD_TOP + b.depth * ROW_H;
            const showText = w > 36;
            const pct = focus.value > 0 ? (b.node.value / focus.value) * 100 : 0;
            return (
              <g key={i} onClick={() => setFocus(b.node)}
                onMouseEnter={ev => setHover({ x: ev.clientX, y: ev.clientY, node: b.node })}
                style={{ cursor: 'pointer' }}>
                <rect x={x} y={y} width={w} height={ROW_H - 2}
                  fill={color} fillOpacity={0.85} stroke="#0d1117" strokeWidth={0.5} />
                {showText && (
                  <text x={x + 4} y={y + 12} fill="#0d1117" fontSize={11} fontWeight={600}>
                    {clipText(b.node.name, w - 8)} ({pct.toFixed(1)}%)
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
            padding: '6px 10px', borderRadius: 6, fontSize: 12,
            pointerEvents: 'none', zIndex: 100, maxWidth: 480,
          }}>
            <div style={{ fontWeight: 600, wordBreak: 'break-all' }}>{hover.node.name}</div>
            {hover.node.file && (
              <div style={{ color: 'var(--text2)', fontSize: 11, wordBreak: 'break-all' }}>
                {hover.node.file}{hover.node.line ? `:${hover.node.line}` : ''}
              </div>
            )}
            <div style={{ marginTop: 4, color: 'var(--text2)' }}>
              value: {hover.node.value.toLocaleString()}
              {hover.node.self ? ` · self: ${hover.node.self.toLocaleString()}` : ''}
              {' · '}{((hover.node.value / focus.value) * 100).toFixed(2)}% of focus
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

// ── Layout ────────────────────────────────────────────────────────────────────
//
// Top-down icicle layout: each node spans (value/parent.value) of parent width.

function layout(root: FlameNode, totalWidth: number): Box[] {
  const out: Box[] = [];
  walk(root, 0, totalWidth, 0, out);
  return out;
}

function walk(n: FlameNode, x: number, w: number, depth: number, out: Box[]) {
  out.push({ node: n, x, width: w, depth });
  if (!n.children || !n.children.length) return;
  // Sort children by value desc so big frames cluster left
  const kids = [...n.children].sort((a, b) => b.value - a.value);
  const total = kids.reduce((s, c) => s + c.value, 0) || 1;
  let cx = x;
  for (const c of kids) {
    const cw = (c.value / total) * w;
    walk(c, cx, cw, depth + 1, out);
    cx += cw;
  }
}

function buildPath(node: FlameNode, target: FlameNode, acc: FlameNode[]): boolean {
  acc.push(node);
  if (node === target) return true;
  for (const c of node.children ?? []) {
    if (buildPath(c, target, acc)) return true;
  }
  acc.pop();
  return false;
}

function clipText(s: string, maxW: number): string {
  // Rough: ~6.5px per monospace char
  const max = Math.floor(maxW / 6.5);
  if (s.length <= max) return s;
  // Trim Go-style package prefixes, keep tail
  return '…' + s.slice(s.length - max + 1);
}
