// WaterfallRows.tsx — Trace Detail waterfall, DOM-row design (replaces the
// canvas renderer for the rich per-row UI the approved design calls for:
// per-span TYPE tag, service-coloured bars, critical-path + error styling).
//
// Token-only (light + dark safe): service hue via the shared svcColor map
// (same one Topology / Traces table / charts use — one colour per service
// across the product); everything else via globals.css CSS variables.
// content-visibility:auto on rows keeps deep traces from janking.

import { useMemo, useState } from 'react';
import { spanHasError } from '@/lib/otel';
import { displaySpanName } from '@/lib/utils';
import { svcColor, fmtDur } from './shared';
import type { SpanRow } from '@/lib/types';

const TREE_PCT = 46; // span-tree column width (%); the rest is the time track
const ROW_H = 30;
const INDENT_BASE = 12;
const INDENT_STEP = 15;
const GRID = [0, 25, 50, 75, 100];

type SpanType = 'HTTP' | 'RPC' | 'DB' | 'MQ';

// spanType — derive the protocol tag from OTel attributes, falling back to
// SpanKind. null = a plain internal span (no protocol tag).
function spanType(s: SpanRow): SpanType | null {
  const a = s.attributes || {};
  if (a['rpc.system']) return 'RPC';
  if (s.httpMethod || s.httpRoute || a['http.method'] || a['http.request.method'] || a['url.full']) return 'HTTP';
  if (s.dbSystem || a['db.system']) return 'DB';
  if (a['messaging.system']) return 'MQ';
  const k = (s.kind || '').toLowerCase();
  if (k === 'client' || k === 'server') return 'RPC';
  if (k === 'producer' || k === 'consumer') return 'MQ';
  return null;
}

// Per-type tint, built from existing tokens via color-mix (no new hex).
const TYPE_TOKEN: Record<SpanType, string> = {
  HTTP: '--info',
  RPC: '--accent',
  DB: '--warn',
  MQ: '--ok',
};

function TypeTag({ t }: { t: SpanType }) {
  const tok = `var(${TYPE_TOKEN[t]})`;
  return (
    <span style={{
      fontSize: 10, fontWeight: 700, letterSpacing: '0.3px',
      fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
      textTransform: 'uppercase', padding: '0 5px', borderRadius: 4, lineHeight: 1.5,
      background: `color-mix(in srgb, ${tok} 16%, transparent)`,
      color: tok, flexShrink: 0,
    }}>{t}</span>
  );
}

interface Laid { span: SpanRow; depth: number; hasKids: boolean; }

function dfsOrder(spans: SpanRow[]): Laid[] {
  const ids = new Set(spans.map(s => s.spanId));
  const byParent = new Map<string, SpanRow[]>();
  for (const s of spans) {
    const pid = s.parentSpanId && ids.has(s.parentSpanId) ? s.parentSpanId : '';
    const list = byParent.get(pid);
    if (list) list.push(s); else byParent.set(pid, [s]);
  }
  for (const list of byParent.values()) list.sort((a, b) => a.startTime - b.startTime);
  const out: Laid[] = [];
  const walk = (pid: string, depth: number) => {
    for (const s of byParent.get(pid) ?? []) {
      const kids = byParent.get(s.spanId);
      out.push({ span: s, depth, hasKids: !!kids && kids.length > 0 });
      walk(s.spanId, depth + 1);
    }
  };
  walk('', 0);
  return out;
}

export function WaterfallRows({ spans, selectedId, onSelect, criticalPathIds, matchIds }: {
  spans: SpanRow[];
  selectedId: string | null;
  onSelect: (id: string | null) => void;
  criticalPathIds?: Set<string>;
  matchIds?: Set<string>;
}) {
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());

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

  // Hide descendants of any collapsed span (DFS order makes this a single pass).
  const visible = useMemo(() => {
    if (collapsed.size === 0) return laid;
    const out: Laid[] = [];
    let collapseDepth = Infinity;
    for (const row of laid) {
      if (row.depth > collapseDepth) continue; // inside a collapsed subtree
      collapseDepth = Infinity;
      out.push(row);
      if (collapsed.has(row.span.spanId)) collapseDepth = row.depth;
    }
    return out;
  }, [laid, collapsed]);

  if (spans.length === 0) {
    return <div style={{ padding: 16, color: 'var(--text3)', fontSize: 12 }}>No spans to render.</div>;
  }

  const critOn = !!criticalPathIds;
  const toggle = (id: string) => setCollapsed(prev => {
    const n = new Set(prev);
    if (n.has(id)) n.delete(id); else n.add(id);
    return n;
  });

  return (
    <div style={{ border: '1px solid var(--border)', borderRadius: 8, overflow: 'hidden', background: 'var(--bg1)', fontSize: 12 }}>
      {/* header time axis */}
      <div style={{ display: 'grid', gridTemplateColumns: `${TREE_PCT}% 1fr`, borderBottom: '1px solid var(--border)', background: 'var(--bg2)' }}>
        <div style={{ padding: '6px 10px', fontSize: 10, color: 'var(--text3)', textTransform: 'uppercase', letterSpacing: '0.4px' }}>Span</div>
        <div style={{ position: 'relative', height: 24, fontFamily: 'ui-monospace, monospace', fontSize: 10, color: 'var(--text3)' }}>
          {GRID.map(p => (
            <span key={p} style={{
              position: 'absolute', top: 6,
              left: p === 100 ? undefined : `calc(${p}% + 8px)`,
              right: p === 100 ? 8 : undefined,
              transform: p === 0 || p === 100 ? undefined : 'translateX(-50%)',
              whiteSpace: 'nowrap',
            }}>{p === 0 ? '0ns' : fmtDur((totalNs * p / 100) / 1e6)}</span>
          ))}
        </div>
      </div>

      {/* rows + gridline overlay */}
      <div style={{ position: 'relative' }}>
        {visible.map(({ span, depth, hasKids }) => {
          const err = spanHasError(span);
          const onCrit = criticalPathIds?.has(span.spanId);
          const dimmed = (matchIds && !matchIds.has(span.spanId)) || false;
          const sel = span.spanId === selectedId;
          const color = svcColor(span.serviceName || 'unknown');
          const left = ((span.startTime - minT) / totalNs) * 100;
          const width = Math.max(0.4, (Math.max(0, span.endTime - span.startTime) / totalNs) * 100);
          const t = spanType(span);
          const labelOutside = left + width <= 82;
          return (
            <div key={span.spanId}
              onClick={() => onSelect(sel ? null : span.spanId)}
              style={{
                display: 'grid', gridTemplateColumns: `${TREE_PCT}% 1fr`,
                minHeight: ROW_H, alignItems: 'center', cursor: 'pointer',
                borderBottom: '1px solid color-mix(in srgb, var(--border) 45%, transparent)',
                background: sel ? 'color-mix(in srgb, var(--accent) 12%, transparent)'
                  : err ? 'color-mix(in srgb, var(--err) 8%, transparent)' : 'transparent',
                opacity: dimmed ? 0.45 : 1,
                contentVisibility: 'auto', containIntrinsicSize: `auto ${ROW_H}px`,
              }}>
              {/* span-tree cell */}
              <div style={{
                display: 'flex', alignItems: 'center', gap: 6,
                paddingLeft: INDENT_BASE + depth * INDENT_STEP, paddingRight: 8, minWidth: 0,
              }}>
                <span
                  onClick={e => { if (hasKids) { e.stopPropagation(); toggle(span.spanId); } }}
                  style={{ width: 10, flexShrink: 0, color: 'var(--text3)', fontSize: 9, textAlign: 'center', cursor: hasKids ? 'pointer' : 'default' }}>
                  {hasKids ? (collapsed.has(span.spanId) ? '▸' : '▾') : ''}
                </span>
                <span style={{ width: 7, height: 7, borderRadius: '50%', background: color, flexShrink: 0 }}
                  title={span.serviceName || 'unknown'} />
                <span style={{ fontWeight: 700, color: 'var(--text)', whiteSpace: 'nowrap', flexShrink: 0 }}>
                  {span.serviceName || 'unknown'}
                </span>
                {t && <TypeTag t={t} />}
                <span className="mono" style={{ color: 'var(--text2)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', minWidth: 0 }}
                  title={displaySpanName(span)}>{displaySpanName(span)}</span>
              </div>

              {/* time track */}
              <div style={{ position: 'relative', height: ROW_H }}>
                <div style={{
                  position: 'absolute', top: '50%', transform: 'translateY(-50%)',
                  left: `${left}%`, width: `${width}%`, height: 12, borderRadius: 3,
                  background: color,
                  opacity: critOn && !onCrit ? 0.62 : 1,
                  // critical path → 2px red inset on the bar's left edge;
                  // error → 1.6px red inset outline around the bar.
                  boxShadow: err ? 'inset 0 0 0 1.6px var(--err)'
                    : (critOn && onCrit ? 'inset 2px 0 0 0 var(--err)' : 'none'),
                }} />
                <span className="mono" style={{
                  position: 'absolute', top: '50%', transform: 'translateY(-50%)',
                  ...(labelOutside
                    ? { left: `calc(${left + width}% + 6px)` }
                    : { right: 'calc(0% + 8px)' }),
                  fontSize: 10, whiteSpace: 'nowrap',
                  color: err ? 'var(--err)' : 'var(--text3)',
                  fontWeight: err ? 700 : 400,
                }}>{err ? '⚠ ' : ''}{fmtDur(span.durationMs)}</span>
              </div>
            </div>
          );
        })}

        {/* faint vertical gridlines over the time track (drawn last, behind nothing
            interactive, so they read across the whole column). */}
        <div style={{ position: 'absolute', top: 0, bottom: 0, left: `${TREE_PCT}%`, right: 0, pointerEvents: 'none' }}>
          {GRID.map(p => (
            <span key={p} style={{
              position: 'absolute', top: 0, bottom: 0, left: `${p}%`,
              width: 1, background: 'var(--border)', opacity: 0.4,
            }} />
          ))}
        </div>
      </div>

      {/* footer legend */}
      <div style={{
        display: 'flex', alignItems: 'center', gap: 8, padding: '8px 10px',
        borderTop: '1px solid var(--border)', background: 'var(--bg2)',
        fontSize: 10, color: 'var(--text3)', flexWrap: 'wrap',
      }}>
        {(['HTTP', 'RPC', 'DB', 'MQ'] as SpanType[]).map(t => <TypeTag key={t} t={t} />)}
        <span style={{ marginLeft: 4 }}>Bars colored by service · the red edge marks the critical path.</span>
      </div>
    </div>
  );
}
