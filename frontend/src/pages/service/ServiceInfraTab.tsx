import { useRef, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { Card } from '@/components/ui';
import { Spinner, Empty } from '@/components/Spinner';
import { MetricArea } from '@/pages/clusters/MetricArea';
import { PromQLList } from '@/pages/clusters/PromQLList';
import { promQuote } from '@/pages/clusters/promQuote';
import { fmtCores, restartColor } from '@/pages/clusters/thresholds';
import { fmtBytes, fmtNum } from '@/lib/utils';
import { useServicePods } from '@/pages/service/useServicePods';
import type { TimeRange } from '@/lib/types';

// ServiceInfraTab — servis detayının Infrastructure sekmesi. CLUSTER-SEVİYESİ
// altyapı: eşleşme notu, cluster çipleri (?icluster), KPI satırı, CPU/Mem
// Total/By-pod grafikleri (MetricArea), servis-kapsamlı PromQL.
//
// v0.9.158 (operatör "Metrics→Pods, infra pod'ları oraya, JVM panellerde"):
// pod listesi (açılır grup) + JVM/JBoss JMX panelleri Pods sekmesine TAŞINDI
// (ServicePodsTab). Pod-envanteri paylaşılan useServicePods hook'undan (Pods
// ile aynı veri). KPI "Pods"/"Restarts" kartları artık Pods sekmesine götürür.
export function ServiceInfraTab({ service, range, onZoom, onZoomReset }: {
  service: string;
  range: TimeRange;
  onZoom?: (fromUnixSec: number, toUnixSec: number) => void;
  // Grafana-parite M1 — çift-tık: Service.tsx zoom geri-yığınını pop eder.
  onZoomReset?: () => void;
}) {
  const [params, setParams] = useSearchParams();
  const {
    metaQ, ns, deploy, matched, rows, clustersWithPods,
    effNs, effDeploy, cFrom, cTo, clamped,
    sourcesPending, noClusters, podsPending,
  } = useServicePods(service, range);

  // ?icluster= — çip filtresi (URL kaynak-of-truth, replace:true).
  const icluster = params.get('icluster') ?? '';
  const setICluster = (c: string) => setParams(prev => {
    const next = new URLSearchParams(prev);
    if (c) next.set('icluster', c); else next.delete('icluster');
    return next;
  }, { replace: true });
  const visRows = icluster ? rows.filter(r => r.cluster === icluster) : rows;

  // Grafikler aktif çipin cluster'ını izler (çip yoksa ilk eşleşen).
  const chartCluster = icluster || clustersWithPods[0] || '';
  // v0.9.72 — CPU/Mem default pod-bazlı (asıl soru "hangi pod sıcak").
  const [cpuByPod, setCpuByPod] = useState(true);
  const [memByPod, setMemByPod] = useState(true);
  const trendOK = chartCluster !== '' && effNs !== '' && effDeploy !== '';
  const cpuTrendQ = useQuery({
    queryKey: ['deploy-trend', chartCluster, effNs, effDeploy, 'cpu', cpuByPod, cFrom, cTo],
    queryFn: () => api.clusterDeployTrend(chartCluster, effNs, effDeploy, 'cpu', cpuByPod, cFrom, cTo),
    staleTime: 60_000, retry: 1, enabled: trendOK,
  });
  const memTrendQ = useQuery({
    queryKey: ['deploy-trend', chartCluster, effNs, effDeploy, 'mem', memByPod, cFrom, cTo],
    queryFn: () => api.clusterDeployTrend(chartCluster, effNs, effDeploy, 'mem', memByPod, cFrom, cTo),
    staleTime: 60_000, retry: 1, enabled: trendOK,
  });

  // KPI kartları (OpenShift konsol deseni): CPU/Mem → kendi grafiğine kaydırır;
  // Pods/Restarts → pod listesi artık Pods sekmesinde olduğundan oraya götürür.
  const cpuChartRef = useRef<HTMLDivElement>(null);
  const memChartRef = useRef<HTMLDivElement>(null);
  const [flash, setFlash] = useState('');
  const scrollToChart = (key: 'cpu' | 'mem') => {
    const ref = key === 'cpu' ? cpuChartRef : memChartRef;
    const reduce = window.matchMedia('(prefers-reduced-motion: reduce)').matches;
    ref.current?.scrollIntoView({ behavior: reduce ? 'auto' : 'smooth', block: 'start' });
    setFlash(key);
    window.setTimeout(() => setFlash(''), 1400);
  };
  const goToPods = () => setParams(prev => {
    const next = new URLSearchParams(prev);
    next.set('tab', 'pods');
    return next;
  }, { replace: true }); // setTab/setICluster deseni — history kirletme (v0.9.159 review)
  const chartClick = (key: 'cpu' | 'mem') => ({
    role: 'button' as const, tabIndex: 0,
    onClick: () => scrollToChart(key),
    onKeyDown: (e: React.KeyboardEvent) => {
      if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); scrollToChart(key); }
    },
    style: { cursor: 'pointer' },
  });
  const podsClick = {
    role: 'button' as const, tabIndex: 0,
    onClick: goToPods,
    onKeyDown: (e: React.KeyboardEvent) => {
      if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); goToPods(); }
    },
    style: { cursor: 'pointer' },
  };
  const flashStyle = (key: string): React.CSSProperties => ({
    outline: flash === key ? '2px solid var(--accent)' : '2px solid transparent',
    outlineOffset: 4, borderRadius: 8, transition: 'outline-color .35s',
  });

  // ── Kapılar (hook'lardan SONRA) ──
  if (metaQ.isPending || sourcesPending) return <Spinner />;
  if (noClusters) {
    return <Empty icon="▦" title="No Thanos clusters configured">
      Add a remote cluster under Settings → Remote clusters to see pod-level infrastructure here.
    </Empty>;
  }
  if (rows.length === 0) {
    if (podsPending) return <Spinner />;
    return <Empty icon="▦" title="No pods matched">
      Tried {ns && deploy ? `k8s.namespace=${ns} · ${deploy}` : 'the k8s metadata mapping'}
      {' '}and pod-name matching (<span className="mono">{service}-*</span>) across{' '}
      {matched.length} Thanos cluster{matched.length > 1 ? 's' : ''} — nothing matched.
      Check that the pods follow the <span className="mono">&lt;service&gt;-&lt;hash&gt;-&lt;rand&gt;</span> naming
      or curate namespace/deployment in the service catalog.
    </Empty>;
  }

  const phaseKnown = visRows.some(r => r.phase);
  const running = visRows.filter(r => r.phase === 'Running').length;
  const cpuSum = visRows.reduce((a, r) => a + r.cpuCores, 0);
  const memSum = visRows.reduce((a, r) => a + r.memBytes, 0);
  const restartSum = visRows.reduce((a, r) => a + (r.restarts ?? 0), 0);
  const kpiVal = { fontSize: 26, fontWeight: 700 } as const;
  const kpiSub = { fontSize: 11, color: 'var(--text3)', marginTop: 4 } as const;

  return (
    <>
      {/* Drill breadcrumb — All clusters › cluster, ?icluster'dan (çip filtresi). */}
      {icluster && (
        <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap', fontSize: 12, marginBottom: 10 }}>
          <button type="button" style={{ all: 'unset', cursor: 'pointer', color: 'var(--accent2)' }}
            title="Clear cluster filter" onClick={() => setICluster('')}>
            All clusters
          </button>
          <span style={{ color: 'var(--text3)' }}>›</span>
          <span className="mono" style={{ color: 'var(--text)' }}>{icluster}</span>
        </div>
      )}
      <div style={{ fontSize: 12, color: 'var(--text3)', marginBottom: 10 }}>
        {ns && deploy ? (
          <>Pods matched to <span className="mono">{service}</span> via{' '}
          k8s.namespace=<span className="mono">{ns}</span> · <span className="mono">{deploy}</span></>
        ) : (
          <>Pods matched to <span className="mono">{service}</span> by pod name{' '}
          (<span className="mono">{service}-*</span>{effNs ? <> · ns:<span className="mono">{effNs}</span></> : null} — no k8s metadata from spans)</>
        )}{' '}
        across {clustersWithPods.length} cluster{clustersWithPods.length > 1 ? 's' : ''}
      </div>

      {/* Cluster çipleri — tıklanınca grafikler o cluster'a daralır. */}
      <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', marginBottom: 12 }}>
        {clustersWithPods.map(c => {
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

      {/* KPI satırı: Running / CPU / Memory / Restarts. */}
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(220px, 1fr))', gap: 14 }}>
        <Card density="tight" header={phaseKnown ? 'Running pods' : 'Pods'} {...podsClick}
          title="Go to the Pods tab">
          <div className="mono" style={{ ...kpiVal, color: phaseKnown ? 'var(--ok)' : undefined }}>
            {fmtNum(phaseKnown ? running : visRows.length)}
          </div>
          <div style={kpiSub}>{phaseKnown
            ? (visRows.length - running > 0 ? `${fmtNum(visRows.length - running)} not running` : 'all pods healthy')
            : 'status unknown — kube-state-metrics not visible on this cluster'}</div>
        </Card>
        <Card density="tight" header="CPU used (cores)" {...chartClick('cpu')} title="Go to the CPU chart">
          <div className="mono" style={kpiVal}>{visRows.length ? fmtCores(cpuSum) : '—'}</div>
        </Card>
        <Card density="tight" header="Memory used" {...chartClick('mem')} title="Go to the memory chart">
          <div className="mono" style={kpiVal}>{visRows.length ? fmtBytes(memSum) : '—'}</div>
        </Card>
        <Card density="tight" header="Restarts (total)" {...podsClick}
          title="Go to the Pods tab">
          <div className="mono" style={{ ...kpiVal, color: restartColor(restartSum) }}>{fmtNum(restartSum)}</div>
        </Card>
      </div>

      {/* CPU/Mem area — MetricArea (Clusters ile ortak), By pod toggle. */}
      {((cpuTrendQ.data?.series?.length ?? 0) > 0 || (memTrendQ.data?.series?.length ?? 0) > 0) && (
        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 14, marginTop: 14 }}>
          <div ref={cpuChartRef} style={flashStyle('cpu')}>
            <MetricArea title={`CPU (cores) · ${chartCluster}${clamped ? ' (last 6h)' : ''}`} byLabel="By pod"
              by={cpuByPod} onToggle={setCpuByPod} onZoom={onZoom} onZoomReset={onZoomReset}
              series={cpuTrendQ.data?.series} seriesName="CPU" />
          </div>
          <div ref={memChartRef} style={flashStyle('mem')}>
            <MetricArea title={`Memory · ${chartCluster}${clamped ? ' (last 6h)' : ''}`} byLabel="By pod"
              by={memByPod} onToggle={setMemByPod} onZoom={onZoom} onZoomReset={onZoomReset}
              series={memTrendQ.data?.series} seriesName="Memory" unit="bytes" />
          </div>
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
    </>
  );
}
