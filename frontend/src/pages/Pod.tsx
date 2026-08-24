import { Suspense, lazy, useCallback, useEffect, useMemo, useRef } from 'react';
import { useSearchParams, Link } from 'react-router-dom';
import { useQuery, useQueries } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { panelMaxDataPoints } from '@/lib/chartStep';
import { usePageZoomRange } from '@/lib/chart/usePageZoomRange';
import { defaultLatencyHidden } from '@/lib/chart/legendVisibility';
import { timeRangeToNs, fmtBytes } from '@/lib/utils';
import { clampThanosWindow, THANOS_MAX_WINDOW_LABEL } from '@/lib/thanosWindow';
import { Topbar } from '@/components/Topbar';
import { Card } from '@/components/ui';
import { Spinner, Empty } from '@/components/Spinner';
import { MultiLineChart } from '@/components/MultiLineChart';
// v0.9.945 (D2/K10) — RED kartları ChartCard'dan (saniye eksenli
// OverviewChart motoru) CorePanelMulti'ye taşındı; gerekçe grid'in
// başında. Lazy: sayfa @grafana/data'ya STATİK bağlanmamalı
// (corePanelEntry.tsx yorumunun ölçümü — 35 KB → 1 MB).
import type { CorePanelMultiItem } from '@/components/chart/corePanelEntry';
import { ThanosTrendPanel } from '@/pages/clusters/TrendPanel';
import { namedSeriesToSeries } from '@/pages/clusters/trendSeries';
import { PromQLList } from '@/pages/clusters/PromQLList';
import { promQuote } from '@/pages/clusters/promQuote';
import { podWorkloadName } from '@/pages/clusters/podWorkload';
import { fmtCores, podPhaseBadge } from '@/pages/clusters/thresholds';
import { resolvePodCluster } from '@/pages/service/podResolve';
import { serviceHref } from '@/lib/serviceHref';
import { PageShell } from '@/components/ui/PageShell';

const CorePanelMultiLazy = lazy(() =>
  import('@/components/chart/corePanelEntry').then(m => ({ default: m.CorePanelMulti })));

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
  // ?from= geri-breadcrumb etiketini sürer (drill kaynağı, v0.9.159):
  // pods/metrics → "Pods" sekmesi (v0.9.158 rename), infra → "Infrastructure".
  // clusters → /clusters sayfası (aşağıda service boşsa zaten oraya gider).
  const drillFrom = sp.get('from') ?? '';
  const toPods = drillFrom === 'pods' || drillFrom === 'metrics';
  const backTab = toPods ? 'pods' : 'infra';
  const backLabel = toPods ? 'Pods' : 'Infrastructure';
  // v0.9.429 — zoom-yığını paylaşılan usePageZoomRange hook'unda.
  const { range, setRange, handleZoom, handleZoomReset } = usePageZoomRange('1h');
  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);
  const xRange = useMemo(() => ({ from: from / 1e9, to: to / 1e9 }), [from, to]);
  // v0.9.945 (D2/K10) — SAYFANIN TEK crosshair grubu.
  //
  // `-ms` bir MOTOR AD ALANI, süs değil (MultiLineChart.tsx:164-172):
  // uPlot.sync imleci karşı grafiğin ÖLÇEĞİNE VALUE olarak taşır, yani
  // ms-eksenli ve saniye-eksenli iki motor aynı anahtarı paylaşırsa
  // crosshair 1000× yanlış yere düşer. MLC kendi syncKey'ine bu eki
  // KENDİ ekliyor; CorePanel'i DOĞRUDAN çağıran (RED üçlüsü) ekin
  // kendisini yazmak zorunda — DetailsMetricsSection'ın v0.9.789'da
  // kurduğu desen.
  const podChartSync = `podjmx:${pod}-ms`;

  // Sunucu pencere tavanı — Infra/JMX Thanos sorgularıyla aynı dürüstlük
  // (Clusters/ServiceInfraTab emsali). RED spans tarafında tavan YOK
  // (raw-spans zaten bounded + auto-sample); yalnız Thanos eksenlerine.
  const { cFrom, cTo, clamped } = useMemo(
    () => clampThanosWindow(from, to), [from, to]);

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
  const { cluster, namespace, row } = useMemo(
    () => resolvePodCluster(searchClusters, podsQs.map(q => q.data?.pods), pod, nsParam, clusterParam),
    [searchClusters, podsQs, pod, nsParam, clusterParam],
  );
  // v0.9.959 (UX denetimi G8/Ö22) — pod ARAMASI kesik listede yapıldıysa
  // "bulunamadı" bir KANIT değil: sunucu topk(500) ile en işlek pod'ları
  // döndürür ve sakin bir pod tam olarak o kuyruğun dışında kalır.
  // Bu sayfa `truncated`ı hiç okumuyordu ve sessizce "eşlenmedi" diyordu.
  const searchTruncated = podsQs.some(q => q.data?.truncated);

  // Per-pod RED — Overview.tsx'in iki batch'ini birebir aynala + host.name.
  const podScope = `service.name = "${service.replace(/"/g, '\\"')}" AND host.name = "${pod.replace(/"/g, '\\"')}"`;
  const redEnabled = !!service && !!pod;
  // v0.9.391 (Faz B) — mdp + zarf select'i (Overview deseniyle aynı).
  const podMdp = panelMaxDataPoints(3);
  const redQ = useQuery({
    queryKey: ['pod-red', service, pod, from, to, podMdp],
    queryFn: () => api.spanMetricBatch({ from, to, maxDataPoints: podMdp, dsl: podScope, aggs: [
      { name: 'rate', agg: 'rate' },
      { name: 'error_rate', agg: 'error_rate' },
    ] }),
    select: d => d.series,
    enabled: redEnabled, staleTime: 30_000,
  });
  // Latency kafka messaging span'lerini HARİÇ tutar (Overview v0.9.129 emsali).
  const latQ = useQuery({
    queryKey: ['pod-latency-nokafka', service, pod, from, to, podMdp],
    queryFn: () => api.spanMetricBatch({ from, to, maxDataPoints: podMdp, dsl: `${podScope} AND messaging.system != "kafka"`, aggs: [
      { name: 'p99', agg: 'p99', field: 'duration_ms' },
      { name: 'p95', agg: 'p95', field: 'duration_ms' },
      { name: 'p50', agg: 'p50', field: 'duration_ms' },
      // Madde 4 sweep — avg serisi (Overview latency batch'iyle ayna kalır).
      { name: 'avg', agg: 'avg', field: 'duration_ms' },
    ] }),
    select: d => d.series,
    enabled: redEnabled, staleTime: 30_000,
  });
  const s = redQ.data;
  const lat = latQ.data;
  const redStatus: 'loading' | 'error' | 'ready' = redQ.isLoading ? 'loading' : redQ.isError ? 'error' : 'ready';
  const latStatus: 'loading' | 'error' | 'ready' = latQ.isLoading ? 'loading' : latQ.isError ? 'error' : 'ready';

  // Throughput OK/Errors band türetimi — Overview ile birebir (ek sorgu yok).
  // v0.9.945 — CorePanelMultiItem şekli: renk ARTIK ROLDEN geliyor
  // (success/error), elle var(--ok)/var(--err) verilmiyor. Overview'un
  // "Failure rate · trace" paneliyle aynı sözleşme, yani iki sayfa aynı
  // bandı aynı renkte çiziyor.
  const throughputBands = useMemo<CorePanelMultiItem[]>(() => {
    const ratePts = s?.rate?.[0]?.points ?? [];
    const erPts = s?.error_rate?.[0]?.points ?? [];
    if (ratePts.length < 2) return [{ series: s?.rate ?? [], name: 'req/s', role: 'data' }];
    const okPts = ratePts.map((p, i) => ({ time: p.time, value: Math.max(0, p.value * (1 - (erPts[i]?.value ?? 0) / 100)) }));
    const errPts = ratePts.map((p, i) => ({ time: p.time, value: Math.max(0, p.value * ((erPts[i]?.value ?? 0) / 100)) }));
    return [
      { series: [{ groupKey: [], points: okPts }], name: 'OK', role: 'success' },
      { series: [{ groupKey: [], points: errPts }], name: 'Errors', role: 'error' },
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
        <PageShell><Empty icon="—" title="Pod belirtilmedi (pod parametresi gerekli)." /></PageShell>
      </>
    );
  }

  return (
    <>
      <Topbar title={`Pod · ${pod}`} range={range} onRangeChange={setRange} />
      <PageShell>
        {/* Geri + kimlik + KPI başlık satırı */}
        <div style={{ display: 'flex', gap: 12, alignItems: 'center', marginBottom: 14, flexWrap: 'wrap' }}>
          {/* v0.9.965 — GERİ linki penceresini taşımıyordu: pod'a özel
              bir pencereden gelen operatör "← servis" dediğinde başka bir
              zaman aralığına dönüyordu. Bir geri linkinin bağlamı
              değiştirmesi, geri linki olmaktan çıkması demek. */}
          <Link to={service ? serviceHref(service, { range, tab: backTab }) : '/clusters'} className="sec" style={{
            padding: '5px 12px', border: '1px solid var(--border)', borderRadius: 6,
            fontSize: 12, color: 'var(--text)', textDecoration: 'none',
          }}>← {service ? `${service} · ${backLabel}` : 'Clusters'}</Link>
          <Stat label="Cluster" value={cluster} />
          <Stat label="Namespace" value={namespace || '—'} />
          {row?.phase && <span className={`badge ${podPhaseBadge(row.phase)}`}>{row.phase}</span>}
          {row && <Stat label="CPU" value={fmtCores(row.cpuCores)} />}
          {row && <Stat label="Mem" value={fmtBytes(row.memBytes)} />}
          {row && <Stat label="Restarts" value={row.restartsUnknown ? '—' : String(row.restarts ?? 0)} />}
          {clamped && <span style={{ fontSize: 11, color: 'var(--text3)' }}>Infra/JVM: son {THANOS_MAX_WINDOW_LABEL}</span>}
        </div>

        {/* RED — servisin kümülatif metrikleri, bu pod'a scope'lu */}
        {service ? (
          <>
            <h3 style={{ fontSize: 13, margin: '4px 0 8px' }}>
              Service metrics · this pod
              <span style={{ fontWeight: 400, color: 'var(--text3)' }}> · {service}</span>
            </h3>
            {/* v0.9.945 (UX denetimi D2 / K10) — RED üçlüsü ChartCard'dan
                CorePanelMulti'ye TAŞINDI.

                v0.9.388 bu üçlüyü sayfanın `podjmx` crosshair grubuna
                kattığını yazıyordu ve o vaat SESSİZCE kırılmıştı:
                MultiLineChart syncKey'e `-ms` ekliyor (MultiLineChart.tsx
                yorumunun gerekçesi: uPlot.sync değeri karşı grafiğin
                ÖLÇEĞİNE taşır, ms-eksenli ve saniye-eksenli motorlar aynı
                grubu paylaşırsa crosshair 1000× yanlış yere düşer). RED
                kartları ChartCard → OverviewChart (saniye ekseni) idi, JMX
                kartları MLC → CorePanel (ms ekseni): iki AYRI grup, hiç
                buluşmayan iki crosshair.

                KESTİRME (`-ms` ekini kaldırmak) BİLİNÇLİ OLARAK
                REDDEDİLDİ — o, bugün doğru olan ölçek yalıtımını kırardı.
                Tek doğru yol motoru birleştirmekti: artık sayfadaki HER
                panel ms eksenli CorePanel, dolayısıyla hepsi `-ms` ad
                alanında ve crosshair GERÇEKTEN paylaşılıyor.

                Kayıp yok: başlık/yükleme/hata/boş durumları CorePanel'in
                kendi kabuğunda (üstelik hata ve boşluk AYRI kanallarda —
                ChartCard ikisini de tek kutuda gösteriyordu), lejant
                kalıcılığı storageKey'de, gizli-varsayılan latency serileri
                defaultHidden'da. */}
            <div className="ov-grid ov-charts-3 ov-mb">
              <Suspense fallback={<Spinner />}>
                <CorePanelMultiLazy
                  title="Response time"
                  storageKey="pod-response-time" height={200}
                  unit="ms" viz="line" xRange={xRange}
                  syncKey={podChartSync}
                  loading={latStatus === 'loading'}
                  error={latStatus === 'error' ? 'Metrikler yüklenemedi' : undefined}
                  defaultHidden={[...defaultLatencyHidden(['avg', 'P50', 'P95', 'P99'])]}
                  items={[
                    { name: 'avg', role: 'data', series: lat?.avg ?? [] },
                    { name: 'P50', role: 'data', series: lat?.p50 ?? [] },
                    { name: 'P95', role: 'data', series: lat?.p95 ?? [] },
                    { name: 'P99', role: 'data', series: lat?.p99 ?? [] },
                  ]}
                />
              </Suspense>
              <Suspense fallback={<Spinner />}>
                <CorePanelMultiLazy
                  title="Throughput"
                  storageKey="pod-throughput" height={200}
                  unit="reqps" viz="stacked" xRange={xRange}
                  syncKey={podChartSync}
                  loading={redStatus === 'loading'}
                  error={redStatus === 'error' ? 'Metrikler yüklenemedi' : undefined}
                  items={throughputBands}
                />
              </Suspense>
              <Suspense fallback={<Spinner />}>
                <CorePanelMultiLazy
                  title="Failure rate"
                  storageKey="pod-failure-rate" height={200}
                  unit="percent" viz="area" xRange={xRange}
                  syncKey={podChartSync}
                  loading={redStatus === 'loading'}
                  error={redStatus === 'error' ? 'Metrikler yüklenemedi' : undefined}
                  items={[{ name: 'errors', role: 'error', series: s?.error_rate ?? [] }]}
                />
              </Suspense>
            </div>
          </>
        ) : (
          <Empty icon="—" title="Bu pod bir Coremetry servisine eşlenmedi — RED metrikleri yok (infra + JVM aşağıda).">
            {/* v0.9.959 (G8/Ö22) — "eşlenmedi" ile "kesik listede
                bulunamadı" aynı ekran olamaz. İkincisi bir eksiklik
                beyanıdır, bir sonuç değil. */}
            {searchTruncated && !row && (
              <div style={{ marginTop: 6 }}>
                ⚠ Pod listesi sunucuda kesildi (topk 500) — bu pod kesilen
                kuyrukta kalmış olabilir; &laquo;eşlenmedi&raquo; kesin değil.
              </div>
            )}
          </Empty>
        )}

        {/* Infra — tek-pod CPU/Mem trend (drawer'dan taşındı). cluster
            çözülene dek (Metrics drill'i cluster taşımaz → Thanos'ta aranır)
            gizli; bulunamazsa hiç gösterilmez (görünmez-düşer). */}
        {cluster && (
          <>
            <h3 style={{ fontSize: 13, margin: '18px 0 8px' }}>
              Infrastructure · <span className="mono">{cluster}</span>
            </h3>
            {/* Madde 4 sweep — trend drag-zoom sayfa range'ine yazar,
                çift-tık geri-yığını pop eder (audit §2.4 kararı güncellendi). */}
            <ThanosTrendPanel cluster={cluster} namespace={namespace} pod={pod} row={row} fromNs={cFrom} toNs={cTo}
              onZoom={handleZoom} onZoomReset={handleZoomReset} />
          </>
        )}

        {/* JVM / JBoss JMX — pod'a filtreli */}
        {jmxMetrics.length > 0 && (
          <>
            <h3 style={{ fontSize: 13, margin: '18px 0 8px' }}>
              JVM / JBoss (JMX) · <span className="mono">{pod}</span>
              <span style={{ fontWeight: 400, color: 'var(--text3)' }}> · {jmxMetrics.length} metrics</span>
            </h3>
            <div className="grid-2" style={{ display: 'grid', gap: 14 }}>
              {jmxMetrics.map((m, i) => {
                const data = jmxPanelQs[i]?.data?.series;
                if (!data || data.length === 0) return null;
                const unit = m.includes('bytes') ? 'bytes' : m.includes('seconds') ? 's' : undefined;
                const isJboss = m.startsWith('jboss_');
                return (
                  <Card key={m} header={m}>
                    {/* Madde 4 sweep — pod'un JMX panelleri ortak crosshair
                        grubu (PodJmxInline ile aynı anahtar) + drag-zoom
                        sayfa range'ine, çift-tık geri-yığınına. */}
                    <MultiLineChart series={namedSeriesToSeries(data, m)} height={180}
                      unit={unit} maxSeries={isJboss ? 40 : undefined}
                      // syncKey EK'SİZ: MLC `-ms`i kendi ekliyor, yani bu
                      // panel de podChartSync grubuna düşer (v0.9.945).
                      syncKey={`podjmx:${pod}`} onZoom={handleZoom} onZoomReset={handleZoomReset} />
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
      </PageShell>
    </>
  );
}
