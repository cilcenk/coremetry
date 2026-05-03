'use client';
import { useEffect, useState } from 'react';
import { useRouter } from 'next/navigation';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { ServiceGraphSVG } from '@/components/ServiceGraphSVG';
import { Combobox } from '@/components/Combobox';
import { api } from '@/lib/api';
import { timeRangeToNs, timeRangeLabel } from '@/lib/utils';
import type { Service, ServiceEdge, TimeRange } from '@/lib/types';

export default function GraphPage() {
  const router = useRouter();
  const [range, setRange] = useState<TimeRange>({ preset: '24h' });
  const [filter, setFilter] = useState('');
  const [edges, setEdges] = useState<ServiceEdge[] | null | undefined>(undefined);
  const [services, setServices] = useState<Service[]>([]);

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

  return (
    <>
      <Topbar title="Service Graph" range={range} onRangeChange={setRange} />
      <div id="content">
        <div className="controls">
          <Combobox value={filter} onChange={setFilter}
            options={services.map(s => s.name)}
            placeholder="Filter by service…" width={240} />
          {filter && (
            <button className="sec" onClick={() => setFilter('')}>Clear</button>
          )}
          <span style={{ color: 'var(--text2)', fontSize: 12, marginLeft: 'auto' }}>
            {filter
              ? <>Showing connections of <b>{filter}</b> · {timeRangeLabel(range)}</>
              : <>All service dependencies · {timeRangeLabel(range)}</>}
          </span>
        </div>

        {edges === undefined && <Spinner />}
        {edges && edges.length === 0 && (
          <Empty icon="⬡" title={filter ? `No connections for "${filter}"` : 'No service connections yet'}>
            Connections appear when spans with <code>peer.service</code> attribute are received.
          </Empty>
        )}
        {edges && edges.length > 0 && (
          <ServiceGraphSVG services={services} edges={edges}
            highlightService={filter || undefined}
            onNodeClick={n => {
              // Click a node → set as filter (drill into its neighborhood)
              // Same node again → open the service detail (callers/callees)
              if (filter === n) router.push(`/service?name=${encodeURIComponent(n)}`);
              else setFilter(n);
            }} />
        )}
      </div>
    </>
  );
}
