import { useMemo, useState } from 'react';
import type { DiffNode } from '@/lib/flameDiff';
import { diffColor } from '@/lib/flameDiff';

// FlameDiff — overlay of two profiles' flame trees with each
// frame coloured by its percentage change between baseline
// and current. Width = union(current, baseline) so a frame
// that shrank still occupies its old footprint, painted
// green. The eye reads "fat red bands = where the regression
// is, green bands = where the optimisation paid off" in one
// glance, which is the whole point of a diff flame.

interface Box {
  node: DiffNode;
  x: number;
  width: number;
  depth: number;
}

const ROW_H = 18;
const PAD_TOP = 4;

export function FlameDiff({ root, totalWidth = 1100 }: {
  root: DiffNode; totalWidth?: number;
}) {
  const [focus, setFocus] = useState<DiffNode>(root);
  const [hover, setHover] = useState<{ x: number; y: number; node: DiffNode } | null>(null);

  const boxes = useMemo(() => layout(focus, totalWidth), [focus, totalWidth]);
  const maxDepth = boxes.reduce((m, b) => Math.max(m, b.depth), 0);
  const height = (maxDepth + 1) * ROW_H + PAD_TOP * 2;

  const path: DiffNode[] = [];
  buildPath(root, focus, path);

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
              {n.name}
            </button>
          </span>
        ))}
        <span style={{ flex: 1 }} />
        <span style={{ color: 'var(--text3)' }}>
          baseline {focus.baseline.toLocaleString()} →
          current {focus.current.toLocaleString()} ({fmtPct(focus.pct)})
        </span>
      </div>

      <div style={{ overflow: 'auto', position: 'relative' }}
           onMouseLeave={() => setHover(null)}>
        <svg width={totalWidth} height={height} style={{ display: 'block', fontFamily: 'monospace' }}>
          {boxes.map((b, i) => {
            const w = Math.max(0.5, b.width);
            const x = b.x;
            const y = PAD_TOP + b.depth * ROW_H;
            const showText = w > 56;
            const fill = diffColor(b.node.pct);
            const lbl = `${b.node.name}${showText ? ` ${fmtPct(b.node.pct)}` : ''}`;
            return (
              <g key={i} onClick={() => setFocus(b.node)}
                 onMouseEnter={ev => setHover({ x: ev.clientX, y: ev.clientY, node: b.node })}
                 style={{ cursor: 'pointer' }}>
                <rect x={x} y={y} width={w} height={ROW_H - 2}
                      fill={fill} fillOpacity={0.88}
                      stroke="var(--bg1)" strokeWidth={0.6} />
                {showText && (
                  <text x={x + 4} y={y + 12}
                        fill="#0d1117" fontSize={11} fontWeight={600}>
                    {clipText(lbl, w - 8)}
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
            pointerEvents: 'none', zIndex: 'var(--z-tooltip)', maxWidth: 480,
            color: 'var(--text)',
            boxShadow: '0 4px 14px rgba(0,0,0,0.35)',
          }}>
            <div style={{ fontWeight: 600, wordBreak: 'break-all' }}>
              {hover.node.name}
            </div>
            {hover.node.file && (
              <div style={{ color: 'var(--text2)', fontSize: 11, marginBottom: 4 }}>
                {hover.node.file}{hover.node.line ? `:${hover.node.line}` : ''}
              </div>
            )}
            <div style={{ color: 'var(--text2)' }}>
              baseline <b style={{ color: 'var(--text)' }}>{hover.node.baseline.toLocaleString()}</b> →
              current <b style={{ color: 'var(--text)' }}>{hover.node.current.toLocaleString()}</b>
            </div>
            <div style={{
              fontSize: 12, marginTop: 4,
              color: hover.node.pct > 0.05 ? 'var(--err)' : hover.node.pct < -0.05 ? 'var(--ok)' : 'var(--text3)',
              fontWeight: 600,
            }}>
              {hover.node.delta > 0 ? '+' : ''}{hover.node.delta.toLocaleString()} samples
              {' '}({fmtPct(hover.node.pct)})
            </div>
          </div>
        )}
      </div>

      <Legend />
    </div>
  );
}

function Legend() {
  // Compact swatch strip explaining the colour bands. Lives
  // below the flame so it doesn't fight the breadcrumbs for
  // attention.
  const stops: { lbl: string; pct: number }[] = [
    { lbl: '≥ +50%', pct: 0.50 },
    { lbl: '+15%',   pct: 0.15 },
    { lbl: '+5%',    pct: 0.05 },
    { lbl: '~0',     pct: 0 },
    { lbl: '−5%',    pct: -0.05 },
    { lbl: '−15%',   pct: -0.15 },
    { lbl: '−50%',   pct: -0.50 },
  ];
  return (
    <div style={{
      marginTop: 8, fontSize: 11, color: 'var(--text3)',
      display: 'flex', alignItems: 'center', gap: 4, flexWrap: 'wrap',
    }}>
      <span style={{ marginRight: 4 }}>Δ vs baseline:</span>
      {stops.map(s => (
        <span key={s.lbl} style={{
          display: 'inline-flex', alignItems: 'center', gap: 4,
          padding: '0 6px',
        }}>
          <span style={{
            width: 10, height: 10, borderRadius: 2,
            background: diffColor(s.pct),
          }} />
          {s.lbl}
        </span>
      ))}
    </div>
  );
}

function fmtPct(pct: number): string {
  if (!isFinite(pct)) return '∞';
  const v = pct * 100;
  if (Math.abs(v) >= 100) return `${v.toFixed(0)}%`;
  if (Math.abs(v) >= 10)  return `${v.toFixed(1)}%`;
  return `${v.toFixed(2)}%`;
}

function layout(root: DiffNode, totalWidth: number): Box[] {
  const out: Box[] = [];
  walk(root, 0, totalWidth, 0, out);
  return out;
}

function walk(n: DiffNode, x: number, w: number, depth: number, out: Box[]) {
  out.push({ node: n, x, width: w, depth });
  if (!n.children || n.children.length === 0) return;
  // Sort children by absolute pct desc so the regressions
  // (loud red) cluster left-of-frame at every depth.
  const kids = [...n.children].sort((a, b) =>
    Math.abs(b.pct) - Math.abs(a.pct));
  const total = kids.reduce((s, c) => s + c.value, 0) || 1;
  let cx = x;
  for (const c of kids) {
    const cw = (c.value / total) * w;
    walk(c, cx, cw, depth + 1, out);
    cx += cw;
  }
}

function buildPath(node: DiffNode, target: DiffNode, acc: DiffNode[]): boolean {
  acc.push(node);
  if (node === target) return true;
  for (const c of node.children ?? []) {
    if (buildPath(c, target, acc)) return true;
  }
  acc.pop();
  return false;
}

function clipText(s: string, maxW: number): string {
  const max = Math.floor(maxW / 6.5);
  if (s.length <= max) return s;
  return s.slice(0, Math.max(1, max - 1)) + '…';
}
