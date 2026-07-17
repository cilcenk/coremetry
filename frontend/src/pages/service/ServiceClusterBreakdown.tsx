import { useMemo } from 'react';
import { Link } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { fmtNum, timeRangeToNs } from '@/lib/utils';
import { useServicesMetadata } from '@/lib/queries';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';

// ServiceClusterBreakdown — sortable RED stats per cluster the
// service emitted spans from. Renders silently when there's
// only one cluster (or none), so single-cluster operators don't
// see noise. Click a row to pivot to /services?cluster=<name>
// scoped to that one cluster. The sort defaults to spanCount
// desc so the heaviest cluster lands at top — usually what an
// operator triaging "is this service slow?" wants first.
// Split out of the Service.tsx monolith (v0.8.252 refactor) verbatim.
const CLUSTER_COLS: DataTableColumn<import('@/lib/types').ServiceClusterStat>[] = [
  { id: 'cluster', label: 'Cluster', sortValue: r => r.cluster,       naturalDir: 'asc', width: 220 },
  { id: 'calls',   label: 'Calls',   sortValue: r => r.spanCount,     numeric: true,     width: 110 },
  { id: 'errRate', label: 'Err %',   sortValue: r => r.errorRate,     numeric: true,     width: 90 },
  { id: 'avg',     label: 'Avg',     sortValue: r => r.avgDurationMs, numeric: true,     width: 90 },
  { id: 'p99',     label: 'P99',     sortValue: r => r.p99DurationMs, numeric: true,     width: 90 },
];

export function ServiceClusterBreakdown({ service, range }: {
  service: string;
  range: import('@/lib/types').TimeRange;
}) {
  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);
  // v0.8.116 — fetch via React Query under a key shared with
  // ServiceLatencyHeatmap's cluster dropdown, so the two collapse into one
  // round trip instead of issuing the same serviceClusters call twice.
  const q = useQuery({
    queryKey: ['service-clusters', service, from, to],
    queryFn: () => api.serviceClusters(service, from, to),
    enabled: !!service && from > 0,
    staleTime: 30_000,
  });
  const clusters = useMemo(() => q.data?.clusters ?? [], [q.data]);

  // v0.8.579 — Databases-tarzı pivot (audit §7.5): cluster adı
  // Settings'teki bir Thanos kaynağıyla eşleşiyorsa "pods →" hücresi
  // /clusters'a, servisin k8s namespace'iyle (metadata deriver'ı,
  // v0.8.436) daraltılmış link verir. Hiç Thanos kaynağı yoksa kolon
  // hiç render edilmez — ölü link/boş kolon üretilmez.
  const sourcesQ = useQuery({
    queryKey: ['cluster-sources'],
    queryFn: () => api.clusterSources(),
    staleTime: 300_000,
  });
  const thanosSet = useMemo(
    () => new Set(sourcesQ.data?.clusters ?? []), [sourcesQ.data]);
  const metaQ = useServicesMetadata();
  const serviceNs = metaQ.data?.[service]?.namespace ?? '';
  const serviceDep = metaQ.data?.[service]?.deployment ?? '';
  const hasPivot = thanosSet.size > 0;
  const cols = useMemo(
    () => hasPivot
      ? [...CLUSTER_COLS, { id: 'pods', label: 'Pods', width: 70 } as DataTableColumn<import('@/lib/types').ServiceClusterStat>]
      : CLUSTER_COLS,
    [hasPivot]);

  // v0.8.116 — adopt the shared sortable + resizable primitive. This panel
  // previously hand-rolled sort via ClusterTh/ClusterSortKey — the exact
  // anti-pattern CLAUDE.md's "never hand-roll sort/resize" constraint names.
  // Hook is unconditional + above the <2-cluster early return.
  const dt = useDataTable<import('@/lib/types').ServiceClusterStat>({
    storageKey: 'service-clusters',
    columns: cols,
    rows: clusters,
    initialSort: { id: 'calls', dir: 'desc' },
  });

  // Silent when fewer than 2 clusters — single-cluster (or zero-cluster,
  // e.g. SDK without resource attrs) deployments don't need the panel.
  // Loading / error states stay quiet for the same reason.
  if (clusters.length < 2) return null;

  return (
    <div style={{ marginBottom: 14 }}>
      <div style={{
        fontSize: 11, fontWeight: 700, marginBottom: 6, color: 'var(--text2)',
        textTransform: 'uppercase', letterSpacing: 0.4,
      }}>
        Per-cluster breakdown <span style={{
          fontWeight: 400, color: 'var(--text3)', textTransform: 'none',
        }}>· {clusters.length} clusters</span>
      </div>
      <div className="table-wrap">
        <table style={{ tableLayout: 'fixed', width: '100%' }}>
          <DataTableColgroup dt={dt} />
          <DataTableHead dt={dt} />
          <tbody>
            {dt.sortedRows.map(c => {
              const errCls = c.errorRate > 5 ? 'err' : c.errorRate > 0 ? 'warn' : 'ok';
              return (
                <tr key={c.cluster}>
                  <td style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                    <Link to={`/services?cluster=${encodeURIComponent(c.cluster)}`}
                          style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }}
                          title={`Filter /services to cluster ${c.cluster}`}>
                      {c.cluster}
                    </Link>
                  </td>
                  <td className="num mono">{fmtNum(c.spanCount)}</td>
                  <td className="num mono">
                    <span className={`badge b-${errCls}`}>{c.errorRate.toFixed(2)}%</span>
                  </td>
                  <td className="num mono">{c.avgDurationMs.toFixed(1)}ms</td>
                  <td className="num mono">{c.p99DurationMs.toFixed(1)}ms</td>
                  {hasPivot && (
                    <td>
                      {thanosSet.has(c.cluster) ? (
                        <Link
                          to={`/clusters?cluster=${encodeURIComponent(c.cluster)}` +
                              `&service=${encodeURIComponent(service)}` +
                              (serviceNs ? `&namespace=${encodeURIComponent(serviceNs)}` : '') +
                              // v0.9.25 — deployment türetildiyse pivot iş-yükü
                              // hassasiyetinde iner; boşsa mevcut davranış (kırılma yok).
                              (serviceDep ? `&deployment=${encodeURIComponent(serviceDep)}` : '')}
                          style={{ fontSize: 11, color: 'var(--accent2)' }}
                          title={serviceNs
                            ? `Pod CPU/memory — ${c.cluster}, namespace ${serviceNs}`
                            : `Pod CPU/memory — ${c.cluster}`}>
                          pods →
                        </Link>
                      ) : (
                        <span style={{ fontSize: 11, color: 'var(--text3)' }}
                          title="This cluster is not defined under Settings → Remote clusters">—</span>
                      )}
                    </td>
                  )}
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </div>
  );
}
