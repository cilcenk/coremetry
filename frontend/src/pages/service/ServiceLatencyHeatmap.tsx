import { useEffect, useMemo, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { timeRangeToNs, fmtClock } from '@/lib/utils';
import { heatmapBucketCount } from '@/lib/chartStep';
import { getRaw, setRaw, STORAGE_KEYS } from '@/lib/storage';
import { Spinner } from '@/components/Spinner';
import { LatencyHeatmap } from '@/components/LatencyHeatmap';
import { heatmapFilters } from './heatmapFilters';
import { HeatmapCellExemplars } from '@/components/HeatmapCellExemplars';
import { Link } from 'react-router-dom';

// ServiceLatencyHeatmap fetches the heatmap for the current
// service + window and renders it under a collapsible
// section. Uses the existing /api/spans/heatmap endpoint
// with a single service_name filter — that endpoint already
// uses the primary-key partition prune so this is cheap
// even on a 24h window.
// Split out of the Service.tsx monolith (v0.8.252 refactor) verbatim.
// v0.8.415 (Tempo-parity T3) — optional operation scope (the page's
// ?op= selection): the distribution narrows to that one operation,
// completing the Grafana/Tempo triple of RED band + latency histogram
// over the same (service, operation) pair.
export function ServiceLatencyHeatmap({ service, range, operation = '', rootOnly = false }: {
  service: string;
  range: import('@/lib/types').TimeRange;
  operation?: string;
  rootOnly?: boolean;
}) {
  const [data, setData] = useState<import('@/lib/types').LatencyHeatmap | null | undefined>(undefined);
  const [picked, setPicked] = useState<string>(''); // '' = all
  // v0.9.379 (redesign D3, mockup af7419e5) — LatencyHeatmap primitifi
  // onCellClick/onBoxSelect'i Explore BubbleUp'tan beri taşıyor; bu sekme
  // kablolamamıştı. Tek hücre = exemplar modalı (HeatmapCellExemplars,
  // Explore ile aynı), sürükleme = "N span · Traces →" eylem çubuğu.
  const [cellExemplar, setCellExemplar] = useState<{
    timeNs: number; lowDurMs: number; highDurMs: number; count: number;
    exemplarTraceId?: string;
  } | null>(null);
  const [boxSel, setBoxSel] = useState<{
    timeFromNs: number; timeToNs: number; lowDurMs: number; highDurMs: number; count: number;
  } | null>(null);
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
    api.spanHeatmap({
      // v0.9.707 — sabit 60 → genişlik-türevi (40..240).
      from, to, buckets: heatmapBucketCount(1),
      filters: JSON.stringify(heatmapFilters(service, picked, operation, rootOnly)),
    })
      .then(r => setData(r ?? null))
      .catch(() => setData(null));
  }, [service, from, to, collapsed, picked, operation, rootOnly]);

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
        {operation && (
          <span title="Scoped to the operation picked in the RED charts above (?op=)."
            style={{
              fontSize: 11, color: 'var(--text2)',
              fontFamily: 'ui-monospace, SFMono-Regular, monospace',
              background: 'var(--bg2)', border: '1px solid var(--border)',
              borderRadius: 3, padding: '1px 6px',
              maxWidth: 260, overflow: 'hidden', textOverflow: 'ellipsis',
              whiteSpace: 'nowrap',
            }}>
            {operation}
          </span>
        )}
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
            <>
              <LatencyHeatmap data={data} height={240}
                onCellClick={(cell) => setCellExemplar(cell)}
                onBoxSelect={setBoxSel} />
              <div style={{ fontSize: 10, color: 'var(--text3)', marginTop: 6 }}>
                tek hücre = örnek trace · sürükle = zaman × gecikme bandı seç
              </div>
              {boxSel && (() => {
                const tFmt = (ns: number) => fmtClock(ns / 1e6);
                const lo = Math.max(0, Math.floor(boxSel.lowDurMs));
                const hi = Math.ceil(boxSel.highDurMs);
                const tracesHref = `/traces?service=${encodeURIComponent(service)}`
                  + (operation ? `&search=${encodeURIComponent(operation)}` : '')
                  + `&minMs=${lo}&maxMs=${hi}`
                  + `&range=custom:${Math.floor(boxSel.timeFromNs / 1e6)}-${Math.ceil(boxSel.timeToNs / 1e6)}`
                  + `&view=list&rootOnly=false`;
                return (
                  <div style={{
                    display: 'flex', gap: 10, alignItems: 'center', marginTop: 8,
                    border: '1px solid var(--accent)', borderRadius: 6,
                    padding: '4px 10px', fontSize: 12, width: 'fit-content',
                    background: 'var(--bg1)',
                  }}>
                    <b>{boxSel.count.toLocaleString()} span</b>
                    <span style={{ color: 'var(--text3)' }}>
                      {tFmt(boxSel.timeFromNs)}–{tFmt(boxSel.timeToNs)} · {lo}–{hi} ms
                    </span>
                    <Link to={tracesHref} style={{ color: 'var(--accent)', textDecoration: 'none' }}>Traces →</Link>
                    <span onClick={() => setBoxSel(null)}
                      style={{ cursor: 'pointer', color: 'var(--text3)' }}>✕</span>
                  </div>
                );
              })()}
            </>
          ))}
        </div>
      )}
      {cellExemplar && data && (
        <HeatmapCellExemplars
          cell={cellExemplar}
          exemplarTraceId={cellExemplar.exemplarTraceId}
          bucketWidthNs={data.times.length >= 2 ? data.times[1] - data.times[0] : 60 * 1e9}
          filters={heatmapFilters(service, picked, operation, rootOnly)}
          onClose={() => setCellExemplar(null)} />
      )}
    </div>
  );
}
