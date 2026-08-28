import { useMemo } from 'react';
import { Link } from 'react-router-dom';
import { Stack, Row } from '@/components/ui';
import { Badge } from '@/components/ui/Badge';
import { Spinner, Empty } from '@/components/Spinner';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import { useEntityEnabled, useEntityServicePods } from '@/lib/queries';
import { timeRangeToNs, fmtDateTime } from '@/lib/utils';
import type { ServicePodRow, TimeRange } from '@/lib/types';

// ServiceEntityPods — v0.10.131 (design §5): servisi taşıyan pod'lar,
// entity katmanından (entity_seen_5m servis öneki + entities/relations).
// Yalnız bayrak AÇIKKEN render edilir (useEntityEnabled viewer-güvenli);
// Infra sekmesi açılınca fetch (ES-cost disiplini). Cluster verilmezse
// tüm cluster'lar okunur ve `clusterAmbiguous` ilan edilir.

const COLS: DataTableColumn<ServicePodRow>[] = [
  { id: 'pod', label: 'Pod', width: 260, sortValue: r => r.pod },
  { id: 'ns', label: 'Namespace', width: 140, sortValue: r => r.namespace },
  { id: 'cluster', label: 'Cluster', width: 140, sortValue: r => r.cluster },
  { id: 'node', label: 'Node', width: 140, sortValue: r => r.node ?? '' },
  { id: 'workload', label: 'Workload', width: 200, sortValue: r => r.entity?.parentId ?? '' },
  { id: 'spans', label: 'Spans', width: 90, numeric: true, sortValue: r => r.spans },
  { id: 'errors', label: 'Errors', width: 80, numeric: true, sortValue: r => r.errors },
  { id: 'avg', label: 'Avg ms', width: 90, numeric: true, sortValue: r => r.avgMs },
  { id: 'since', label: 'Lifetime since', width: 170, sortValue: r => r.entity?.validFrom ?? '' },
  { id: 'links', label: '', width: 120, sortValue: () => '' },
];

function workloadLabel(parentId?: string): string {
  if (!parentId) return '';
  // wl:<cid>/<ns>/<kind>/<name> → kind/name ; ns:<cid>/<ns> → namespace
  const m = /^wl:[^/]+\/[^/]+\/([^/]+)\/(.+)$/.exec(parentId);
  if (m) return `${m[1]}/${m[2]}`;
  return parentId.startsWith('ns:') ? '(no workload)' : parentId;
}

export function ServiceEntityPods({ service, range, cluster }: { service: string; range: TimeRange; cluster?: string }) {
  const { enabled } = useEntityEnabled();
  const win = useMemo(() => timeRangeToNs(range), [range]);
  const q = useEntityServicePods(service, cluster ?? '', win, enabled);
  const rows = q.data?.pods ?? [];
  const dt = useDataTable<ServicePodRow>({
    storageKey: 'service-entity-pods',
    columns: COLS,
    rows,
    initialSort: { id: 'spans', dir: 'desc' },
  });
  if (!enabled) return null;
  return (
    <Stack gap={2} style={{ marginBottom: 16 }}>
      <Row gap={3} style={{ alignItems: 'baseline' }}>
        <h3 style={{ margin: 0 }}>Pods (entity layer)</h3>
        {q.data?.clusterAmbiguous && q.data.clusterAmbiguous.length > 1 && (
          <Badge tone="warning" title="Same service name in more than one cluster — pick a cluster chip to narrow">
            {q.data.clusterAmbiguous.length} clusters
          </Badge>
        )}
        {q.data?.unmappedClusters && q.data.unmappedClusters.length > 0 && (
          <Badge tone="warning" title={`Span cluster values without a Remote Cluster record: ${q.data.unmappedClusters.join(', ')}`}>
            unmapped: {q.data.unmappedClusters.join(', ')}
          </Badge>
        )}
      </Row>
      {q.isPending ? <Spinner /> : q.error ? (
        <Empty icon="!" title="Pods could not be loaded">{String(q.error)}</Empty>
      ) : rows.length === 0 ? (
        <Empty icon="∅" title="No pods seen for this service in the window">entity_seen_5m has no rows — spans without k8s.pod.name, or the window is empty.</Empty>
      ) : (
        <div style={{ overflowX: 'auto' }}>
          <table style={{ tableLayout: 'fixed', width: '100%' }}>
            <DataTableColgroup dt={dt} />
            <DataTableHead dt={dt} />
            <tbody>
              {dt.sortedRows.map(r => {
                const podHref = `/pod?cluster=${encodeURIComponent(r.clusterId ?? r.cluster)}&namespace=${encodeURIComponent(r.namespace)}&pod=${encodeURIComponent(r.pod)}&service=${encodeURIComponent(service)}`;
                const filters = encodeURIComponent(JSON.stringify([{ k: 'k8s.pod.name', op: '=', v: [r.pod] }]));
                const tracesHref = `/traces?service=${encodeURIComponent(service)}&filters=${filters}${r.cluster ? `&cluster=${encodeURIComponent(r.cluster)}` : ''}`;
                return (
                  <tr key={`${r.cluster}/${r.namespace}/${r.pod}`} style={rows.length > 100 ? { contentVisibility: 'auto', containIntrinsicSize: '0 32px' } : undefined}>
                    <td className="mono" title={r.entityId ?? ''}>
                      {r.pod}
                      {r.entity?.nameStable && <Badge tone="neutral" style={{ marginLeft: 6 }} title="No pod uid — lifetimes split by podGap only">name-stable</Badge>}
                    </td>
                    <td className="mono">{r.namespace}</td>
                    <td className="mono">{r.cluster}</td>
                    <td className="mono">{r.node ?? r.entity?.labels?.node ?? ''}</td>
                    <td className="mono">{workloadLabel(r.entity?.parentId)}</td>
                    <td className="num">{r.spans}</td>
                    <td className="num">{r.errors}</td>
                    <td className="num">{r.avgMs.toFixed(1)}</td>
                    <td className="mono">{r.entity ? fmtDateTime(new Date(r.entity.validFrom)) : ''}</td>
                    <td>
                      <Link to={podHref} className="sec">Pod</Link>{' '}
                      <Link to={tracesHref} className="sec">Traces</Link>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </Stack>
  );
}
