import { Suspense, useMemo } from 'react';
import { useSearchParams, Link } from 'react-router-dom';
import { useQuery, useQueries } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { useUrlRange } from '@/lib/useUrlRange';
import { timeRangeToNs, fmtBytes } from '@/lib/utils';
import { Topbar } from '@/components/Topbar';
import { Card } from '@/components/ui';
import { Spinner, Empty } from '@/components/Spinner';
import { MultiLineChart } from '@/components/MultiLineChart';
import { ChartCard, type ChartLine } from '@/pages/service/charts/ChartCard';
import { ThanosTrendPanel } from '@/pages/clusters/TrendPanel';
import { namedSeriesToSeries } from '@/pages/clusters/trendSeries';
import { PromQLList } from '@/pages/clusters/PromQLList';
import { promQuote } from '@/pages/clusters/promQuote';
import { podWorkloadName } from '@/pages/clusters/podWorkload';
import { fmtCores, podPhaseBadge } from '@/pages/clusters/thresholds';
import type { ClusterPodRow } from '@/lib/types';

// Pod detay sayfası (v0.9.151) — H.Polat önerisi: pod'a tıklayınca cramped
// drawer YERİNE tam sayfa. Üç kaynak tek yerde, hepsi POD'a scope'lu:
//   • RED — servisin kümülatif metrikleri (throughput/error/latency) bu pod'da.
//     Overview'un iki spanMetricBatch'i AYNEN, DSL'e host.name=<pod> eklenir;
//     operationMVGate host.name'i reddeder → bounded raw-spans (host_name kolonu;
//     service_summary_5m'de host_name YOK). service yoksa RED gizlenir.
//   • Infra — tek-pod CPU/Mem (ThanosTrendPanel, drawer'dan taşındı).
//   • JVM/JBoss JMX — pod'a filtreli (clusterJmxTrend pod arg, v0.9.149);
//     deploy JMX keşfini sürer (verilmezse pod adından türetilir).
// Rota: /pod?cluster=&namespace=&pod=&service=&deploy= (App.tsx flat route).

export default function PodPage() {
  return <Suspense fallback={<Spinner />}><PodDetail /></Suspense>;
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <span style={{ fontSize: 12 }}>
      <span style={{ color: 'var(--text3)' }}>{label} </span>
      <span className="mono">{value}</span>
    </span>
  );
}

function PodDetail() {
  const [sp] = useSearchParams();
  const clusterParam = sp.get('cluster') ?? '';
  const nsParam = sp.get('namespace') ?? '';
  const pod = sp.get('pod') ?? '';
  const service = sp.get('service') ?? '';
  // deploy yalnız JMX keşfi için gerekir; verilmezse pod adından türet.
  const deploy = sp.get('deploy') || (pod ? podWorkloadName(pod) : '');
  // ?from= geri-breadcrumb etiketini sürer (drill kaynağı): metrics →
  // "Metrics", infra/clusters/vars. → "Infrastructure" (v0.9.152).
  const drillFrom = sp.get('from') ?? '';
  const backTab = drillFrom === 'metrics' ? 'metrics' : 'infra';
  const backLabel = drillFrom === 'metrics' ? 'Metrics' : 'Infrastructure';
  const [range, setRange] = useUrlRange('1h');
  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);
  const xRange = useMemo(() => ({ from: from / 1e9, to: to / 1e9 }), [from, to]);

  // Sunucu 6h clamp — Infra/JMX Thanos sorgularıyla aynı dürüstlük (Clusters/
  // ServiceInfraTab emsali). RED spans tarafında clamp YOK (raw-spans zaten
  // bounded + auto-sample); yalnız Thanos eksenlerine uygulanır.
  const { cFrom, cTo, clamped } = useMemo(() => {
    const sixH = 6 * 3600 * 1e9;
    if (to - from > sixH) return { cFrom: to - sixH, cTo: to, clamped: true };
    return { cFrom: from, cTo: to, clamped: false };
  }, [from, to]);

  // cluster/namespace çözümü (v0.9.153): Infra/Clusters drill'i cluster'ı
  // taşır (tek fetch); Metrics drill'i YALNIZ service+pod taşır → pod'un
  // hangi cluster'da olduğunu tüm Thanos kaynaklarında arayarak çöz. row da
  // (phase/cpu/mem başlığı) buradan gelir. cluster çözülene dek RED zaten
  // çalışır (service+pod), yalnız infra/JMX bekler — kademeli.
  const sourcesQ = useQuery({
    queryKey: ['cluster-sources'],
    queryFn: () => api.clusterSources(),
    staleTime: 300_000, enabled: !clusterParam && !!pod,
  });
  const searchClusters = useMemo(
    () => (clusterParam ? [clusterParam] : (sourcesQ.data?.clusters ?? [])),
    [clusterParam, sourcesQ.data],
  );
  const podsQs = useQueries({
    queries: searchClusters.map(c => ({
      queryKey: ['cluster-pods', c],
      queryFn: () => api.clusterPods(c),
      staleTime: 60_000, retry: 1,
    })),
  });
  const { cluster, namespace, row } = useMemo<{ cluster: string; namespace: string; row: ClusterPodRow | undefined }>(() => {
    for (let i = 0; i < searchClusters.length; i++) {
      const found = (podsQs[i]?.data?.pods ?? []).find(
        p => p.pod === pod && (!nsParam || p.namespace === nsParam));
      if (found) return { cluster: searchClusters[i], namespace: found.namespace, row: found };
    }
    return { cluster: clusterParam, namespace: nsParam, row: undefined };
  }, [searchClusters, podsQs, pod, nsParam, clusterParam]);

  // Per-pod RED — Overview.tsx'in iki batch'ini birebir aynala + host.name.
  const podScope = `service.name = "${service.replace(/"/g, '\\"')}" AND host.name = "${pod.replace(/"/g, '\\"')}"`;
  const redEnabled = !!service && !!pod;
  const redQ = useQuery({
    queryKey: ['pod-red', service, pod, from, to],
    queryFn: () => api.spanMetricBatch({ from, to, dsl: podScope, aggs: [
      { name: 'rate', agg: 'rate' },
      { name: 'error_rate', agg: 'error_rate' },
    ] }),
    enabled: redEnabled, staleTime: 30_000,
  });
  // Latency kafka messaging span'lerini HARİÇ tutar (Overview v0.9.129 emsali).
  const latQ = useQuery({
    queryKey: ['pod-latency-nokafka', service, pod, from, to],
    queryFn: () => api.spanMetricBatch({ from, to, dsl: `${podScope} AND messaging.system != "kafka"`, aggs: [
      { name: 'p99', agg: 'p99', field: 'duration_ms' },
      { name: 'p95', agg: 'p95', field: 'duration_ms' },
      { name: 'p50', agg: 'p50', field: 'duration_ms' },
    ] }),
    enabled: redEnabled, staleTime: 30_000,
  });
  const s = redQ.data;
  const lat = latQ.data;
  const redStatus: 'loading' | 'error' | 'ready' = redQ.isLoading ? 'loading' : redQ.isError ? 'error' : 'ready';
  const latStatus: 'loading' | 'error' | 'ready' = latQ.isLoading ? 'loading' : latQ.isError ? 'error' : 'ready';

  // Throughput OK/Errors band türetimi — Overview ile birebir (ek sorgu yok).
  const throughputBands = useMemo<ChartLine[]>(() => {
    const ratePts = s?.rate?.[0]?.points ?? [];
    const erPts = s?.error_rate?.[0]?.points ?? [];
    if (ratePts.length < 2) return [{ series: s?.rate ?? [], color: 'var(--accent)', label: 'req/s' }];
    const okPts = ratePts.map((p, i) => ({ time: p.time, value: Math.max(0, p.value * (1 - (erPts[i]?.value ?? 0) / 100)) }));
    const errPts = ratePts.map((p, i) => ({ time: p.time, value: Math.max(0, p.value * ((erPts[i]?.value ?? 0) / 100)) }));
    return [
      { series: [{ groupKey: [], points: okPts }], color: 'var(--ok)', label: 'OK' },
      { series: [{ groupKey: [], points: errPts }], color: 'var(--err)', label: 'Errors' },
    ];
  }, [s]);

  // JVM/JBoss JMX — pod'a filtreli (byPod=false: tek pod, jboss datasource'a
  // gruplanır, jvm toplam). deploy JMX keşfini sürer.
  const jmxMetricsQ = useQuery({
    queryKey: ['jmx-metrics', cluster, namespace, deploy],
    queryFn: () => api.clusterJmxMetrics(cluster, namespace, deploy),
    staleTime: 60_000, retry: 1, enabled: !!cluster && !!namespace && !!deploy,
  });
  const jmxMetrics = useMemo(() => jmxMetricsQ.data?.metrics ?? [], [jmxMetricsQ.data]);
  const jmxPanelQs = useQueries({
    queries: jmxMetrics.map(m => ({
      queryKey: ['jmx-trend-pod', cluster, namespace, deploy, m, pod, cFrom, cTo],
      queryFn: () => api.clusterJmxTrend(cluster, namespace, deploy, m, false, cFrom, cTo, pod),
      staleTime: 60_000, retry: 1,
    })),
  });

  if (!pod) {
    return (
      <>
        <Topbar title="Pod" />
        <div id="content"><Empty icon="—" title="Pod belirtilmedi (pod parametresi gerekli)." /></div>
      </>
    );
  }

  return (
    <>
      <Topbar title={`Pod · ${pod}`} range={range} onRangeChange={setRange} />
      <div id="content">
        {/* Geri + kimlik + KPI başlık satırı */}
        <div style={{ display: 'flex', gap: 12, alignItems: 'center', marginBottom: 14, flexWrap: 'wrap' }}>
          <Link to={service ? `/service?name=${encodeURIComponent(service)}&tab=${backTab}` : '/clusters'} className="sec" style={{
            padding: '5px 12px', border: '1px solid var(--border)', borderRadius: 6,
            fontSize: 12, color: 'var(--text)', textDecoration: 'none',
          }}>← {service ? `${service} · ${backLabel}` : 'Clusters'}</Link>
          <Stat label="Cluster" value={cluster} />
          <Stat label="Namespace" value={namespace || '—'} />
          {row?.phase && <span className={`badge ${podPhaseBadge(row.phase)}`}>{row.phase}</span>}
          {row && <Stat label="CPU" value={fmtCores(row.cpuCores)} />}
          {row && <Stat label="Mem" value={fmtBytes(row.memBytes)} />}
          {row && <Stat label="Restarts" value={String(row.restarts ?? 0)} />}
          {clamped && <span style={{ fontSize: 11, color: 'var(--text3)' }}>Infra/JVM: son 6h</span>}
        </div>

        {/* RED — servisin kümülatif metrikleri, bu pod'a scope'lu */}
        {service ? (
          <>
            <h3 style={{ fontSize: 13, margin: '4px 0 8px' }}>
              Service metrics · this pod
              <span style={{ fontWeight: 400, color: 'var(--text3)' }}> · {service}</span>
            </h3>
            <div className="ov-grid ov-charts-3 ov-mb">
              <ChartCard title="Response time" unit=" ms" mode="line" status={latStatus} onZoom={undefined} xRange={xRange} lines={[
                { series: lat?.p50 ?? [], color: 'var(--purple)', label: 'P50' },
                { series: lat?.p95 ?? [], color: 'var(--orange)', label: 'P95' },
                { series: lat?.p99 ?? [], color: 'var(--err)', label: 'P99' },
              ]} />
              <ChartCard title="Throughput" unit=" req/s" mode="stacked" status={redStatus} xRange={xRange} lines={throughputBands} />
              <ChartCard title="Failure rate" unit="%" mode="area" status={redStatus} xRange={xRange} lines={[
                { series: s?.error_rate ?? [], color: 'var(--err)', label: 'errors' },
              ]} />
            </div>
          </>
        ) : (
          <Empty icon="—" title="Bu pod bir Coremetry servisine eşlenmedi — RED metrikleri yok (infra + JVM aşağıda)." />
        )}

        {/* Infra — tek-pod CPU/Mem trend (drawer'dan taşındı). cluster
            çözülene dek (Metrics drill'i cluster taşımaz → Thanos'ta aranır)
            gizli; bulunamazsa hiç gösterilmez (görünmez-düşer). */}
        {cluster && (
          <>
            <h3 style={{ fontSize: 13, margin: '18px 0 8px' }}>
              Infrastructure · <span className="mono">{cluster}</span>
            </h3>
            <ThanosTrendPanel cluster={cluster} namespace={namespace} pod={pod} row={row} fromNs={cFrom} toNs={cTo} />
          </>
        )}

        {/* JVM / JBoss JMX — pod'a filtreli */}
        {jmxMetrics.length > 0 && (
          <>
            <h3 style={{ fontSize: 13, margin: '18px 0 8px' }}>
              JVM / JBoss (JMX) · <span className="mono">{pod}</span>
              <span style={{ fontWeight: 400, color: 'var(--text3)' }}> · {jmxMetrics.length} metrics</span>
            </h3>
            <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 14 }}>
              {jmxMetrics.map((m, i) => {
                const data = jmxPanelQs[i]?.data?.series;
                if (!data || data.length === 0) return null;
                const unit = m.includes('bytes') ? 'bytes' : m.includes('seconds') ? 's' : undefined;
                const isJboss = m.startsWith('jboss_');
                return (
                  <Card key={m} header={m}>
                    <MultiLineChart series={namedSeriesToSeries(data, m)} height={180}
                      unit={unit} maxSeries={isJboss ? 40 : undefined} />
                  </Card>
                );
              })}
            </div>
          </>
        )}

        {/* PromQL — pod scope */}
        <div style={{ marginTop: 18, maxWidth: 720 }}>
          <Card header="Prometheus queries (pod scope)">
            <PromQLList queries={[
              ['CPU (cores)', `rate(container_cpu_usage_seconds_total{namespace="${promQuote(namespace)}",pod="${promQuote(pod)}"}[5m])`],
              ['Working-set memory', `container_memory_working_set_bytes{namespace="${promQuote(namespace)}",pod="${promQuote(pod)}"}`],
            ]} />
          </Card>
        </div>
      </div>
    </>
  );
}
