'use client';
import { useEffect, useRef, useState } from 'react';
import type { Service, ServiceEdge } from '@/lib/types';
import { hashColor, fmtNum } from '@/lib/utils';

interface Node extends Partial<Service> {
  name: string;
  x: number;
  y: number;
}

export function ServiceGraphSVG({ services, edges, onNodeClick, highlightService }: {
  services: Service[];
  edges: ServiceEdge[];
  onNodeClick: (name: string) => void;
  highlightService?: string;
}) {
  const containerRef = useRef<HTMLDivElement>(null);
  const [tooltip, setTooltip] = useState<{ x: number; y: number; text: string } | null>(null);
  const [w, setW] = useState(900);
  const H = 520;

  useEffect(() => {
    const update = () => setW(containerRef.current?.clientWidth ?? 900);
    update();
    window.addEventListener('resize', update);
    return () => window.removeEventListener('resize', update);
  }, []);

  // Build node list (services + any names referenced by edges)
  const names = new Set<string>();
  services.forEach(s => names.add(s.name));
  edges.forEach(e => { names.add(e.source); names.add(e.target); });
  const cx = w / 2, cy = H / 2;
  const R = Math.min(w, H) * 0.38;
  const nodes: Node[] = [...names].map((name, i, arr) => {
    const angle = (2 * Math.PI * i) / arr.length - Math.PI / 2;
    const svc = services.find(s => s.name === name);
    return { name, x: cx + R * Math.cos(angle), y: cy + R * Math.sin(angle), ...svc };
  });
  const nodeMap = new Map(nodes.map(n => [n.name, n]));
  const maxCalls = Math.max(...edges.map(e => e.callCount), 1);

  return (
    <div ref={containerRef} id="graph-wrap" style={{ height: H }}>
      <svg id="graph-svg" viewBox={`0 0 ${w} ${H}`}>
        <defs>
          {edges.map((e, i) => {
            const color = e.errorRate > 10 ? '#ff5252' : e.errorRate > 0 ? '#d29922' : '#3fb950';
            return (
              <marker key={i} id={`arr-${i}`} markerWidth="8" markerHeight="6" refX="7" refY="3" orient="auto">
                <path d="M0,0 L8,3 L0,6 Z" fill={color} />
              </marker>
            );
          })}
        </defs>

        {/* Edges */}
        {edges.map((e, i) => {
          const src = nodeMap.get(e.source); const tgt = nodeMap.get(e.target);
          if (!src || !tgt) return null;
          const strokeW = Math.max(1, Math.log1p((e.callCount / maxCalls) * 100) * 1.5);
          const color = e.errorRate > 10 ? '#ff5252' : e.errorRate > 0 ? '#d29922' : '#3fb950';
          const mx = (src.x + tgt.x) / 2 + (tgt.y - src.y) * 0.25;
          const my = (src.y + tgt.y) / 2 - (tgt.x - src.x) * 0.25;
          const onEnter = (ev: React.MouseEvent) => setTooltip({
            x: ev.clientX, y: ev.clientY,
            text: `${e.source} → ${e.target}\nCalls: ${e.callCount}\nErrors: ${e.errorRate.toFixed(1)}%\nAvg: ${e.avgMs.toFixed(2)}ms`,
          });
          return (
            <g key={i}>
              <path d={`M ${src.x} ${src.y} Q ${mx} ${my} ${tgt.x} ${tgt.y}`} fill="none"
                stroke={color} strokeWidth={strokeW} strokeOpacity="0.7"
                markerEnd={`url(#arr-${i})`}
                onMouseEnter={onEnter} onMouseLeave={() => setTooltip(null)} />
              <text x={mx} y={my} fill="#8b949e" fontSize="10" textAnchor="middle"
                fontFamily="monospace" pointerEvents="none">
                {fmtNum(e.callCount)} · {e.avgMs.toFixed(1)}ms
              </text>
            </g>
          );
        })}

        {/* Nodes */}
        {nodes.map(n => {
          const color = hashColor(n.name);
          const isHL = highlightService === n.name;
          const dim  = highlightService && !isHL;
          const r = 28 + Math.min(20, Math.log1p((n.spanCount ?? 0) / 100) * 4);
          const short = n.name.length > 14 ? n.name.slice(0, 12) + '…' : n.name;
          return (
            <g key={n.name} className="graph-node" transform={`translate(${n.x},${n.y})`}
              opacity={dim ? 0.45 : 1}
              onClick={() => onNodeClick(n.name)}
              onMouseEnter={ev => setTooltip({
                x: ev.clientX, y: ev.clientY,
                text: `${n.name}\nSpans: ${fmtNum(n.spanCount ?? 0)}\nErrors: ${(n.errorRate ?? 0).toFixed(1)}%\nAvg: ${(n.avgDurationMs ?? 0).toFixed(1)}ms`,
              })}
              onMouseLeave={() => setTooltip(null)}>
              <circle r={r} fill={color} fillOpacity={isHL ? 0.4 : 0.2}
                stroke={color} strokeWidth={isHL ? 4 : 2} />
              <text textAnchor="middle" dominantBaseline="central" fill="#e6edf3"
                fontSize={isHL ? 12 : 11} fontWeight={isHL ? 700 : 600}>
                {short}
              </text>
              {(n.errorRate ?? 0) > 0 && (
                <circle r="5" cx={r * 0.7} cy={-r * 0.7} fill="#ff5252" />
              )}
            </g>
          );
        })}
      </svg>
      {tooltip && (
        <div className="graph-tooltip" style={{ left: tooltip.x + 14, top: tooltip.y - 10 }}>
          {tooltip.text.split('\n').map((l, i) => <div key={i}>{l}</div>)}
        </div>
      )}
    </div>
  );
}
