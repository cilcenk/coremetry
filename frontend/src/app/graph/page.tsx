'use client';
import { useEffect, useMemo, useState } from 'react';
import { useRouter } from 'next/navigation';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { ServiceGraphSVG } from '@/components/ServiceGraphSVG';
import { ServicePicker } from '@/components/ServicePicker';
import { GraphErrorBoundary } from '@/components/GraphErrorBoundary';
import { api } from '@/lib/api';
import { timeRangeToNs, timeRangeLabel } from '@/lib/utils';
import type { Service, ServiceEdge, TimeRange } from '@/lib/types';

export default function GraphPage() {
  const router = useRouter();
  const [range, setRange] = useState<TimeRange>({ preset: '1h' });
  const [filter, setFilter] = useState('');
  const [edges, setEdges] = useState<ServiceEdge[] | null | undefined>(undefined);
  const [services, setServices] = useState<Service[]>([]);
  // Hide low-volume edges to declutter dense graphs. Default 0
  // (show everything); the slider becomes useful past ~50 edges and
  // dominant past ~200.
  const [minCalls, setMinCalls] = useState(0);

  // Load all services for the autocomplete dropdown (independent of filter)
  useEffect(() => {
    api.services(timeRangeToNs(range)).then(s => setServices(s ?? [])).catch(() => setServices([]));
  }, [range]);

  // Load edges with current filter applied
  useEffect(() => {
    setEdges(undefined);
    api.graph(timeRangeToNs(range), filter || undefined)
      .then(e => setEdges(e ?? []))
      .catch(() => setEdges(null));
  }, [range, filter]);

  // Cap the displayed edges by the min-calls slider. Counts per node
  // recomputed too — a node loses both its edges and the slider drops
  // it from the canvas (orphans in dense graphs aren't useful).
  const visible = useMemo(() => {
    const all = edges ?? [];
    const filtered = minCalls > 0 ? all.filter(e => e.callCount >= minCalls) : all;
    const live = new Set<string>();
    filtered.forEach(e => { live.add(e.source); live.add(e.target); });
    const visibleServices = minCalls > 0
      ? services.filter(s => live.has(s.name))
      : services;
    return { edges: filtered, services: visibleServices, total: all.length };
  }, [edges, services, minCalls]);

  // Slider top — 95th-percentile call count, so the slider has useful
  // resolution at the busy end of the distribution. Clamp to a
  // sensible minimum so a tiny graph still shows the control.
  const sliderMax = useMemo(() => {
    if (!edges || edges.length === 0) return 100;
    const sorted = edges.map(e => e.callCount).sort((a, b) => a - b);
    const p95 = sorted[Math.floor(sorted.length * 0.95)] ?? sorted[sorted.length - 1];
    return Math.max(10, p95);
  }, [edges]);

  return (
    <>
      <Topbar title="Service Graph" range={range} onRangeChange={setRange} />
      <div id="content">
        <div className="controls">
          <ServicePicker value={filter} onChange={setFilter}
            placeholder="Filter by service…" width={240} />
          {filter && (
            <button className="sec" onClick={() => setFilter('')}>Clear</button>
          )}
          {edges && edges.length > 50 && (
            <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6, marginLeft: 8 }}>
              <span style={{ color: 'var(--text2)', fontSize: 11 }}>Min calls:</span>
              <input type="range" min={0} max={sliderMax} step={1}
                value={Math.min(minCalls, sliderMax)}
                onChange={e => setMinCalls(parseInt(e.target.value, 10))}
                style={{ width: 120 }} />
              <span style={{ color: 'var(--text3)', fontSize: 11, fontFamily: 'ui-monospace, monospace', minWidth: 32 }}>
                {minCalls}
              </span>
            </span>
          )}
          <span style={{ color: 'var(--text2)', fontSize: 12, marginLeft: 'auto' }}>
            {filter
              ? <>Showing connections of <b>{filter}</b> · {timeRangeLabel(range)}</>
              : <>All service dependencies · {timeRangeLabel(range)}</>}
            {edges && (
              <>
                {' · '}
                {visible.edges.length === visible.total
                  ? <>{visible.total} edge{visible.total === 1 ? '' : 's'}</>
                  : <>{visible.edges.length}/{visible.total} edges</>}
                {' · '}{visible.services.length} services
              </>
            )}
          </span>
        </div>

        {edges === undefined && <Spinner />}
        {edges && edges.length === 0 && (
          <Empty icon="⬡" title={filter ? `No connections for "${filter}"` : 'No service connections yet'}>
            Connections appear when spans with <code>peer.service</code> attribute are received.
          </Empty>
        )}
        {edges && edges.length > 0 && (
          // Wrap the topology canvas — at hundreds of services any
          // numerical instability in the layout (NaN, Infinity)
          // would otherwise blank the whole page. ErrorBoundary
          // shows a recovery banner + a "switch to circular layout"
          // button instead.
          <GraphErrorBoundary>
            <ServiceGraphSVG services={visible.services} edges={visible.edges}
              highlightService={filter || undefined}
              onNodeClick={n => {
                if (filter === n) router.push(`/service?name=${encodeURIComponent(n)}`);
                else setFilter(n);
              }} />
          </GraphErrorBoundary>
        )}
      </div>
    </>
  );
}
