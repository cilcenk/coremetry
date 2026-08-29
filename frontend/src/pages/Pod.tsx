import { Suspense, lazy, useMemo, useState } from 'react';
import { useSearchParams, Link } from 'react-router-dom';
import { useQuery, useQueries } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { panelMaxDataPoints } from '@/lib/chartStep';
import { usePageZoomRange } from '@/lib/chart/usePageZoomRange';
import { defaultLatencyHidden } from '@/lib/chart/legendVisibility';
import { timeRangeToNs } from '@/lib/utils';
import { clampThanosWindow, THANOS_MAX_WINDOW_LABEL } from '@/lib/thanosWindow';
import { Topbar } from '@/components/Topbar';
import { Card, DisclosureButton, Badge, LinkButton } from '@/components/ui';
import { readBandsParam, writeBandsParam } from '@/lib/bandsParam';
import { PanelTitle } from '@/components/ui/PanelTitle';
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
import { resolvePodCluster } from '@/pages/service/podResolve';
import { podDetailPath } from '@/pages/service/podDetailPath';
import { serviceHref } from '@/lib/serviceHref';
import { logsHref } from '@/lib/logsUrl';
import { encodeFiltersParam } from '@/lib/logFilters';
import { tracesPivotHref } from '@/lib/pivotHref';
import { PageShell } from '@/components/ui/PageShell';
import { useEntityEnabled, useEntity, useEntityServices, useEntityContainers, useAnomalyEvents, useAnomalySilences } from '@/lib/queries';
import { windowAnomalies, anomalyRegions, silencedSet } from '@/lib/anomalyRegions';
import { entityHref, entityLiveness } from '@/lib/entityHref';
import { PodIdentityLine, type PodPivot } from '@/pages/pod/PodIdentityLine';
import { PodKpiStrip, type RedState } from '@/pages/pod/PodKpiStrip';
import { PodServicesTable } from '@/pages/pod/PodServicesTable';
import { PodTracesTable } from '@/pages/pod/PodTracesTable';
import { PodContainersTable, PodSiblingsTable, PodLabelsTable, PodLifetimesTable } from '@/pages/pod/PodContextTables';
import { windowTotals, windowP95, joinSiblings } from '@/pages/pod/podPage';

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
//
// v0.10.160 — A anatomisi (mockup scratchpad/pod-detail/option-A.html, iki
// yargıçta birinci; brief §4'teki var-olmayanlar ÇİZİLMEDİ): tek kimlik
// satırı (F1 tekrarı bitti: v0.9.151 Cluster/Namespace/faz şeridi ve v0.10.135
// PodEntityPanel zinciri birleşti, yerel `Stat` kopyası silindi) → KPI şeridi
// (pencere toplamları RED serilerinden, ayrı uç yok) → RED üçlüsü → Taşıdığı
// servisler tablosu → Trace listesi (varsayılan «Hatalı», ?spans=) →
// Konteynerler → Altyapı → Kardeşler | Etiketler + Yaşam döngüsü → Ek (JMX,
// PromQL: kapalı disclosure, açılınca fetch). Aynı anatomi node/namespace
// sayfasıyla (EntityDetail) — operatöre yeni şey öğretmiyor. Zor durumlar:
// sonlanmış pod (gone: KSM/konteyner yok, ömür kapalı, RED penceresi geçmiş),
// aynı ad birden çok cluster'da (cluster çipleri, resolvePodCluster ilkini
// seçer ama bunu SÖYLER), metrik gelmeyen cluster (Thanos satırı yok → '—').

export default function PodPage() {
  return <Suspense fallback={<Spinner />}><PodDetail /></Suspense>;
}

function PodDetail() {
  const [sp, setSp] = useSearchParams();
  const clusterParam = sp.get('cluster') ?? '';
  const nsParam = sp.get('namespace') ?? '';
  const pod = sp.get('pod') ?? '';
  const service = sp.get('service') ?? '';
  const rangeParam = sp.get('range') ?? '';
  // deploy yalnız JMX keşfi için gerekir; verilmezse pod adından türet.
  const deploy = sp.get('deploy') || (pod ? podWorkloadName(pod) : '');
  // ?from= geri-breadcrumb etiketini sürer (drill kaynağı, v0.9.159):
  // pods/metrics → "Pods" sekmesi (v0.9.158 rename), infra → "Infrastructure".
  // clusters → /clusters sayfası (aşağıda service boşsa zaten oraya gider).
  const drillFrom = sp.get('from') ?? '';
  // v0.10.135 — tarihsel bağlam (ms): entity paneli o an geçerli pod kaydını çözer.
  const atParam = Number(sp.get('at') ?? 0) || 0;
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
  const searchTruncated = podsQs.some(q => q.data?.truncated);
  const podsPending = podsQs.some(q => q.isPending) || (!clusterParam && sourcesQ.isPending);
  // v0.10.160 (zor durum) — aynı pod adı birden çok cluster'da (StatefulSet
  // kafka-0 gibi): resolvePodCluster ilk eşleşmeyi seçer; bunu SÖYLE ve
  // ötekilere çip ver. Yalnız cluster parametresiz aramada anlamlı.
  const podClusterMatches = useMemo(() => (
    clusterParam ? [] : searchClusters.filter((c, i) =>
      (podsQs[i]?.data?.pods ?? []).some(p => p.pod === pod && (!nsParam || p.namespace === nsParam)))
  ), [clusterParam, searchClusters, podsQs, pod, nsParam]);
  // Kardeş tablosu için bu cluster'ın topk 500 listesi (aynı fetch, ek sorgu yok).
  const clusterIdx = searchClusters.indexOf(cluster);
  const clusterPodsList = clusterIdx >= 0 ? podsQs[clusterIdx]?.data : undefined;

  // Per-pod RED — Overview.tsx'in iki batch'ini birebir aynala + host.name.
  // v0.10.135 — servis parametresi yokken (entity linkinden gelindi) ve entity
  // katmanı açıkken RED üçlüsü pod'un TERFİ kolonuyla scope'lanır
  // (k8s.pod.name + k8s.namespace.name → k8s_pod/k8s_namespace, set index):
  // bu pod'dan geçen TÜM servislerin span'leri. Bayrak kapalı = eski davranış.
  // İnceleme (v0.10.135): pod adı cluster-benzersiz DEĞİL (StatefulSet
  // kafka-0 iki cluster'da aynı ad) → `cluster` DSL anahtarı ŞART; span
  // tarafı değeri Remote Cluster kaydının spanClusterValue'su. Değer
  // bilinmiyorsa (eşlenmemiş cluster) entity dalı KAPALI kalır — yanlış
  // kapsamdansa eski "eşlenmedi" ekranı. `resource.` öneki: terfi haritası
  // boot probe'unda dolmadıysa bile res_values'a düşer (doğru ama yavaş);
  // çıplak yazım o durumda attr_values'a düşüp SIFIR satır verirdi.
  const { enabled: entityOn, clusters: entityClusters } = useEntityEnabled();
  const clusterRef = cluster || clusterParam;
  const entityCluster = entityClusters.find(c => c.id === clusterRef || c.name === clusterRef);
  // v0.10.139 — bir kayıt birden çok span değeri taşır → DSL `in [..]` listesi
  // (dsl.go: operatör KÜÇÜK harf, köşeli parantez, tırnaklı değerler — TestDSLClusterInList pinler).
  const spanClusters = (entityCluster?.spanClusterValues?.length ? entityCluster.spanClusterValues : [entityCluster?.spanClusterValue ?? '']).filter(Boolean);
  const spanCluster = spanClusters.join(', ');
  // Trace listesi/linkleri için TEK cluster değeri: kayıt birden çok değer
  // taşıyorsa süzgeç UYGULANMAZ ve bunu söyleriz (inceleme #8) — grafikler
  // `in [..]` ile hepsini ölçerken liste alt küme olmasın.
  const traceCluster = spanClusters.length === 1 ? spanClusters[0] : '';
  const multiSpanCluster = spanClusters.length > 1;
  const esc = (v: string) => v.replace(/"/g, '\\"');
  const podScope = service
    ? `service.name = "${esc(service)}" AND host.name = "${esc(pod)}"`
    : `resource.k8s.pod.name = "${esc(pod)}"${namespace ? ` AND resource.k8s.namespace.name = "${esc(namespace)}"` : ''} AND cluster in [${spanClusters.map(v => `"${esc(v)}"`).join(', ')}]`;
  const redEnabled = !!pod && (!!service || (entityOn && spanClusters.length > 0));
  // v0.9.391 (Faz B) — mdp + zarf select'i (Overview deseniyle aynı).
  const podMdp = panelMaxDataPoints(3);
  const redQ = useQuery({
    queryKey: ['pod-red', podScope, pod, from, to, podMdp],
    queryFn: () => api.spanMetricBatch({ from, to, maxDataPoints: podMdp, dsl: podScope, aggs: [
      { name: 'rate', agg: 'rate' },
      { name: 'error_rate', agg: 'error_rate' },
    ] }),
    // Zarfın stepSeconds'ı KPI toplamına gider (Σ rate·step kesin; inceleme #4).
    select: d => ({ series: d.series, stepSeconds: d.stepSeconds }),
    enabled: redEnabled, staleTime: 30_000,
  });
  // Latency kafka messaging span'lerini HARİÇ tutar (Overview v0.9.129 emsali).
  const latQ = useQuery({
    queryKey: ['pod-latency-nokafka', podScope, pod, from, to, podMdp],
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
  const s = redQ.data?.series;
  const stepSec = redQ.data?.stepSeconds;
  const lat = latQ.data;
  const redStatus: 'loading' | 'error' | 'ready' = redQ.isLoading ? 'loading' : redQ.isError ? 'error' : 'ready';
  const latStatus: 'loading' | 'error' | 'ready' = latQ.isLoading ? 'loading' : latQ.isError ? 'error' : 'ready';
  const redState: RedState = !redEnabled ? 'off' : (redStatus === 'loading' || latStatus === 'loading') ? 'loading' : (redStatus === 'error' || latStatus === 'error') ? 'error' : 'ready';

  // v0.10.162 — servis anomalileri bant olarak RED panellerinde (servis
  // kapsamlı pod'da; anomali olayında pod boyutu yok → servis düzeyi,
  // paneldeki altyazı söyler) — v0.10.170'ten beri yalnız ?bands=1 iken.
  // Sorgu global anahtar (60 s), sayfa başına ek yük yok; kapalıyken de
  // çekilir — «aç» bağlantısı anomali varken gösterilsin diye.
  // v0.10.170 — bantlar varsayılan KAPALI; ?bands=1 açar (Overview ile aynı anahtar).
  const anomaliesQ = useAnomalyEvents(!!service);
  const silencesQ = useAnomalySilences(!!service);
  const bandsOn = readBandsParam(sp);
  const toggleBands = () => setSp(prev => writeBandsParam(prev, !bandsOn, window.location.search), { replace: true });
  const podWindowEvents = useMemo(() => (service ? windowAnomalies(anomaliesQ.data?.items, service, from, to) : []), [service, anomaliesQ.data, from, to]);
  const podAnomalyRegions = useMemo(() => {
    if (!bandsOn || podWindowEvents.length === 0) return undefined;
    return anomalyRegions(podWindowEvents, silencedSet(silencesQ.data), from, to);
  }, [bandsOn, podWindowEvents, silencesQ.data, from, to]);

  // v0.10.160 — KPI şeridi + «Yavaş» eşiği grafik serilerinden (podPage.ts, ek sorgu yok).
  const totals = useMemo(() => (redEnabled
    ? windowTotals(s?.rate?.[0]?.points ?? [], s?.error_rate?.[0]?.points ?? [], lat?.avg?.[0]?.points ?? [], stepSec)
    : null), [redEnabled, s, lat, stepSec]);
  const p95Ms = useMemo(() => windowP95(lat?.p95?.[0]?.points ?? [], s?.rate?.[0]?.points ?? []), [lat, s]);

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

  // v0.10.135/160 — entity katmanı: kimlik zinciri, servisler, konteynerler,
  // kardeşler, etiketler, ömürler. Bayrak kapalı → bu bölümler çizilmez, sayfa
  // v0.10.130 öncesi anatomide kalır (kimlik satırı düz metin).
  const cid = entityCluster?.id ?? '';
  const entityId = entityOn && cid && (namespace || nsParam) && pod ? `pod:${cid}/${namespace || nsParam}/${pod}` : '';
  const entQ = useEntity(entityId, atParam || undefined, entityOn && !!entityId);
  const entRange = useMemo(() => ({ from, to }), [from, to]);
  const svcQ = useEntityServices(entityId, entRange, entityOn && !!entityId);
  const detail = entQ.data;
  const live = detail ? entityLiveness(detail.entity) : null;
  const ctrQ = useEntityContainers(entityId, entityOn && !!entityId && !!detail && live !== 'gone');
  const siblingRows = useMemo(() => joinSiblings(detail?.siblings ?? [], clusterPodsList?.pods), [detail, clusterPodsList]);
  const nearest = detail?.parents[0];
  const siblingLabel = nearest?.type === 'workload'
    ? `aynı iş yükü · ${nearest.labels?.kind ?? 'workload'}/${nearest.name}`
    : nearest?.type === 'namespace' ? `namespace ${nearest.name} içindeki diğer pod'lar` : 'kardeş pod\'lar';

  // JVM/JBoss JMX — pod'a filtreli (byPod=false: tek pod, jboss datasource'a
  // gruplanır, jvm toplam). deploy JMX keşfini sürer. v0.10.160: KAPALI
  // disclosure, açılınca fetch (Thanos maliyet disiplini).
  const [jmxOpen, setJmxOpen] = useState(false);
  const [promOpen, setPromOpen] = useState(false);
  // Keşif (tek etiket sorgusu) HEP açık — kapalı satır «N metrik» diyebilsin
  // (inceleme #10); yalnız N trend sorgusu açılınca.
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
      staleTime: 60_000, retry: 1, enabled: jmxOpen,
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

  // Pivotlar (B aşısı): geri · servis sayfası · bu pod'un trace'leri (hatalı
  // önce) · loglar. Trace linki tracesPivotHref üzerinden (pencere ZORUNLU
  // argüman — ham ?range= boşken /traces kendi 30m'sine düşerdi, inceleme
  // #9). Log linki: /api/logs pod parametresi almıyor (brief §4) → görünür,
  // kaldırılabilir bir PİL (`filters`), `q` değil — `q` CH'de gövde LIKE'ı,
  // alan:"değer" orada ölü (inceleme #1; prod ES query_string'te çalışır).
  // Servis kapsamı ayrıca `service`te: pil kaldırılsa bile makul görünüm.
  const traceFilters = JSON.stringify([{ k: 'k8s.pod.name', op: '=', v: [pod] }]);
  const tracesHref = tracesPivotHref({ window: range, service: service || undefined, cluster: traceCluster || undefined, filters: traceFilters, hasError: true });
  const logsPodPill = encodeFiltersParam([{ key: 'kubernetes.pod_name', value: pod, negated: false, disabled: false }]);
  const pivots: PodPivot[] = [
    // v0.9.965 — GERİ linki penceresini taşır: pod'a özel bir pencereden
    // gelen operatör "← servis" dediğinde aynı zaman aralığına döner.
    { href: service ? serviceHref(service, { range, tab: backTab }) : '/clusters', label: `← ${service ? `${service} · ${backLabel}` : 'Clusters'}` },
    ...(service ? [{ href: serviceHref(service, { range }), label: 'Servis sayfası ↗' }] : []),
    { href: tracesHref, label: 'Trace\'ler ↗', title: 'k8s.pod.name = pod, hatalı önce' },
    { href: logsHref({ window: range, service: service || undefined, cluster: traceCluster || undefined, filters: logsPodPill }), label: 'Loglar ↗', title: 'kubernetes.pod_name pili (ES alan süzgeci; CH lokalde gövde araması — pili kaldırınca servis kapsamı kalır)' },
  ];
  const scopeLabel = service || `bu pod'dan geçen tüm servisler${spanCluster ? ` · cluster ${spanCluster}` : ''}`;

  return (
    <>
      <Topbar title={`Pod · ${pod}`} range={range} onRangeChange={setRange} />
      <PageShell>
        <PodIdentityLine detail={detail} clusterName={cluster || clusterParam} namespace={namespace || nsParam} pod={pod}
          row={row} at={atParam} pageRange={range} pivots={pivots} />
        {podClusterMatches.length > 1 && (
          <div className="pod-cap" style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap', marginBottom: 10 }}>
            <Badge tone="warning">bu ad {podClusterMatches.length} cluster'da var</Badge>
            <span>gösterilen: <span className="mono">{cluster}</span> · öteki:</span>
            {podClusterMatches.filter(c => c !== cluster).map(c => (
              <Link key={c} to={podDetailPath({ pod, cluster: c, namespace: nsParam || undefined, service: service || undefined, range: rangeParam || null, at: atParam || undefined })} className="sec mono">{c}</Link>
            ))}
          </div>
        )}
        {!row && !podsPending && searchTruncated && (
          <div className="pod-cap"><Badge tone="warning">Pod listesi sunucuda kesildi (topk 500)</Badge> bu pod kesilen kuyrukta kalmış olabilir; Thanos alanları «—» ise «yok» kesin değil.</div>
        )}

        <PodKpiStrip totals={totals} redState={redState} scopeLabel={scopeLabel} row={row} rowPending={podsPending} clamped={clamped} />

        {/* RED — servisin kümülatif metrikleri, bu pod'a scope'lu */}
        <div className="pod-sec">
          <PanelTitle sub={redEnabled ? <>{scopeLabel} · paylaşılan crosshair{podWindowEvents.length > 0 && <> · anomali bantları (servis düzeyi): {bandsOn ? 'açık' : 'kapalı'} · <LinkButton onClick={toggleBands} aria-pressed={bandsOn} title="Anomali bantlarını grafiklerde göster/gizle (?bands=1)" className="bands-toggle">{bandsOn ? 'kapat' : 'aç'}</LinkButton></>}</> : undefined}
            right={service ? <Link to={serviceHref(service, { range })} className="sec">→ {service} · Overview</Link> : undefined}>
            Servis metrikleri · bu pod
          </PanelTitle>
          {redEnabled ? (
            <div className="ov-grid ov-charts-3 ov-mb">
              <Suspense fallback={<Spinner />}>
                <CorePanelMultiLazy
                  title="Response time"
                  storageKey="pod-response-time" height={200}
                  unit="ms" viz="line" xRange={xRange}
                  syncKey={podChartSync}
                  regions={podAnomalyRegions}
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
                  regions={podAnomalyRegions}
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
                  regions={podAnomalyRegions}
                  loading={redStatus === 'loading'}
                  error={redStatus === 'error' ? 'Metrikler yüklenemedi' : undefined}
                  items={[{ name: 'errors', role: 'error', series: s?.error_rate ?? [] }]}
                />
              </Suspense>
            </div>
          ) : (
            <Empty icon="—" title="Bu pod bir Coremetry servisine eşlenmedi — RED metrikleri yok (trace listesi ve altyapı aşağıda).">
              {/* v0.9.959 (G8/Ö22) — "eşlenmedi" ile "kesik listede
                  bulunamadı" aynı ekran olamaz. İkincisi bir eksiklik
                  beyanıdır, bir sonuç değil. */}
              {entityOn && !entityCluster && cluster && (
                <div style={{ marginTop: 6 }}>Cluster <span className="mono">{cluster}</span> için Remote Cluster kaydı / span cluster değeri yok — Settings → Clusters'ta eşle.</div>
              )}
            </Empty>
          )}
        </div>

        {/* Taşıdığı servisler — entity_seen_5m (bayrak açıkken) */}
        {entityOn && entityId && (
          <div className="pod-sec">
            <PanelTitle sub="entity_seen_5m · seçili pencere">Taşıdığı servisler</PanelTitle>
            <PodServicesTable data={svcQ.data} pending={svcQ.isPending} error={svcQ.error} pod={pod}
              spanCluster={traceCluster} pageRange={range} />
          </div>
        )}

        {/* Trace listesi — B aşısı: varsayılan «Hatalı» */}
        <div className="pod-sec">
          <PanelTitle sub="/api/traces · k8s.pod.name süzgeci" right={<Link to={tracesHref} className="sec">→ Traces'te aç</Link>}>Trace listesi</PanelTitle>
          {multiSpanCluster && <div className="pod-cap">Cluster kaydı birden çok span değeri taşıyor ({spanCluster}) — trace süzgecinde cluster UYGULANMADI; grafikler hepsini ölçer.</div>}
          <PodTracesTable ctx={{ pod, from, to, cluster: traceCluster, service }} p95Ms={p95Ms} />
        </div>

        {/* Konteynerler — Thanos KSM anlık (canlı pod) */}
        {entityOn && detail && live !== 'gone' && (
          <div className="pod-sec">
            <PanelTitle sub="Thanos KSM · anlık durum">Konteynerler</PanelTitle>
            <PodContainersTable ctr={ctrQ.data} pending={ctrQ.isPending} containerRecs={detail.containers} />
          </div>
        )}
        {entityOn && detail && live === 'gone' && (
          <div className="pod-sec">
            <PanelTitle sub="pod artık mevcut değil">Konteynerler</PanelTitle>
            <div className="pod-cap">Sonlanmış pod: KSM anlık durum yok{detail.containers && detail.containers.length > 0 ? ` · kayıtlı konteynerler: ${detail.containers.map(c => c.name).join(', ')}` : ''}. Ömür ve son görülme yukarıda.</div>
          </div>
        )}

        {/* Infra — tek-pod CPU/Mem trend. cluster çözülene dek gizli. */}
        <div className="pod-sec">
          <PanelTitle sub={cluster ? `Thanos PodTrend · konteynerlerin toplamı${clamped ? ` · pencere tavanı son ${THANOS_MAX_WINDOW_LABEL}` : ''}` : undefined}>
            Altyapı{cluster ? <> · <span className="mono">{cluster}</span></> : ''}
          </PanelTitle>
          {cluster ? (
            <>
              {/* Satır (topk 500) yokken de SOR (inceleme #2): TrendPanel adla
                  sorgular, satırsız modu destekler ve boş pencereyi kendisi
                  söyler — «seri yok» demek için önce Thanos'a bakılır. */}
              {!row && !podsPending && (
                <div className="pod-cap">{live === 'gone' ? 'Sonlanmış pod — Thanos\'ta güncel seri beklenmez; ömür penceresi için Clusters sayfasında tarihsel pencere seçin.' : 'Pod topk 500 listesinde değil — request/limit çizgileri yok; seri varsa yine çizilir.'}</div>
              )}
              {/* Madde 4 sweep — trend drag-zoom sayfa range'ine yazar,
                  çift-tık geri-yığını pop eder (audit §2.4 kararı güncellendi). */}
              <ThanosTrendPanel cluster={cluster} namespace={namespace} pod={pod} row={row} fromNs={cFrom} toNs={cTo}
                onZoom={handleZoom} onZoomReset={handleZoomReset} />
            </>
          ) : (
            <div className="pod-cap">{podsPending ? 'cluster çözülüyor…' : 'cluster çözülemedi — Thanos kaynaklarında bu pod bulunamadı.'}</div>
          )}
        </div>

        {/* Kardeşler | Etiketler + Yaşam döngüsü */}
        {entityOn && detail && (
          <div className="pod-sec pod-two">
            <div>
              <PanelTitle sub={`${siblingLabel} · ${detail.siblings?.length ?? 0}${(detail.siblings?.length ?? 0) >= 50 ? '+' : ''} pod`}
                right={nearest?.type === 'workload' ? <Link to={entityHref(nearest, { range, at: atParam || undefined, clusterName: detail.cluster?.name ?? cluster })} className="sec">→ iş yükü sayfası</Link> : undefined}>
                Kardeş pod'lar
              </PanelTitle>
              <PodSiblingsTable rows={siblingRows} pageRange={range} at={atParam} clusterName={detail.cluster?.name ?? cluster}
                truncated={!!clusterPodsList?.truncated} />
            </div>
            <div>
              <PanelTitle sub="kube_pod_labels">Etiketler</PanelTitle>
              <PodLabelsTable labels={detail.entity.labels} />
              <div style={{ height: 14 }} />
              <PanelTitle sub="aynı ad · entity kayıtları">Yaşam döngüsü</PanelTitle>
              <PodLifetimesTable lifetimes={detail.lifetimes} atMatch={detail.atMatch} at={atParam} />
            </div>
          </div>
        )}

        {/* Ek — varsayılan kapalı; açılınca yüklenir */}
        <div className="pod-sec pod-ek">
          <PanelTitle sub="varsayılan kapalı · açılınca yüklenir">Ek</PanelTitle>
          <Card style={{ padding: 0 }}>
            <DisclosureButton anatomy="section" expanded={jmxOpen} onClick={() => setJmxOpen(v => !v)}>
              JVM / JBoss (JMX) <span className="field-hint">· pod'a filtreli · deploy {deploy || '—'} · {jmxMetricsQ.isPending && cluster ? '…' : `${jmxMetrics.length} metrik`}</span>
            </DisclosureButton>
            {jmxOpen && (
              <div className="pod-ek-body">
                {!cluster || !namespace ? <div className="pod-cap">cluster/namespace çözülmeden JMX keşfi yapılamaz.</div>
                  : jmxMetricsQ.isPending ? <Spinner />
                  : jmxMetrics.length === 0 ? <div className="pod-cap">Bu deploy için JMX metriği keşfedilmedi.</div>
                  : (
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
                  )}
              </div>
            )}
          </Card>
          <Card style={{ padding: 0 }}>
            <DisclosureButton anatomy="section" expanded={promOpen} onClick={() => setPromOpen(v => !v)}>
              Prometheus sorguları <span className="field-hint">· pod kapsamı</span>
            </DisclosureButton>
            {promOpen && (
              <div className="pod-ek-body pod-ek-prom">
                <PromQLList queries={[
                  ['CPU (cores)', `rate(container_cpu_usage_seconds_total{namespace="${promQuote(namespace)}",pod="${promQuote(pod)}"}[5m])`],
                  ['Working-set memory', `container_memory_working_set_bytes{namespace="${promQuote(namespace)}",pod="${promQuote(pod)}"}`],
                ]} />
              </div>
            )}
          </Card>
        </div>
      </PageShell>
    </>
  );
}
