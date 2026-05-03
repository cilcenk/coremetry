'use client';
import { useEffect, useMemo, useRef, useState } from 'react';
import type { SpanRow } from '@/lib/types';
import { fmtNs, hashColor } from '@/lib/utils';

const TICKS = [0, 0.25, 0.5, 0.75, 1];
const NAME_MIN = 160;
const NAME_MAX = 800;

interface Row {
  span: SpanRow;
  depth: number;
  hasChildren: boolean;
}

export function TraceWaterfall({ spans, selectedId, onSelect }: {
  spans: SpanRow[];
  selectedId: string | null;
  onSelect: (id: string) => void;
}) {
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());
  const [nameWidth, setNameWidth] = useState<number | null>(null); // null = auto-fit
  const [containerWidth, setContainerWidth] = useState(0);
  const containerRef = useRef<HTMLDivElement>(null);
  const dragRef = useRef<{ startX: number; startW: number } | null>(null);

  // Track container width so the 2:3 default can adapt to the viewport.
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
    const dfs = (id: string, depth: number) => {
      const s = map.get(id); if (!s) return;
      const kids = children.get(id) ?? [];
      out.push({ span: s, depth, hasChildren: kids.length > 0 });
      if (!collapsed.has(id)) kids.forEach(c => dfs(c.spanId, depth + 1));
    };
    roots.forEach(r => dfs(r.spanId, 0));

    const minT = Math.min(...spans.map(s => s.startTime));
    const maxT = Math.max(...spans.map(s => s.endTime));
    const totalNs = maxT - minT || 1;
    const maxDepth = out.reduce((m, r) => Math.max(m, r.depth), 0);
    return { rows: out, minT, totalNs, maxDepth };
  }, [spans, collapsed]);

  // 2:3 split — span column gets 2/5 (≈40%) of the container; bar gets 3/5.
  // Bounded by NAME_MIN/NAME_MAX and a depth-aware floor so deeply nested
  // trees still have room for indentation.
  const defaultNameWidth = useMemo(() => {
    if (containerWidth <= 0) return 380;
    const target = Math.round((containerWidth - 6) * 0.4);
    const depthMin = Math.min(320, 220 + maxDepth * 16);
    return Math.max(NAME_MIN, depthMin, Math.min(target, NAME_MAX, containerWidth * 0.65));
  }, [containerWidth, maxDepth]);

  const colWidth = nameWidth ?? defaultNameWidth;

  // ── Drag-to-resize handler ───────────────────────────────────────────────
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

  // Double-click resizer → reset to default
  const onResizeDoubleClick = () => setNameWidth(null);

  const toggle = (id: string, e: React.MouseEvent) => {
    e.stopPropagation();
    const next = new Set(collapsed);
    next.has(id) ? next.delete(id) : next.add(id);
    setCollapsed(next);
  };

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

      {rows.map(({ span: s, depth, hasChildren }) => {
        const color = s.statusCode === 'error' ? '#ff5252' : hashColor(s.serviceName + '::' + s.name);
        const startPct = ((s.startTime - minT) / totalNs * 100).toFixed(4);
        const widthPct = Math.max(0.15, ((s.endTime - s.startTime) / totalNs) * 100).toFixed(4);
        const dur = s.endTime - s.startTime;
        const isCol = collapsed.has(s.spanId);
        const indent = 8 + depth * 18;
        const sel = s.spanId === selectedId;
        const cls = ['wf-row', s.statusCode === 'error' ? 'wf-err' : '', sel ? 'wf-sel' : ''].join(' ').trim();
        return (
          <div key={s.spanId} className={cls} onClick={() => onSelect(s.spanId)}>
            <div className="wf-row-name" style={{ width: colWidth, paddingLeft: indent }}>
              {hasChildren
                ? <div className="wf-toggle" onClick={e => toggle(s.spanId, e)}>{isCol ? '▶' : '▼'}</div>
                : <div className="wf-leaf" />}
              <span className="wf-dot" style={{ color }}>●</span>
              <span className="wf-name" title={s.name}>{s.name}</span>
              <span className="wf-svc" title={s.serviceName}>{s.serviceName}</span>
              <span className="wf-dur">{fmtNs(dur)}</span>
            </div>
            <div className="wf-resizer-row" />
            <div className="wf-row-bar">
              {TICKS.filter(t => t > 0).map(t => (
                <div key={`v${t}`} className="wf-vline" style={{ left: `${t * 100}%` }} />
              ))}
              <div className="wf-bar" style={{ left: `${startPct}%`, width: `${widthPct}%`, background: color }}>
                <span className="wf-bar-label">{fmtNs(dur)}</span>
              </div>
            </div>
          </div>
        );
      })}
    </div>
  );
}
