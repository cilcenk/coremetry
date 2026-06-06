import { useMemo, useRef, useState } from 'react';
import { useServices } from '@/lib/queries';
import { api } from '@/lib/api';
import { useQuery } from '@tanstack/react-query';
import { timeRangeToNs, fmtNum } from '@/lib/utils';
import { svcColor } from '@/components/traces/shared';
import { healthToken } from '@/lib/health';
import { Spinner, Empty } from '@/components/Spinner';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import type { TimeRange, Service, ServiceGraphResponse } from '@/lib/types';

// ServicePicker (v0.8.19 — topology rebuild Phase) — the LANDING surface of
// /topology. At thousands of services the full graph is unreadable, so the
// operator FIRST picks a service; only then do we render its focused
// neighborhood. This is a sortable, searchable table (default: error-rate
// desc — the services worth looking at float up), with a per-row connection
// count read from the GLOBAL graph's edge list (degree of the node).
//
// The picker reuses the project's shared DataTable primitive (sortable +
// resizable, persisted under the 'topology-picker' storageKey) so it behaves
// exactly like every other Coremetry list.

// PickRow extends Service with its graph-degree (connection count), computed
// from the global edge list so the operator sees the topological weight of a
// service before committing to focus it.
interface PickRow extends Service {
  connections: number;
}

const COLS: DataTableColumn<PickRow>[] = [
  { id: 'name',        label: 'Service',       sortValue: r => r.name,        naturalDir: 'asc', width: 280 },
  { id: 'spanCount',   label: 'Calls',         sortValue: r => r.spanCount,   numeric: true, width: 110 },
  { id: 'p99',         label: 'P99',           sortValue: r => r.p99DurationMs, numeric: true, width: 110 },
  { id: 'errorRate',   label: 'Err%',          sortValue: r => r.errorRate,   numeric: true, width: 100 },
  { id: 'connections', label: 'Connections',   sortValue: r => r.connections, numeric: true, width: 150 },
];

function fmtMs(ms: number): string {
  if (ms >= 1000) return (ms / 1000).toFixed(ms >= 10_000 ? 0 : 1) + 's';
  return Math.round(ms) + 'ms';
}

export function ServicePicker({ range, onPick }: {
  range: TimeRange;
  onPick: (service: string) => void;
}) {
  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);

  // The service catalogue (top-N by span volume) — the rows of the picker.
  const services = useServices({ from, to }, { limit: 500 });
  // The GLOBAL graph, fetched ONCE — used here only for per-service degree
  // (edge count), and reused by FocusedNeighborhood (same query key → shared
  // cache, no second round-trip) for the BFS.
  const graph = useQuery<ServiceGraphResponse>({
    queryKey: ['servicegraph', 'global', '', from, to],
    queryFn: () => api.serviceGraph({ scope: 'global', from, to }),
    staleTime: 30_000,
  });

  // degree(svc) — number of global edges touching the service (as caller OR
  // callee). Built once per graph payload, O(edges).
  const degree = useMemo(() => {
    const m = new Map<string, number>();
    for (const e of graph.data?.edges ?? []) {
      m.set(e.source, (m.get(e.source) ?? 0) + 1);
      m.set(e.target, (m.get(e.target) ?? 0) + 1);
    }
    return m;
  }, [graph.data]);

  const [filter, setFilter] = useState('');
  const searchRef = useRef<HTMLInputElement | null>(null);

  const rows = useMemo<PickRow[]>(() => {
    const q = filter.trim().toLowerCase();
    return (services.data ?? [])
      .filter(s => !q || s.name.toLowerCase().includes(q))
      .map(s => ({ ...s, connections: degree.get(s.name) ?? 0 }));
  }, [services.data, degree, filter]);

  const dt = useDataTable<PickRow>({
    storageKey: 'topology-picker',
    columns: COLS,
    rows,
    initialSort: { id: 'errorRate', dir: 'desc' },
    onOpen: r => onPick(r.name),
    searchRef,
  });

  return (
    <div style={{ maxWidth: 1000, margin: '0 auto', padding: '4px 2px' }}>
      <div style={{ marginBottom: 14 }}>
        <h2 style={{ margin: '0 0 4px', fontSize: 18, fontWeight: 700, color: 'var(--text)' }}>
          Pick a service to map its neighborhood
        </h2>
        <p style={{ margin: 0, fontSize: 13, color: 'var(--text2)', lineHeight: 1.5, maxWidth: 640 }}>
          At thousands of services the full graph is unreadable. Choose a service — Coremetry
          renders only it and its direct callers + dependencies, expandable on demand.
        </p>
      </div>

      <input
        ref={searchRef}
        value={filter}
        onChange={e => setFilter(e.target.value)}
        placeholder="Search a service to focus…"
        className="field"
        style={{ width: '100%', maxWidth: 360, marginBottom: 12 }}
      />

      {services.isLoading ? (
        <div style={{ padding: 40, display: 'grid', placeItems: 'center' }}><Spinner /></div>
      ) : services.isError ? (
        <Empty icon="⋔" title="Services unavailable">Couldn't load the service catalogue.</Empty>
      ) : rows.length === 0 ? (
        <Empty icon="🔍" title="No services match" >
          {filter ? `Nothing matches "${filter}".` : 'No services emitted spans in this window.'}
        </Empty>
      ) : (
        <table style={{ tableLayout: 'fixed', width: '100%' }}>
          <DataTableColgroup dt={dt} />
          <DataTableHead dt={dt} />
          <tbody>
            {dt.sortedRows.map((r, i) => (
              <tr
                key={r.name}
                {...dt.rowProps(i)}
                onClick={() => onPick(r.name)}
                style={{ cursor: 'pointer', contentVisibility: 'auto', containIntrinsicSize: 'auto 34px' }}
              >
                <td style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={r.name}>
                  <span style={{ display: 'inline-flex', alignItems: 'center', gap: 8 }}>
                    <span style={{ width: 9, height: 9, borderRadius: '50%', background: healthToken(r.errorRate), flexShrink: 0 }} />
                    <span style={{ width: 3, height: 14, borderRadius: 2, background: svcColor(r.name), flexShrink: 0 }} />
                    <span style={{ overflow: 'hidden', textOverflow: 'ellipsis' }}>{r.name}</span>
                  </span>
                </td>
                <td className="num">{fmtNum(r.spanCount)}</td>
                <td className="num">{fmtMs(r.p99DurationMs)}</td>
                <td className="num">
                  <span style={{
                    display: 'inline-block', padding: '1px 7px', borderRadius: 10, fontSize: 11.5,
                    fontVariantNumeric: 'tabular-nums', fontWeight: 600,
                    color: healthToken(r.errorRate),
                    background: `color-mix(in srgb, ${healthToken(r.errorRate)} 14%, transparent)`,
                  }}>
                    {r.errorRate.toFixed(1)}%
                  </span>
                </td>
                <td className="num" style={{ color: r.connections ? 'var(--text)' : 'var(--text3)' }}>
                  {r.connections} connection{r.connections === 1 ? '' : 's'} →
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}
