import { useMemo, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { useQuery, useQueries } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { useServicesMetadata } from '@/lib/queries';
import { timeRangeToNs, fmtBytes, fmtNum } from '@/lib/utils';
import { Card } from '@/components/ui';
import { Spinner, Empty } from '@/components/Spinner';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import { MetricArea } from '@/pages/clusters/MetricArea';
import { PodDrawer } from '@/pages/clusters/PodDrawer';
import { PromQLList } from '@/pages/clusters/PromQLList';
import { promQuote } from '@/pages/clusters/promQuote';
import { fmtCores, fmtBps, podPhaseBadge, restartColor } from '@/pages/clusters/thresholds';
import type { DataTableColumn } from '@/lib/dataTable';
import type { ClusterPodRow, TimeRange } from '@/lib/types';

// ServiceInfraTab — servis detayının Infrastructure sekmesi (v0.9.51,
// design handoff §8). Servisin k8s.namespace + deployment eşlemesiyle
// (metadata deriver, v0.8.436/v0.9.25) Thanos'taki pod'larını cluster
// dağılımıyla gösterir: eşleşme notu, cluster çipleri, KPI satırı,
// CPU/Mem Total/By-pod grafikleri (MetricArea), cluster-gruplu pod
// tablosu ve Clusters'la AYNI PodDrawer ("Open in Clusters →" pivotlu).
// Tüm görseller best-effort görünmez-düşer; fetch yalnız sekme
// aktifken (bileşen ancak o zaman mount olur).

const POD_COLS: DataTableColumn<ClusterPodRow>[] = [
  { id: 'cluster',  label: 'Cluster',  sortValue: r => r.cluster,  naturalDir: 'asc', width: 140 },
  { id: 'pod',      label: 'Pod',      sortValue: r => r.pod,      naturalDir: 'asc', width: 280 },
  { id: 'phase',    label: 'Status',   sortValue: r => r.phase ?? '', naturalDir: 'asc', width: 100 },
  { id: 'cpuCores', label: 'CPU',      sortValue: r => r.cpuCores, numeric: true, width: 90 },
  { id: 'memBytes', label: 'Memory',   sortValue: r => r.memBytes, numeric: true, width: 100 },
  { id: 'netIn',    label: 'Net in',   sortValue: r => r.netInBps ?? 0, numeric: true, width: 90 },
  { id: 'netOut',   label: 'Net out',  sortValue: r => r.netOutBps ?? 0, numeric: true, width: 90 },
  { id: 'restarts', label: 'Restarts', sortValue: r => r.restarts ?? 0, numeric: true, width: 84 },
];

export function ServiceInfraTab({ service, range }: { service: string; range: TimeRange }) {
  const [params, setParams] = useSearchParams();
  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);

  const metaQ = useServicesMetadata();
  const ns = metaQ.data?.[service]?.namespace ?? '';
  const deploy = metaQ.data?.[service]?.deployment ?? '';

  const sourcesQ = useQuery({
    queryKey: ['cluster-sources'],
    queryFn: () => api.clusterSources(),
    staleTime: 300_000,
  });
  // ServiceClusterBreakdown ile paylaşılan key — tek round-trip.
  const svcClustersQ = useQuery({
    queryKey: ['service-clusters', service, from, to],
    queryFn: () => api.serviceClusters(service, from, to),
    enabled: !!service && from > 0,
    staleTime: 30_000,
  });
  const matched = useMemo(() => {
    const thanos = new Set(sourcesQ.data?.clusters ?? []);
    return (svcClustersQ.data?.clusters ?? [])
      .map(c => c.cluster)
      .filter(c => thanos.has(c));
  }, [sourcesQ.data, svcClustersQ.data]);

  // Cluster başına deployment (podNames üyeliği) + pod metrikleri —
  // Clusters sayfasıyla AYNI cache slotları (tekrar fetch yok).
  const depQs = useQueries({
    queries: matched.map(c => ({
      queryKey: ['cluster-deployments', c, ns],
      queryFn: () => api.clusterDeployments(c, ns),
      staleTime: 60_000, retry: 1, enabled: ns !== '',
    })),
  });
  const podQs = useQueries({
    queries: matched.map(c => ({
      queryKey: ['cluster-pods', c],
      queryFn: () => api.clusterPods(c),
      staleTime: 60_000, retry: 1,
    })),
  });

  // Pod eşleşmesi: birincil kaynak DeploymentRow.podNames (gerçek KSM
  // eşlemesi, v0.9.23); satır yoksa README sözleşmesi "<deploy>-" önek
  // sezgiseli. ns dışı pod'lar hiç girmez. Bilinçli memo'suz: useQueries
  // dizisinin kimliği her render'da değişir (memo hiçbir şey kazandırmaz)
  // ve tarama ≤ birkaç bin satır — render başına ucuz.
  const rows: ClusterPodRow[] = [];
  matched.forEach((c, i) => {
    const depRow = (depQs[i]?.data?.deployments ?? []).find(d => d.deployment === deploy);
    const podSet = depRow ? new Set(depRow.podNames) : null;
    for (const p of podQs[i]?.data?.pods ?? []) {
      if (p.namespace !== ns) continue;
      if (podSet ? podSet.has(p.pod) : p.pod.startsWith(deploy + '-')) rows.push(p);
    }
  });

  // ?icluster= — çip filtresi (URL kaynak-of-truth, replace:true).
  const icluster = params.get('icluster') ?? '';
  const setICluster = (c: string) => setParams(prev => {
    const next = new URLSearchParams(prev);
    if (c) next.set('icluster', c); else next.delete('icluster');
    return next;
  }, { replace: true });
  const visRows = icluster ? rows.filter(r => r.cluster === icluster) : rows;

  // Grafikler aktif çipin cluster'ını izler (çip yoksa ilk eşleşen).
  const chartCluster = icluster || matched[0] || '';
  const [cpuByPod, setCpuByPod] = useState(false);
  const [memByPod, setMemByPod] = useState(false);
  // Sunucu 6h clamp'i — Clusters Overview'la aynı dürüstlük (v0.9.21).
  const { cFrom, cTo, clamped } = useMemo(() => {
    const sixH = 6 * 3600 * 1e9;
    if (to - from > sixH) return { cFrom: to - sixH, cTo: to, clamped: true };
    return { cFrom: from, cTo: to, clamped: false };
  }, [from, to]);
  const trendOK = chartCluster !== '' && ns !== '' && deploy !== '';
  const cpuTrendQ = useQuery({
    queryKey: ['deploy-trend', chartCluster, ns, deploy, 'cpu', cpuByPod, cFrom, cTo],
    queryFn: () => api.clusterDeployTrend(chartCluster, ns, deploy, 'cpu', cpuByPod, cFrom, cTo),
    staleTime: 60_000, retry: 1, enabled: trendOK,
  });
  const memTrendQ = useQuery({
    queryKey: ['deploy-trend', chartCluster, ns, deploy, 'mem', memByPod, cFrom, cTo],
    queryFn: () => api.clusterDeployTrend(chartCluster, ns, deploy, 'mem', memByPod, cFrom, cTo),
    staleTime: 60_000, retry: 1, enabled: trendOK,
  });

  // ?pod=c|ns|p — Clusters'la aynı drawer kimlik biçimi.
  const podParam = params.get('pod');
  const openPod = (r: ClusterPodRow) => setParams(prev => {
    const next = new URLSearchParams(prev);
    next.set('pod', `${r.cluster}|${r.namespace}|${r.pod}`);
    return next;
  }, { replace: true });
  const closePod = () => setParams(prev => {
    const next = new URLSearchParams(prev);
    next.delete('pod');
    return next;
  }, { replace: true });

  const dt = useDataTable<ClusterPodRow>({
    storageKey: 'service-infra-pods', columns: POD_COLS,
    rows: visRows, initialSort: { id: 'cpuCores', dir: 'desc' },
  });

  // ── Kapılar (hook'lardan SONRA) ──
  if (metaQ.isPending || sourcesQ.isPending || svcClustersQ.isPending) return <Spinner />;
  if ((sourcesQ.data?.clusters ?? []).length === 0) {
    return <Empty icon="▦" title="No Thanos clusters configured">
      Add a remote cluster under Settings → Remote clusters to see pod-level infrastructure here.
    </Empty>;
  }
  if (!ns || !deploy) {
    return <Empty icon="▦" title="No Kubernetes mapping yet">
      This service hasn't reported k8s.namespace.name / k8s.deployment.name resource
      attributes, so its pods can't be matched to a cluster.
    </Empty>;
  }
  if (matched.length === 0) {
    return <Empty icon="▦" title="No matching Thanos cluster">
      The service emits spans from clusters that aren't configured as Thanos sources.
    </Empty>;
  }

  const running = visRows.filter(r => r.phase === 'Running').length;
  const cpuSum = visRows.reduce((a, r) => a + r.cpuCores, 0);
  const memSum = visRows.reduce((a, r) => a + r.memBytes, 0);
  const restartSum = visRows.reduce((a, r) => a + (r.restarts ?? 0), 0);
  const kpiVal = { fontSize: 26, fontWeight: 700 } as const;
  const kpiSub = { fontSize: 11, color: 'var(--text3)', marginTop: 4 } as const;

  return (
    <>
      <div style={{ fontSize: 12, color: 'var(--text3)', marginBottom: 10 }}>
        Pods matched to <span className="mono">{service}</span> via{' '}
        k8s.namespace=<span className="mono">{ns}</span> · <span className="mono">{deploy}</span>{' '}
        across {matched.length} cluster{matched.length > 1 ? 's' : ''}
      </div>

      {/* Cluster çipleri — tıklanınca tablo + grafikler o cluster'a daralır. */}
      <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', marginBottom: 12 }}>
        {matched.map(c => {
          const rs = rows.filter(r => r.cluster === c);
          const failing = rs.filter(r =>
            r.phase && r.phase !== 'Running' && r.phase !== 'Succeeded').length;
          const active = icluster === c;
          return (
            <button key={c} type="button"
              onClick={() => setICluster(active ? '' : c)}
              title={active ? 'Click to clear the cluster filter' : 'Filter to this cluster'}
              style={{
                all: 'unset', cursor: 'pointer', display: 'inline-flex',
                alignItems: 'center', gap: 8, padding: '4px 10px', borderRadius: 14,
                border: `1px solid ${active ? 'var(--accent)' : 'var(--border)'}`,
                background: active ? 'var(--accent-soft)' : 'var(--bg2)', fontSize: 12,
              }}>
              <span className="mono" style={{ fontWeight: 600 }}>{c}</span>
              <span style={{ color: 'var(--text3)' }}>{rs.length} pods · {fmtCores(rs.reduce((a, r) => a + r.cpuCores, 0))} CPU</span>
              {failing > 0 && <span className="badge b-err">{failing} failing</span>}
            </button>
          );
        })}
      </div>

      {/* KPI satırı (§8): Running / CPU / Memory / Restarts. Restarts
          kube sayacının TOPLAMI (24h increase değil) — dürüst etiket. */}
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(220px, 1fr))', gap: 14 }}>
        <Card density="tight" header="Running pods">
          <div className="mono" style={{ ...kpiVal, color: 'var(--ok)' }}>{fmtNum(running)}</div>
          <div style={kpiSub}>{visRows.length - running > 0 ? `${fmtNum(visRows.length - running)} not running` : 'all pods healthy'}</div>
        </Card>
        <Card density="tight" header="CPU used (cores)">
          <div className="mono" style={kpiVal}>{visRows.length ? fmtCores(cpuSum) : '—'}</div>
        </Card>
        <Card density="tight" header="Memory used">
          <div className="mono" style={kpiVal}>{visRows.length ? fmtBytes(memSum) : '—'}</div>
        </Card>
        <Card density="tight" header="Restarts (total)">
          <div className="mono" style={{ ...kpiVal, color: restartColor(restartSum) }}>{fmtNum(restartSum)}</div>
        </Card>
      </div>

      {/* CPU/Mem area — MetricArea (Clusters ile ortak), By pod toggle. */}
      {((cpuTrendQ.data?.series?.length ?? 0) > 0 || (memTrendQ.data?.series?.length ?? 0) > 0) && (
        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 14, marginTop: 14 }}>
          <MetricArea title={`CPU (cores) · ${chartCluster}${clamped ? ' (last 6h)' : ''}`} byLabel="By pod"
            by={cpuByPod} onToggle={setCpuByPod}
            series={cpuTrendQ.data?.series} seriesName="CPU" />
          <MetricArea title={`Memory · ${chartCluster}${clamped ? ' (last 6h)' : ''}`} byLabel="By pod"
            by={memByPod} onToggle={setMemByPod}
            series={memTrendQ.data?.series} seriesName="Memory" unit="bytes" />
        </div>
      )}

      {/* Pod tablosu — cluster-gruplu (Cluster kolonu + çip filtresi). */}
      <h3 style={{ fontSize: 13, margin: '18px 0 8px' }}>
        Pods ({visRows.length}{icluster ? ` in ${icluster}` : ''})
      </h3>
      {visRows.length === 0 ? (
        <Empty icon="▦" title="No pods matched">
          The Thanos pod list has no pods for this namespace/deployment right now.
        </Empty>
      ) : (
        <div className="table-wrap">
          <table style={{ tableLayout: 'fixed', width: '100%' }}>
            <DataTableColgroup dt={dt} />
            <DataTableHead dt={dt} />
            <tbody>
              {dt.sortedRows.map(r => (
                <tr key={`${r.cluster}|${r.pod}`}
                  onClick={() => openPod(r)}
                  title="Open pod detail"
                  style={{ cursor: 'pointer', contentVisibility: 'auto', containIntrinsicSize: 'auto 36px' }}>
                  <td className="mono" style={{ fontSize: 12 }}>{r.cluster}</td>
                  <td className="mono" style={{ fontSize: 12, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={r.pod}>{r.pod}</td>
                  <td>{r.phase
                    ? <span className={`badge ${podPhaseBadge(r.phase)}`}>{r.phase}</span>
                    : <span style={{ color: 'var(--text3)' }}>—</span>}</td>
                  <td className="num mono">{fmtCores(r.cpuCores)}</td>
                  <td className="num mono">{fmtBytes(r.memBytes)}</td>
                  <td className="num mono">{(r.netInBps ?? 0) > 0 ? fmtBps(r.netInBps!) : '—'}</td>
                  <td className="num mono">{(r.netOutBps ?? 0) > 0 ? fmtBps(r.netOutBps!) : '—'}</td>
                  <td className="num mono" style={{ color: restartColor(r.restarts ?? 0) }}>{r.restarts ?? 0}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {/* Servis-kapsamlı PromQL (§8) — display-only, promQuote'lu. */}
      <div style={{ marginTop: 14, maxWidth: 720 }}>
        <Card header="Prometheus queries (service scope)">
          <PromQLList queries={[
            ['CPU (cores)', `sum(rate(container_cpu_usage_seconds_total{namespace="${promQuote(ns)}",pod=~"${promQuote(deploy)}-.*"}[5m]))`],
            ['Working-set memory', `sum(container_memory_working_set_bytes{namespace="${promQuote(ns)}",pod=~"${promQuote(deploy)}-.*"})`],
            ['Restarts (1h)', `sum(increase(kube_pod_container_status_restarts_total{namespace="${promQuote(ns)}"}[1h]))`],
            ['Ready replicas', `kube_deployment_status_replicas_ready{namespace="${promQuote(ns)}",deployment="${promQuote(deploy)}"}`],
          ]} />
        </Card>
      </div>

      {/* Pod drawer — Clusters'la AYNI bileşen + Clusters pivotu. */}
      {podParam && (() => {
        const [c, pns, p] = podParam.split('|');
        if (!c || !pns || !p) return null;
        const row = rows.find(r => r.cluster === c && r.namespace === pns && r.pod === p);
        return <PodDrawer cluster={c} namespace={pns} pod={p} row={row}
          range={range} onClose={closePod} clustersLink />;
      })()}
    </>
  );
}
