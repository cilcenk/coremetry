'use client';
import { useEffect, useMemo, useRef, useState } from 'react';
import type { SpanRow } from '@/lib/types';
import { fmtNs, hashColor, displaySpanName } from '@/lib/utils';

const TICKS = [0, 0.25, 0.5, 0.75, 1];
const NAME_MIN = 160;
const NAME_MAX = 800;
const INDENT_PX = 16;

interface Row {
  span: SpanRow;
  depth: number;
  hasChildren: boolean;
  // For each ancestor depth (1..depth), whether the ancestor at that
  // depth still has later siblings in the visible tree. Drives the
  // continuation tree lines: at depths where the ancestor has more
  // siblings, the vertical guide line extends through this row.
  // Standard Tempo / Jaeger waterfall convention.
  ancestorContinues: boolean[];
  isLastSibling: boolean;
}

// Span-kind glyphs — case-insensitive variants of the OTel-spec values.
const KIND_ICON: Record<string, string> = {
  server:   '🖥',
  client:   '📡',
  producer: '📤',
  consumer: '📥',
  internal: '⚙',
};
function kindIcon(k: string): string {
  return KIND_ICON[(k || 'internal').toLowerCase()] ?? '⚙';
}

// Span category chip — Uptrace/SigNoz convention. The eye scans the
// category column and immediately sees "this is a DB call vs an
// outbound HTTP vs a Kafka publish", which is faster than parsing
// the operation name.
//
// Detection runs against the OTel semantic conventions: presence of
// `db.system` is the tell for a DB span, `messaging.system` for a
// queue/topic span, etc. Order matters — e.g. an HTTP span calling a
// gRPC server has both `rpc.system` and (rarely) `http.method`; we
// pick RPC first because that's what's actually being executed.
type SpanCategory = { tag: string; color: string };
// Muted, low-saturation palette to match the service-badge band —
// chips read as categorical metadata without competing visually with
// the operation name.
function categoryOf(s: SpanRow): SpanCategory | null {
  const a = s.attributes ?? {};
  if (a['db.system'])        return { tag: 'DB',   color: '#c89651' }; // amber-tan
  if (a['messaging.system']) return { tag: 'MQ',   color: '#4fb6c5' }; // turquoise
  if (a['rpc.system'])       return { tag: 'RPC',  color: '#b06b3a' }; // sienna copper
  if (a['http.method'] || a['http.request.method']) {
    return { tag: 'HTTP', color: '#5b8def' };                          // sky blue
  }
  return null;
}

export function TraceWaterfall({ spans, selectedId, onSelect }: {
  spans: SpanRow[];
  selectedId: string | null;
  onSelect: (id: string) => void;
}) {
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());
  const [nameWidth, setNameWidth] = useState<number | null>(null);
  const [containerWidth, setContainerWidth] = useState(0);
  const containerRef = useRef<HTMLDivElement>(null);
  const dragRef = useRef<{ startX: number; startW: number } | null>(null);

  useEffect(() => {
    if (!containerRef.current) return;
    setContainerWidth(containerRef.current.clientWidth);
    const ro = new ResizeObserver(entries => setContainerWidth(entries[0].contentRect.width));
    ro.observe(containerRef.current);
    return () => ro.disconnect();
  }, []);

  const { rows, minT, totalNs, maxDepth } = useMemo(() => {
    const map = new Map(spans.map(s => [s.spanId, s]));
    const children = new Map<string, SpanRow[]>(spans.map(s => [s.spanId, []]));
    const roots: SpanRow[] = [];
    for (const s of spans) {
      if (s.parentSpanId && map.has(s.parentSpanId)) children.get(s.parentSpanId)!.push(s);
      else roots.push(s);
    }
    children.forEach(c => c.sort((a, b) => a.startTime - b.startTime));
    roots.sort((a, b) => a.startTime - b.startTime);

    const out: Row[] = [];
    const dfs = (id: string, depth: number, isLast: boolean, ancestorContinues: boolean[]) => {
      const s = map.get(id); if (!s) return;
      const kids = children.get(id) ?? [];
      out.push({ span: s, depth, hasChildren: kids.length > 0, ancestorContinues, isLastSibling: isLast });
      if (collapsed.has(id)) return;
      kids.forEach((c, i) => {
        const last = i === kids.length - 1;
        // children inherit the parent's ancestor-continues flags + add
        // a flag for this depth based on whether this very subtree's
        // root still has siblings after it.
        dfs(c.spanId, depth + 1, last, [...ancestorContinues, !isLast]);
      });
    };
    roots.forEach((r, i) => dfs(r.spanId, 0, i === roots.length - 1, []));

    const minT = Math.min(...spans.map(s => s.startTime));
    const maxT = Math.max(...spans.map(s => s.endTime));
    const totalNs = maxT - minT || 1;
    const maxDepth = out.reduce((m, r) => Math.max(m, r.depth), 0);
    return { rows: out, minT, totalNs, maxDepth };
  }, [spans, collapsed]);

  const defaultNameWidth = useMemo(() => {
    if (containerWidth <= 0) return 380;
    const target = Math.round((containerWidth - 6) * 0.4);
    const depthMin = Math.min(320, 220 + maxDepth * INDENT_PX);
    return Math.max(NAME_MIN, depthMin, Math.min(target, NAME_MAX, containerWidth * 0.65));
  }, [containerWidth, maxDepth]);

  const colWidth = nameWidth ?? defaultNameWidth;

  const onResizeStart = (e: React.MouseEvent) => {
    e.preventDefault();
    dragRef.current = { startX: e.clientX, startW: colWidth };
    document.body.style.cursor = 'col-resize';
    document.body.style.userSelect = 'none';
  };

  useEffect(() => {
    const onMove = (e: MouseEvent) => {
      if (!dragRef.current) return;
      const w = dragRef.current.startW + (e.clientX - dragRef.current.startX);
      setNameWidth(Math.max(NAME_MIN, Math.min(NAME_MAX, w)));
    };
    const onUp = () => {
      if (!dragRef.current) return;
      dragRef.current = null;
      document.body.style.cursor = '';
      document.body.style.userSelect = '';
    };
    window.addEventListener('mousemove', onMove);
    window.addEventListener('mouseup', onUp);
    return () => {
      window.removeEventListener('mousemove', onMove);
      window.removeEventListener('mouseup', onUp);
    };
  }, []);

  const onResizeDoubleClick = () => setNameWidth(null);

  const toggle = (id: string, e: React.MouseEvent) => {
    e.stopPropagation();
    const next = new Set(collapsed);
    next.has(id) ? next.delete(id) : next.add(id);
    setCollapsed(next);
  };

  // Stable per-service colour (the left stripe + the bar + the
  // service badge). Hashing on serviceName ALONE means every span
  // emitted by `user-service` gets the same colour anywhere in the
  // trace — the standard Uptrace/Tempo convention for "scan the row
  // colours to spot service handoffs". (Earlier we hashed on
  // serviceName+name, which gave each operation in a service its
  // own colour and made traces look noisier than the topology
  // actually was.)
  const colorFor = (s: SpanRow) =>
    s.statusCode === 'error' ? '#ff5252' : hashColor(s.serviceName);

  return (
    <div id="wf-outer" ref={containerRef}>
      <div className="wf-header">
        <div className="wf-col-name" style={{ width: colWidth }}>Span</div>
        <div className="wf-resizer"
          title="Drag to resize · double-click to auto-fit"
          onMouseDown={onResizeStart}
          onDoubleClick={onResizeDoubleClick} />
        <div className="wf-col-bar">
          {TICKS.map(t => (
            <span key={`l${t}`}>
              <span className="wf-tick-label" style={{ left: `${t * 100}%` }}>{fmtNs(t * totalNs)}</span>
              {t > 0 && <div className="wf-vline" style={{ left: `${t * 100}%` }} />}
            </span>
          ))}
        </div>
      </div>

      {rows.map(({ span: s, depth, hasChildren, ancestorContinues, isLastSibling }) => {
        const color = colorFor(s);
        const cat = categoryOf(s);
        const startPct = ((s.startTime - minT) / totalNs * 100).toFixed(4);
        const widthPct = Math.max(0.15, ((s.endTime - s.startTime) / totalNs) * 100).toFixed(4);
        const dur = s.endTime - s.startTime;
        // Replace generic gRPC names ("grpc command", "rpc", bare method)
        // with a richer label derived from rpc.* / peer.service attrs.
        const displayName = displaySpanName(s);
        const durMs = dur / 1e6;
        const isCol = collapsed.has(s.spanId);
        const sel = s.spanId === selectedId;
        const cls = ['wf-row', s.statusCode === 'error' ? 'wf-err' : '', sel ? 'wf-sel' : ''].join(' ').trim();

        // Decide whether the duration label fits inside the bar (Tempo
        // does this — short bars get the label outside-right). 60px is
        // roughly the width of "10.5ms" at the row's font size.
        const labelInside = parseFloat(widthPct) > 6;

        return (
          <div key={s.spanId} className={cls} onClick={() => onSelect(s.spanId)}>
            {/* Left stripe — solid 3px service-color marker so the eye
                can scan service handoffs down the trace. Selected row
                gets a brighter, wider stripe to mark focus. */}
            <div className="wf-stripe" style={{ background: color }} />

            <div className="wf-row-name" style={{ width: colWidth }}>
              {/* Tree guide lines — one vertical line per ancestor that
                  still has later siblings, plus an L-shape for the row
                  itself. Indent comes from the lines, not padding, so
                  the visualization is tight at every depth. */}
              {ancestorContinues.map((cont, i) => (
                <span key={i} className={`wf-tree-v${cont ? '' : ' wf-tree-v-empty'}`}
                      style={{ left: i * INDENT_PX + 4 }} />
              ))}
              {depth > 0 && (
                <span className={`wf-tree-elbow${isLastSibling ? ' wf-tree-elbow-last' : ''}`}
                      style={{ left: (depth - 1) * INDENT_PX + 4 }} />
              )}

              <div className="wf-row-name-inner" style={{ paddingLeft: depth * INDENT_PX + 8 }}>
                {hasChildren
                  ? <button className="wf-toggle" onClick={e => toggle(s.spanId, e)}
                            aria-label={isCol ? 'Expand' : 'Collapse'}
                            title={isCol ? 'Expand' : 'Collapse'}>
                      {isCol ? '▶' : '▼'}
                    </button>
                  : <div className="wf-leaf" />}
                <span className="wf-kind" title={s.kind || 'internal'}>{kindIcon(s.kind)}</span>
                {/* Service identifier — Tempo-style soft underline.
                    The service name reads as plain text with a 2px
                    underline in the per-service hash colour, so the
                    scan-down-the-column "this is service X" signal
                    survives without a high-contrast filled chip
                    competing with the operation name. */}
                <span className="wf-svc"
                      title={`service.name: ${s.serviceName}`}
                      style={{ borderBottomColor: color }}>
                  {s.serviceName}
                </span>
                {cat && (
                  <span className="wf-cat" title={`Category: ${cat.tag}`}
                        style={{ color: cat.color, borderColor: cat.color }}>
                    {cat.tag}
                  </span>
                )}
                <span className="wf-name" title={s.name === displayName ? s.name : `raw: ${s.name}`}>
                  {displayName}
                </span>
                {s.statusCode === 'error' && (
                  <span className="wf-err-dot" title="Error">●</span>
                )}
              </div>
            </div>

            <div className="wf-resizer-row" />

            <div className="wf-row-bar">
              {TICKS.filter(t => t > 0).map(t => (
                <div key={`v${t}`} className="wf-vline" style={{ left: `${t * 100}%` }} />
              ))}
              <div
                className="wf-bar"
                title={`${displayName}\n${s.serviceName}\n${fmtNs(dur)} (${durMs.toFixed(2)}ms)`}
                style={{ left: `${startPct}%`, width: `${widthPct}%`, background: color }}
              >
                {labelInside && <span className="wf-bar-label">{fmtNs(dur)}</span>}
              </div>
              {!labelInside && (
                <span className="wf-bar-label-outside"
                      style={{ left: `calc(${startPct}% + ${widthPct}% + 4px)` }}>
                  {fmtNs(dur)}
                </span>
              )}
            </div>
          </div>
        );
      })}
    </div>
  );
}
