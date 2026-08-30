// ServiceEntityPods — v0.10.131 (entity katmanı, Service → Infra) + v0.10.136
// (DETAY SAYFALARI adım 2): servisi taşıyan pod'lar entity_seen_5m'den;
// durum/CPU/Mem Thanos KSM anlık (cluster başına tek hedefli sorgu);
// p50/p95/p99 giriş-span ham spans; zincir şeridi (cluster › namespace ›
// workload, pod sayısıyla). HER link entityHref üzerinden — doğru cluster,
// range korunur; cluster eşlenmemiş satırda link YOK, rozet açık ilan eder
// (brief kuralı 4). Bayrak kapalı → null.
import { useEffect, useMemo } from 'react';
import { Link } from 'react-router-dom';
import { Stack, Row } from '@/components/ui';
import { Badge } from '@/components/ui/Badge';
import { Spinner, Empty } from '@/components/Spinner';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import { useEntityEnabled, useEntityServicePods } from '@/lib/queries';
import { timeRangeToNs, fmtDateTime, fmtBytes } from '@/lib/utils';
import { fmtCores, podPhaseBadge } from '@/pages/clusters/thresholds';
import { entityHref, entityLiveness } from '@/lib/entityHref';
import type { ServicePodRow, ServicePodsChainItem, TimeRange } from '@/lib/types';

const COLS: DataTableColumn<ServicePodRow>[] = [
  { id: 'pod', label: 'Pod', width: 240, sortValue: r => r.pod },
  { id: 'status', label: 'Status', width: 220, sortValue: r => (r.statusKnown ? r.phase ?? '' : '') },
  { id: 'ns', label: 'Namespace', width: 120, sortValue: r => r.namespace },
  { id: 'cluster', label: 'Cluster', width: 110, sortValue: r => r.cluster },
  { id: 'node', label: 'Node', width: 120, sortValue: r => r.node ?? '' },
  { id: 'workload', label: 'Workload', width: 170, sortValue: r => r.entity?.parentId ?? '' },
  { id: 'cpu', label: 'CPU', width: 80, numeric: true, sortValue: r => (r.statusKnown ? r.cpuCores ?? 0 : -1) },
  { id: 'mem', label: 'Mem', width: 90, numeric: true, sortValue: r => (r.statusKnown ? r.memBytes ?? 0 : -1) },
  { id: 'spans', label: 'Spans', width: 80, numeric: true, sortValue: r => r.spans },
  { id: 'errpct', label: 'Err %', width: 70, numeric: true, sortValue: r => (r.spans ? r.errors / r.spans : 0) },
  { id: 'p50', label: 'P50 ms', width: 76, numeric: true, sortValue: r => r.p50Ms ?? -1 },
  { id: 'p95', label: 'P95 ms', width: 76, numeric: true, sortValue: r => r.p95Ms ?? -1 },
  { id: 'p99', label: 'P99 ms', width: 76, numeric: true, sortValue: r => r.p99Ms ?? -1 },
  { id: 'last', label: 'Last seen', width: 150, sortValue: r => r.lastSeen },
  { id: 'links', label: '', width: 80, sortValue: () => '' },
];

const WL_RE = /^wl:([^/]+)\/([^/]+)\/([^/]+)\/(.+)$/;

function ms(v?: number): string {
  if (v === undefined || v <= 0) return '—';
  return v < 10 ? v.toFixed(1) : v.toFixed(0);
}

function ChainStrip({ chain, range, nameOf }: { chain: ServicePodsChainItem[]; range: TimeRange; nameOf: (cid: string) => string }) {
  return (
    <Row gap={3} wrap>
      <span className="field-hint">runs in:</span>
      {chain.map(c => {
        const cn = nameOf(c.clusterId);
        const nsName = c.type === 'namespace' ? c.name : c.namespace;
        return (
          <Row key={c.id} gap={2}>
            <Link to={entityHref({ type: 'cluster', id: `cluster:${c.clusterId}`, name: cn, clusterId: c.clusterId }, { range })} className="sec" title={c.clusterId}>{cn}</Link>
            {nsName && (
              <>
                <span className="field-hint">›</span>
                <Link to={entityHref({ type: 'namespace', id: `ns:${c.clusterId}/${nsName}`, name: nsName, clusterId: c.clusterId }, { range })} className="sec">{nsName}</Link>
              </>
            )}
            {c.type === 'workload' && (
              <>
                <span className="field-hint">›</span>
                <Link to={entityHref({ type: 'workload', id: c.id, name: c.name, namespace: c.namespace, clusterId: c.clusterId }, { range })} className="sec" title={c.id}>
                  {c.kind ?? 'workload'}/{c.name}
                </Link>
              </>
            )}
            <span className="field-hint">({c.pods} pod{c.pods > 1 ? 's' : ''})</span>
          </Row>
        );
      })}
    </Row>
  );
}

// onRows (v0.10.149) — üst bileşen (Pods sekmesi) entity satırı varken Thanos
// envanterini kapalı başlatır; bayrak kapalı ya da 0 satır → 0.
export function ServiceEntityPods({ service, range, cluster, onRows }: { service: string; range: TimeRange; cluster?: string; onRows?: (n: number) => void }) {
  const { enabled, clusters } = useEntityEnabled();
  const win = useMemo(() => timeRangeToNs(range), [range]);
  const q = useEntityServicePods(service, cluster ?? '', win, enabled);
  const rows = q.data?.pods ?? [];
  const rowCount = enabled ? rows.length : 0;
  useEffect(() => { onRows?.(rowCount); }, [rowCount, onRows]);
  const dt = useDataTable<ServicePodRow>({
    storageKey: 'service-entity-pods',
    columns: COLS,
    rows,
    initialSort: { id: 'spans', dir: 'desc' },
  });
  if (!enabled) return null;
  const nameOf = (cid: string) => clusters.find(c => c.id === cid)?.name ?? cid;
  const chain = q.data?.chain ?? [];
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
        {q.data?.statusNotes?.map(n => <Badge key={n} tone="warning" title={n}>{n.length > 34 ? n.slice(0, 34) + '…' : n}</Badge>)}
      </Row>
      {chain.length > 0 && <ChainStrip chain={chain} range={range} nameOf={nameOf} />}
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
                const cid = r.clusterId ?? '';
                const clusterName = cid ? nameOf(cid) : r.cluster;
                const podRec = cid ? { type: 'pod' as const, id: r.entityId ?? `pod:${cid}/${r.namespace}/${r.pod}`, name: r.pod, namespace: r.namespace, clusterId: cid } : null;
                const live = r.entity ? entityLiveness(r.entity) : null;
                const wl = r.entity?.parentId ? WL_RE.exec(r.entity.parentId) : null;
                const filters = encodeURIComponent(JSON.stringify([{ k: 'k8s.pod.name', op: '=', v: [r.pod] }]));
                const tracesHref = `/traces?service=${encodeURIComponent(service)}&filters=${filters}${r.cluster ? `&cluster=${encodeURIComponent(r.cluster)}` : ''}`;
                const errPct = r.spans ? (100 * r.errors) / r.spans : 0;
                return (
                  <tr key={`${r.cluster}/${r.namespace}/${r.pod}`} style={rows.length > 100 ? { contentVisibility: 'auto', containIntrinsicSize: '0 32px' } : undefined}>
                    <td className="mono" title={`${clusterName} / ${r.namespace} / ${r.pod}`}>
                      {podRec
                        ? <Link to={entityHref(podRec, { range, clusterName, service })} className="sec">{r.pod}</Link>
                        : <span title="Cluster eşlenmemiş — entity kaydı yok, link yok">{r.pod}</span>}
                      {live === 'gone' && <Badge tone="danger" style={{ marginLeft: 6 }} title={`Artık mevcut değil · son görülme ${fmtDateTime(new Date(r.entity!.lastSeen))}`}>gone</Badge>}
                      {live === 'stale' && <Badge tone="warning" style={{ marginLeft: 6 }} title="Son senkronda görülmedi">stale</Badge>}
                      {r.entity?.nameStable && <Badge tone="neutral" style={{ marginLeft: 6 }} title="No pod uid — lifetimes split by podGap only">name-stable</Badge>}
                    </td>
                    <td style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                      {r.statusKnown ? (
                        <span title={`${r.phase || '?'}${r.restartsUnknown ? '' : r.restarts ? ` · ${r.restarts} restart` : ''}${r.lastTermReason ? ` · last terminated: ${r.lastTermReason}` : ''}`}>
                          <span className={`badge ${podPhaseBadge(r.phase ?? '')}`}>{r.phase || '?'}</span>
                          {r.restartsUnknown ? '' : r.restarts ? ` · ${r.restarts}↻` : ''}
                          {r.lastTermReason ? ` · ${r.lastTermReason}` : ''}
                        </span>
                      ) : <span className="field-hint" title="Thanos'ta bu pod için seri yok (ölü ya da KSM dışı)">—</span>}
                    </td>
                    <td className="mono">{r.namespace}</td>
                    <td className="mono" title={cid || 'unmapped'}>{clusterName}</td>
                    <td className="mono">
                      {r.node && cid
                        ? <Link to={entityHref({ type: 'node', id: `node:${cid}/${r.node}`, name: r.node, clusterId: cid }, { range })} className="sec">{r.node}</Link>
                        : (r.node ?? r.entity?.labels?.node ?? '')}
                    </td>
                    <td className="mono">
                      {wl
                        ? <Link to={entityHref({ type: 'workload', id: r.entity!.parentId!, name: wl[4], namespace: wl[2], clusterId: wl[1] }, { range })} className="sec">{wl[3]}/{wl[4]}</Link>
                        : (r.entity?.parentId?.startsWith('ns:') ? '(no workload)' : '')}
                    </td>
                    <td className="num">{r.statusKnown ? fmtCores(r.cpuCores ?? 0) : '—'}</td>
                    <td className="num">{r.statusKnown ? fmtBytes(r.memBytes ?? 0) : '—'}</td>
                    <td className="num">{r.spans}</td>
                    <td className="num" style={errPct >= 5 ? { color: 'var(--err)' } : undefined}>{errPct.toFixed(1)}</td>
                    <td className="num">{ms(r.p50Ms)}</td>
                    <td className="num">{ms(r.p95Ms)}</td>
                    <td className="num">{ms(r.p99Ms)}</td>
                    <td className="mono">{fmtDateTime(new Date(r.lastSeen))}</td>
                    <td><Link to={tracesHref} className="sec">Traces</Link></td>
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
