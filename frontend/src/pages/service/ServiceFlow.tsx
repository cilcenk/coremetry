import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Link } from 'react-router-dom';
import { Spinner } from '@/components/Spinner';
import { api } from '@/lib/api';
import { encodeRange } from '@/lib/urlState';
import type { TimeRange } from '@/lib/types';

// ServiceFlow (v0.7.95) — the request-path map from the design handoff:
// callers (inbound) → the focused service → dependencies (outbound:
// services + DB + queue). Data is the 1-hop slice of the service topology
// (api.serviceTopology focus=svc). SVG bezier wires are measured from the
// node DOM rects (ResizeObserver) and highlight on node hover.

interface FlowNode {
  name: string;       // display name
  to?: string;        // /service link target (services only)
  kind: string;       // 'service' | 'db' | 'queue' | 'external'
  calls: number;
  avgMs: number;
  errRate: number;
}

interface Wire { key: string; side: 'in' | 'out'; i: number; x1: number; y1: number; x2: number; y2: number }

function dotLevel(errRate: number): 'green' | 'amber' | 'red' {
  return errRate > 5 ? 'red' : errRate > 1 ? 'amber' : 'green';
}
function fnum(n: number): string {
  return n >= 1000 ? `${(n / 1000).toFixed(1)}K` : `${n}`;
}
function kindLabel(kind: string): string {
  return kind === 'db' ? 'database' : kind === 'queue' ? 'queue' : kind === 'external' ? 'external' : 'service';
}
// Strip the topology node prefix ("db:h2" → "h2", "queue:orders" → "orders").
function cleanName(raw: string): string {
  const i = raw.indexOf(':');
  return i > 0 && i < 8 ? raw.slice(i + 1) : raw;
}

function NodeCard({ d, side, i, hot, anyHot, onHot, refCb }: {
  d: FlowNode; side: 'in' | 'out' | 'center'; i: number;
  hot: boolean; anyHot: boolean; onHot: (h: { side: 'in' | 'out'; i: number } | null) => void;
  refCb?: (el: HTMLElement | null) => void;
}) {
  const cls = 'ov-node'
    + (side === 'center' ? ' center' : '')
    + (hot ? ' hot' : '')
    + (anyHot && !hot && side !== 'center' ? ' dim' : '');
  const inner = (
    <>
      {side !== 'center' && <span className={`ov-dot ${dotLevel(d.errRate)}`} />}
      <div style={{ minWidth: 0 }}>
        <div className="ov-nm" style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{d.name}</div>
        <div className="ov-st">{side === 'center'
          ? `${fnum(d.calls)} calls · ${d.errRate.toFixed(1)}% err · ${d.avgMs.toFixed(0)}ms`
          : kindLabel(d.kind)}</div>
      </div>
      {side !== 'center' && (
        <div className="ov-edge-stat"><b>{fnum(d.calls)}</b><br />{d.avgMs.toFixed(0)}ms · {d.errRate.toFixed(1)}%</div>
      )}
    </>
  );
  const common = {
    className: cls, ref: refCb,
    onMouseEnter: () => side !== 'center' && onHot({ side, i }),
    onMouseLeave: () => side !== 'center' && onHot(null),
  };
  return d.to
    ? <Link {...common} to={d.to} style={{ textDecoration: 'none', color: 'inherit' }}>{inner}</Link>
    : <div {...common}>{inner}</div>;
}

export function ServiceFlow({ service, range, from, to }: {
  service: string; range: TimeRange; from: number; to: number;
}) {
  const topoQ = useQuery({
    queryKey: ['service-overview-flow', service, from, to],
    queryFn: () => api.serviceTopology({ from, to, focus: service, hops: 1 }),
    enabled: !!service,
    staleTime: 30_000,
  });

  const rangeParam = encodeRange(range);
  const { callers, deps } = useMemo(() => {
    const edges = topoQ.data?.edges ?? [];
    const cIn: FlowNode[] = [];
    const cOut: FlowNode[] = [];
    for (const e of edges) {
      if (e.childNode === service) {
        cIn.push({ name: e.parentService, to: `/service?name=${encodeURIComponent(e.parentService)}&range=${rangeParam}`, kind: 'service', calls: e.calls, avgMs: e.avgMs, errRate: e.errorRate });
      } else if (e.parentService === service) {
        const svc = e.nodeKind === 'service';
        cOut.push({ name: cleanName(e.childNode), to: svc ? `/service?name=${encodeURIComponent(e.childNode)}&range=${rangeParam}` : undefined, kind: e.nodeKind, calls: e.calls, avgMs: e.avgMs, errRate: e.errorRate });
      }
    }
    cIn.sort((a, b) => b.calls - a.calls);
    cOut.sort((a, b) => b.calls - a.calls);
    return { callers: cIn.slice(0, 6), deps: cOut.slice(0, 7) };
  }, [topoQ.data, service, rangeParam]);

  const center: FlowNode = useMemo(() => {
    // Aggregate the inbound (caller) edges: total calls + call-weighted
    // error rate AND latency, so the center node shows a real
    // "N calls · X% err · Yms" stat instead of a hardcoded 0ms.
    const totalCalls = callers.reduce((s, c) => s + c.calls, 0);
    const wErr = callers.reduce((s, c) => s + c.errRate * c.calls, 0);
    const wAvg = callers.reduce((s, c) => s + c.avgMs * c.calls, 0);
    return {
      name: service, kind: 'service', calls: totalCalls,
      avgMs: totalCalls ? wAvg / totalCalls : 0,
      errRate: totalCalls ? wErr / totalCalls : 0,
    };
  }, [callers, service]);

  // ── SVG wire measurement ──────────────────────────────────────────────
  const wrapRef = useRef<HTMLDivElement>(null);
  const centerRef = useRef<HTMLElement | null>(null);
  const inRefs = useRef<(HTMLElement | null)[]>([]);
  const outRefs = useRef<(HTMLElement | null)[]>([]);
  const [wires, setWires] = useState<Wire[]>([]);
  const [hot, setHot] = useState<{ side: 'in' | 'out'; i: number } | null>(null);

  useLayoutEffect(() => {
    const measure = () => {
      const wrap = wrapRef.current, C = centerRef.current;
      if (!wrap || !C) { setWires([]); return; }
      const cb = wrap.getBoundingClientRect();
      const CB = C.getBoundingClientRect();
      const cl = { x: CB.left - cb.left, y: CB.top - cb.top + CB.height / 2, r: CB.right - cb.left };
      const w: Wire[] = [];
      callers.forEach((_, i) => {
        const el = inRefs.current[i]; if (!el) return;
        const b = el.getBoundingClientRect();
        w.push({ key: `in${i}`, side: 'in', i, x1: b.right - cb.left, y1: b.top - cb.top + b.height / 2, x2: cl.x, y2: cl.y });
      });
      deps.forEach((_, i) => {
        const el = outRefs.current[i]; if (!el) return;
        const b = el.getBoundingClientRect();
        w.push({ key: `out${i}`, side: 'out', i, x1: cl.r, y1: cl.y, x2: b.left - cb.left, y2: b.top - cb.top + b.height / 2 });
      });
      setWires(w);
    };
    measure();
    const ro = new ResizeObserver(measure);
    if (wrapRef.current) ro.observe(wrapRef.current);
    window.addEventListener('resize', measure);
    return () => { ro.disconnect(); window.removeEventListener('resize', measure); };
  }, [callers, deps]);

  const isHot = (side: 'in' | 'out', i: number) => !!hot && hot.side === side && hot.i === i;
  const anyHot = hot != null;

  // v0.8.308 (quality bar P6) — the load state used to render the card frame
  // with zero nodes (a blank panel) and an error left it that way forever.
  // Loading gets a Spinner in the same card so the layout doesn't jump;
  // an error hides the panel like the no-flow case (overview stays tight).
  if (topoQ.isLoading) {
    return (
      <div className="card ov-mb">
        <div className="ov-card-h"><h3>Service flow</h3><span className="ov-sub">1-hop request path</span></div>
        <div className="ov-card-b" style={{ display: 'grid', placeItems: 'center', minHeight: 120 }}><Spinner /></div>
      </div>
    );
  }
  if (topoQ.isError || (topoQ.data && callers.length === 0 && deps.length === 0)) {
    return null; // no flow to show — keep the overview tight
  }

  return (
    <div className="card ov-mb">
      <div className="ov-card-h"><h3>Service flow</h3><span className="ov-sub">1-hop request path</span></div>
      <div className="ov-card-b">
        <div className="ov-flow" ref={wrapRef}>
          <svg className="ov-wires" aria-hidden="true">
            {wires.map(wr => {
              const h = isHot(wr.side, wr.i);
              const mx = (wr.x1 + wr.x2) / 2;
              return (
                <path key={wr.key}
                  d={`M${wr.x1},${wr.y1} C${mx},${wr.y1} ${mx},${wr.y2} ${wr.x2},${wr.y2}`}
                  fill="none"
                  stroke={h ? 'var(--accent)' : 'var(--border-strong)'}
                  strokeWidth={h ? 2 : 1.4}
                  opacity={anyHot && !h ? 0.35 : 1} />
              );
            })}
          </svg>
          <div className="ov-flow-cols">
            <div className="ov-flow-col">
              <div className="ov-col-lab">Callers</div>
              {callers.length === 0 && <div className="ov-st" style={{ textAlign: 'center' }}>—</div>}
              {callers.map((d, i) => (
                <NodeCard key={d.name} d={d} side="in" i={i} hot={isHot('in', i)} anyHot={anyHot} onHot={setHot}
                  refCb={el => { inRefs.current[i] = el; }} />
              ))}
            </div>
            <div className="ov-flow-col">
              <div className="ov-col-lab">Service</div>
              <NodeCard d={center} side="center" i={0} hot={false} anyHot={anyHot} onHot={setHot}
                refCb={el => { centerRef.current = el; }} />
            </div>
            <div className="ov-flow-col">
              <div className="ov-col-lab">Dependencies</div>
              {deps.length === 0 && <div className="ov-st" style={{ textAlign: 'center' }}>—</div>}
              {deps.map((d, i) => (
                <NodeCard key={d.name} d={d} side="out" i={i} hot={isHot('out', i)} anyHot={anyHot} onHot={setHot}
                  refCb={el => { outRefs.current[i] = el; }} />
              ))}
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}
