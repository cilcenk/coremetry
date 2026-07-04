import { useEffect, useMemo, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { timeRangeToNs } from '@/lib/utils';
import { getRaw, setRaw, STORAGE_KEYS } from '@/lib/storage';
import { Spinner } from '@/components/Spinner';
import { LatencyHeatmap } from '@/components/LatencyHeatmap';

// ServiceLatencyHeatmap fetches the heatmap for the current
// service + window and renders it under a collapsible
// section. Uses the existing /api/spans/heatmap endpoint
// with a single service_name filter — that endpoint already
// uses the primary-key partition prune so this is cheap
// even on a 24h window.
// Split out of the Service.tsx monolith (v0.8.252 refactor) verbatim.
export function ServiceLatencyHeatmap({ service, range }: {
  service: string;
  range: import('@/lib/types').TimeRange;
}) {
  const [data, setData] = useState<import('@/lib/types').LatencyHeatmap | null | undefined>(undefined);
  const [picked, setPicked] = useState<string>(''); // '' = all
  // Collapse state — defaults open. Persisted to localStorage so an operator
  // who'd rather hide the panel doesn't fight it on every reload. Keyed
  // globally (not per-service) so the preference is a one-time setting.
  const [collapsed, setCollapsed] = useState<boolean>(
    () => getRaw(STORAGE_KEYS.svcHeatmapCollapsed) === '1');
  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);

  // v0.8.116 — the parent already wraps this panel in <LazyMount> (mounts
  // within 200px of the viewport), so the former in-component
  // IntersectionObserver/hasBeenVisible gate was a redundant second lazy
  // layer and was removed. Fetches gate on !collapsed alone.

  // Cluster set for the pivot dropdown — a service usually runs across several
  // clusters at once and the operator pivots the heatmap to one (or the union
  // "All clusters" default). Shares ServiceClusterBreakdown's query key so the
  // two panels collapse into a single serviceClusters round trip.
  const clustersQ = useQuery({
    queryKey: ['service-clusters', service, from, to],
    queryFn: () => api.serviceClusters(service, from, to),
    enabled: !!service && from > 0 && !collapsed,
    staleTime: 30_000,
  });
  const clusters = useMemo(
    () => (clustersQ.data?.clusters ?? []).map(c => c.cluster),
    [clustersQ.data],
  );
  // If the previously-picked cluster vanished from the window (window moved
  // past its traffic), drop back to "All" instead of querying for nothing.
  useEffect(() => {
    if (picked && !clusters.includes(picked)) setPicked('');
  }, [clusters, picked]);

  useEffect(() => {
    if (collapsed) return;
    setData(undefined);
    const f: { key: string; op: string; value: string }[] = [
      { key: 'service.name', op: '=', value: service },
    ];
    if (picked) {
      // Hit the resource-attr key directly. The OTLP ingest path materialises
      // k8s.cluster.name as a span attr, so a single predicate is enough (no
      // coalesce across resource + span attrs needed at query time).
      f.push({ key: 'k8s.cluster.name', op: '=', value: picked });
    }
    api.spanHeatmap({ from, to, filters: JSON.stringify(f), buckets: 60 })
      .then(r => setData(r ?? null))
      .catch(() => setData(null));
  }, [service, from, to, collapsed, picked]);

  const toggle = () => {
    const next = !collapsed;
    setCollapsed(next);
    setRaw(STORAGE_KEYS.svcHeatmapCollapsed, next ? '1' : '0');
  };

  return (
    <div style={{ marginTop: 24, marginBottom: 14 }}>
      <div style={{
        display: 'flex', alignItems: 'baseline', gap: 8, marginBottom: 6,
      }}>
        <button type="button" onClick={toggle}
          style={{
            all: 'unset', cursor: 'pointer',
            fontSize: 11, fontWeight: 700, color: 'var(--text2)',
            textTransform: 'uppercase', letterSpacing: 0.4,
            display: 'inline-flex', alignItems: 'center', gap: 6,
          }}
          title={collapsed ? 'Expand' : 'Collapse'}>
          <span style={{ color: 'var(--text3)' }}>{collapsed ? '▸' : '▾'}</span>
          Latency distribution
        </button>
        {!collapsed && clusters.length >= 2 && (
          <select value={picked}
            onChange={e => setPicked(e.target.value)}
            title="Same service runs across multiple clusters — pivot the heatmap to any single cluster, or stay on the union view."
            style={{ fontSize: 11, padding: '2px 6px', marginLeft: 4 }}>
            <option value="">All clusters ({clusters.length})</option>
            {clusters.map(c => <option key={c} value={c}>{c}</option>)}
          </select>
        )}
        {!collapsed && data && data.maxCount > 0 && (
          <span style={{ fontSize: 11, color: 'var(--text3)' }}>
            peak {data.maxCount.toLocaleString()} spans/cell · log-scale y-axis
          </span>
        )}
      </div>
      {!collapsed && (
        <div style={{
          padding: 10, borderRadius: 6,
          background: 'var(--bg2)', border: '1px solid var(--border)',
        }}>
          {data === undefined && <Spinner />}
          {data === null && (
            <div style={{ fontSize: 12, color: 'var(--err)' }}>
              Failed to load latency distribution.
            </div>
          )}
          {data && (data.maxCount === 0 ? (
            <div style={{ fontSize: 12, color: 'var(--text3)' }}>
              No spans in this window.
            </div>
          ) : (
            <LatencyHeatmap data={data} height={240} />
          ))}
        </div>
      )}
    </div>
  );
}
