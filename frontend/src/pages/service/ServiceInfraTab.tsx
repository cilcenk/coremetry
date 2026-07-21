import { useMemo, useRef, useState } from 'react';
import { useSearchParams, useNavigate } from 'react-router-dom';
import { useQuery, useQueries } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { useServicesMetadata } from '@/lib/queries';
import { timeRangeToNs, fmtBytes, fmtNum } from '@/lib/utils';
import { Card } from '@/components/ui';
import { Spinner, Empty } from '@/components/Spinner';
import { useDataTable } from '@/components/DataTable';
import { MetricArea } from '@/pages/clusters/MetricArea';
import { PromQLList } from '@/pages/clusters/PromQLList';
import { promQuote } from '@/pages/clusters/promQuote';
import { fmtCores, restartColor } from '@/pages/clusters/thresholds';
import { podMatchesService } from '@/pages/clusters/podWorkload';
import { dsToken, reconcile, applyDsIsolate } from '@/pages/service/jmxSelectors';
import { podDetailPath } from '@/pages/service/podDetailPath';
import { ServiceClusterPods } from '@/pages/service/ServiceClusterPods';
import type { DataTableColumn } from '@/lib/dataTable';
import type { ClusterPodRow, TimeRange } from '@/lib/types';

// ServiceInfraTab — servis detayının Infrastructure sekmesi (v0.9.51,
// design handoff §8). Servisin k8s.namespace + deployment eşlemesiyle
// (metadata deriver, v0.8.436/v0.9.25) Thanos'taki pod'larını cluster
// dağılımıyla gösterir: eşleşme notu, cluster çipleri, KPI satırı,
// CPU/Mem Total/By-pod grafikleri (MetricArea), cluster-gruplu pod
// tablosu; pod satırına tıklama TAM /pod detay sayfasına gider (v0.9.151,
// eski drawer kaldırıldı). Tüm görseller best-effort görünmez-düşer; fetch
// yalnız sekme aktifken (bileşen ancak o zaman mount olur).

// dominantNamespace — eşleşen pod'ların en sık namespace'i (v0.9.56):
// metadata ns türetilememişse grafik sorgularının namespace parametresi
// buradan gelir (yedek modda pod'lar zaten ada göre eşleşti).
function dominantNamespace(rows: ClusterPodRow[]): string {
  const counts = new Map<string, number>();
  for (const r of rows) {
    if (r.namespace) counts.set(r.namespace, (counts.get(r.namespace) ?? 0) + 1);
  }
  let best = '', n = 0;
  for (const [ns, c] of counts) {
    if (c > n || (c === n && ns < best)) { best = ns; n = c; }
  }
  return best;
}

// v0.9.155 — Cluster kolonu kaldırıldı: pod listesi artık cluster'a göre
// açılır grup (ServiceClusterPods), cluster grup başlığında.
const POD_COLS: DataTableColumn<ClusterPodRow>[] = [
  { id: 'pod',      label: 'Pod',      sortValue: r => r.pod,      naturalDir: 'asc', width: 300 },
  { id: 'phase',    label: 'Status',   sortValue: r => r.phase ?? '', naturalDir: 'asc', width: 100 },
  { id: 'cpuCores', label: 'CPU',      sortValue: r => r.cpuCores, numeric: true, width: 90 },
  { id: 'memBytes', label: 'Memory',   sortValue: r => r.memBytes, numeric: true, width: 100 },
  { id: 'netIn',    label: 'Net in',   sortValue: r => r.netInBps ?? 0, numeric: true, width: 90 },
  { id: 'netOut',   label: 'Net out',  sortValue: r => r.netOutBps ?? 0, numeric: true, width: 90 },
  { id: 'restarts', label: 'Restarts', sortValue: r => r.restarts ?? 0, numeric: true, width: 84 },
];

export function ServiceInfraTab({ service, range, onZoom }: {
  service: string;
  range: TimeRange;
  // v0.9.58 — grafik drag-seçimi global time picker'a yazar
  // (Service.tsx'in ServiceOverview'a verdiği handler'ın aynısı).
  onZoom?: (fromUnixSec: number, toUnixSec: number) => void;
}) {
  const [params, setParams] = useSearchParams();
  const navigate = useNavigate();
  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);

  const metaQ = useServicesMetadata();
  const ns = metaQ.data?.[service]?.namespace ?? '';
  const deploy = metaQ.data?.[service]?.deployment ?? '';

  const sourcesQ = useQuery({
    queryKey: ['cluster-sources'],
    queryFn: () => api.clusterSources(),
    staleTime: 300_000,
  });
  // Cluster keşfi TÜM etkin Thanos kaynaklarını tarar (v0.9.138 — operatör:
  // "ocpma çıkıyor, ocpmb çıkmıyor"). ÖNCEDEN (v0.9.56) span-türetimli
  // cluster listesine kilitleniyordu: span'lar EN AZ BİR cluster çözerse
  // YALNIZ onları tarar, span'ı cluster-attr TAŞIMAYAN (ya da adı Thanos
  // kaynağıyla uyuşmayan) bir cluster hiç sorgulanmaz, servis orada koşsa
  // bile çip olarak çıkmazdı — all-or-nothing hata. Span-türetimi cluster
  // adında güvenilmez olduğundan cluster SEÇİMİNDE kullanılmaz; hangi
  // cluster'da servisin pod'u olduğunu precise pod-eşleşmesi
  // (podMatchesService, v0.9.130) belirler → clustersWithPods çipleri.
  // Kaynak sayısı Settings'te sınırlı (fan-out bounded, Clusters cache'i
  // paylaşılır). serviceClusters yalnız ServiceClusterBreakdown'da kullanılır.
  const matched = useMemo(() => sourcesQ.data?.clusters ?? [], [sourcesQ.data]);

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

  // Pod eşleşme zinciri (v0.9.56 → saf podMatchesService'e taşındı,
  // v0.9.130 operatör raporu: "bazı cluster'ları buluyor bazılarını
  // bulamıyor"). Karar podMatchesService'te (podWorkload.ts, testli):
  // ns süzgeci + deploy varken podSet ÜYELİĞİ ⋃ "<deploy>-" prefix
  // (podSet artık ADDİTİF, kilit değil) VEYA yedek modda isim-eşitliği.
  // Bilinçli memo'suz: useQueries kimliği her render değişir, tarama
  // ≤ birkaç bin satır.
  const rows: ClusterPodRow[] = [];
  matched.forEach((c, i) => {
    const depRow = deploy
      ? (depQs[i]?.data?.deployments ?? []).find(d => d.deployment === deploy)
      : undefined;
    const podSet = depRow ? new Set(depRow.podNames) : null;
    for (const p of podQs[i]?.data?.pods ?? []) {
      if (podMatchesService(p, { service, deploy, ns, podNames: podSet })) rows.push(p);
    }
  });
  // Çipler/grafikler için gerçek küme: eşleşen pod'u OLAN cluster'lar.
  const clustersWithPods = [...new Set(rows.map(r => r.cluster))];
  // Grafik parametreleri yedek modda da dolu: deploy yoksa servis adı
  // (pod'lar zaten o önekle koşuyor), ns yoksa eşleşen pod'ların baskın
  // namespace'i.
  const effDeploy = deploy || service;
  const effNs = ns || dominantNamespace(rows);

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
  // v0.9.72 (operatör isteği) — CPU/Mem default pod-bazlı (By pod):
  // servis-infra sekmesinde asıl soru "hangi pod sıcak", tek toplam
  // çizgi bunu gizliyordu. Total'a toggle'la geçilir.
  const [cpuByPod, setCpuByPod] = useState(true);
  const [memByPod, setMemByPod] = useState(true);
  // Sunucu 6h clamp'i — Clusters Overview'la aynı dürüstlük (v0.9.21).
  const { cFrom, cTo, clamped } = useMemo(() => {
    const sixH = 6 * 3600 * 1e9;
    if (to - from > sixH) return { cFrom: to - sixH, cTo: to, clamped: true };
    return { cFrom: from, cTo: to, clamped: false };
  }, [from, to]);
  // v0.9.56 — yedek modda da grafik: deploy yoksa servis adı, ns yoksa
  // eşleşen pod'ların baskın namespace'i (effDeploy/effNs yukarıda).
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

  // JVM/JBoss JMX auto-discovery (v0.9.144, operatör: "Thanos'taki jvm
  // metrikleri yine Infrastructure altında gözüksün"). chartCluster'da
  // servisin taşıdığı jvm_/jboss_ metrik ADLARINI keşfet (aynı effNs/effDeploy
  // cAdvisor selector'ı) — SABİT ad listesi YOK — her biri için pod-başı
  // MetricArea. `_total`/`_sum` sayaçları backend'de rate'lenir.
  const [jmxBy, setJmxBy] = useState<Record<string, boolean>>({});
  // v0.9.149 — Pod + Datasource seçiciler (Grafana $pod/$datasource,
  // operatör "pod ve datasource"). jpod = BACKEND filtre (sorgu tek pod'a
  // daralır, tüm paneller o pod'un JMX'ini gösterir); jds = CLIENT-SIDE
  // izole (By-datasource serisini render'da süzer, re-fetch yok). İkisi de
  // URL kaynak-of-truth (replace:true).
  const jpod = params.get('jpod') ?? '';
  const jds = params.get('jds') ?? '';
  const setJmxParam = (key: 'jpod' | 'jds', v: string) => setParams(prev => {
    const next = new URLSearchParams(prev);
    if (v) next.set(key, v); else next.delete(key);
    return next;
  }, { replace: true });
  const jmxMetricsQ = useQuery({
    queryKey: ['jmx-metrics', chartCluster, effNs, effDeploy],
    queryFn: () => api.clusterJmxMetrics(chartCluster, effNs, effDeploy),
    staleTime: 60_000, retry: 1, enabled: trendOK,
  });
  const jmxMetrics = useMemo(() => jmxMetricsQ.data?.metrics ?? [], [jmxMetricsQ.data]);
  // Pod seçici opsiyonları — chartCluster VE effNs eşleşen pod'lar. effNs
  // süzgeci ŞART: backend sorgusu namespace="effNs" ile sabitler, başka
  // namespace'teki bir pod seçilse hiç eşleşmez (review #4/#6). effJpod:
  // seçili pod artık listede yoksa (cluster değişti / pod deploy'da yeniden
  // adlandı / paylaşılan URL) YOK SAY → "All pods" — bayat jpod tüm bölümü
  // boşaltıyordu (review #2/#5). URL'yi reaktif YAZMAYIZ (bir-yön-oku
  // tuzağı, v0.8.253/256); render+sorgu için türetiriz.
  const podOptions = [...new Set(
    rows.filter(r => r.cluster === chartCluster && r.namespace === effNs).map(r => r.pod),
  )].sort();
  const effJpod = reconcile(jpod, podOptions);
  const jmxPanelQs = useQueries({
    // byPod varsayılanı render toggle'ıyla AYNI olmalı (jboss→By datasource,
    // jvm→By pod): eskiden sorgu `?? true` çekiyordu ama toggle jboss'ta
    // `?? false` gösteriyordu — veri/etiket uyuşmazlığı (v0.9.149 düzeltme).
    // effJpod dolu ise sorgu o pod'a daralır.
    queries: jmxMetrics.map(m => {
      const byPod = jmxBy[m] ?? !m.startsWith('jboss_');
      return {
        queryKey: ['jmx-trend', chartCluster, effNs, effDeploy, m, byPod, effJpod, cFrom, cTo],
        queryFn: () => api.clusterJmxTrend(chartCluster, effNs, effDeploy, m, byPod, cFrom, cTo, effJpod),
        staleTime: 60_000, retry: 1,
      };
    }),
  });
  // dsOptions jboss panel serilerinin datasource token'larından (dsToken);
  // effJds bayat değeri (re-fetch'te kaybolan datasource) YOK SAYAR → tüm
  // datasource'lar (review #3). Saf mantık jmxSelectors.ts + testli.
  const dsOptions = [...new Set(
    jmxMetrics.flatMap((m, i) =>
      m.startsWith('jboss_') ? (jmxPanelQs[i]?.data?.series ?? []).map(s => dsToken(s.name)) : []),
  )].filter(Boolean).sort();
  const effJds = reconcile(jds, dsOptions);

  // v0.9.151 — pod'a tıklama artık cramped drawer yerine TAM /pod detay
  // sayfasına gider (H.Polat önerisi, "onun yerine ... tek bir detay sayfa").
  // service RED'i, effDeploy JMX keşfini, pod'un kendi namespace'i selector'ı
  // sürer. Eski ?pod= drawer kaldırıldı.
  // Pod'a tıkla → /pod tam detay. podDetailPath ?range'i taşır (v0.9.152
  // review: brush'lanmış pencere localStorage'a yazılmaz, drill'de geçmezse
  // /pod 1h'e düşer; /traces & /logs drill'leri de range taşır).
  const openPod = (r: ClusterPodRow) => navigate(podDetailPath({
    cluster: r.cluster, namespace: r.namespace, pod: r.pod,
    service, deploy: effDeploy, range: params.get('range'), from: 'infra',
  }));

  const dt = useDataTable<ClusterPodRow>({
    storageKey: 'service-infra-pods-v2', columns: POD_COLS, // v0.9.157: 'cluster' kolonu kalktı, bayat sort/width sıfırla
    rows: visRows, initialSort: { id: 'cpuCores', dir: 'desc' },
  });

  // v0.9.59 (operatör isteği, OpenShift konsol deseni) — KPI kartları
  // tıklanınca ilgili metriğe götürür: CPU/Mem → kendi grafiği,
  // Pods → pod tablosu, Restarts → tablo restarts-desc sıralanıp
  // odaklanır. Hedef kısa bir accent çerçevesiyle yanıp söner;
  // prefers-reduced-motion'da kaydırma anlık olur.
  const cpuChartRef = useRef<HTMLDivElement>(null);
  const memChartRef = useRef<HTMLDivElement>(null);
  const podsRef = useRef<HTMLDivElement>(null);
  const [flash, setFlash] = useState('');
  const jumpTo = (key: 'cpu' | 'mem' | 'pods', opts?: { sortRestarts?: boolean }) => {
    if (opts?.sortRestarts) dt.setSort({ id: 'restarts', dir: 'desc' });
    const ref = key === 'cpu' ? cpuChartRef : key === 'mem' ? memChartRef : podsRef;
    const reduce = window.matchMedia('(prefers-reduced-motion: reduce)').matches;
    ref.current?.scrollIntoView({ behavior: reduce ? 'auto' : 'smooth', block: 'start' });
    setFlash(key);
    window.setTimeout(() => setFlash(''), 1400);
  };
  const kpiClick = (key: 'cpu' | 'mem' | 'pods', opts?: { sortRestarts?: boolean }) => ({
    role: 'button' as const, tabIndex: 0,
    onClick: () => jumpTo(key, opts),
    onKeyDown: (e: React.KeyboardEvent) => {
      if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); jumpTo(key, opts); }
    },
    style: { cursor: 'pointer' },
  });
  const flashStyle = (key: string): React.CSSProperties => ({
    outline: flash === key ? '2px solid var(--accent)' : '2px solid transparent',
    outlineOffset: 4, borderRadius: 8, transition: 'outline-color .35s',
  });

  // ── Kapılar (hook'lardan SONRA) ──
  if (metaQ.isPending || sourcesQ.isPending) return <Spinner />;
  if ((sourcesQ.data?.clusters ?? []).length === 0) {
    return <Empty icon="▦" title="No Thanos clusters configured">
      Add a remote cluster under Settings → Remote clusters to see pod-level infrastructure here.
    </Empty>;
  }
  // v0.9.56 — metadata'sızlık artık kapı DEĞİL: ad-tabanlı yedek zincir
  // pod bulur. Tek terminal boş durum: hiçbir kaynakta eşleşen pod yok.
  const podsLoading = podQs.some(q => q.isPending);
  if (rows.length === 0) {
    if (podsLoading) return <Spinner />;
    return <Empty icon="▦" title="No pods matched">
      Tried {ns && deploy ? `k8s.namespace=${ns} · ${deploy}` : 'the k8s metadata mapping'}
      {' '}and pod-name matching (<span className="mono">{service}-*</span>) across{' '}
      {matched.length} Thanos cluster{matched.length > 1 ? 's' : ''} — nothing matched.
      Check that the pods follow the <span className="mono">&lt;service&gt;-&lt;hash&gt;-&lt;rand&gt;</span> naming
      or curate namespace/deployment in the service catalog.
    </Empty>;
  }

  // v0.9.57 (operatör raporu: "Running pods 0 ama grafikler dolu") —
  // phase kube-state-metrics'ten gelir (best-effort), CPU/Mem
  // cadvisor'dan; KSM o cluster'da yoksa her satırın phase'i boştur.
  // Faz verisi HİÇ yokken 0 saymak fake-zero: kart "Pods" başlığıyla
  // toplam sayıya düşer ve durumu açıkça söyler.
  const phaseKnown = visRows.some(r => r.phase);
  const running = visRows.filter(r => r.phase === 'Running').length;
  const cpuSum = visRows.reduce((a, r) => a + r.cpuCores, 0);
  const memSum = visRows.reduce((a, r) => a + r.memBytes, 0);
  const restartSum = visRows.reduce((a, r) => a + (r.restarts ?? 0), 0);
  const kpiVal = { fontSize: 26, fontWeight: 700 } as const;
  const kpiSub = { fontSize: 11, color: 'var(--text3)', marginTop: 4 } as const;

  return (
    <>
      {/* Drill breadcrumb (v0.9.147, pod segmenti v0.9.151'de kalktı — pod
          tıklaması artık /pod tam sayfasına gider, bu tab'ta ?pod= yok):
          All clusters › cluster, URL ?icluster'dan. Yalnız çip filtresi
          varken görünür (tıkla-geri, replace:true). */}
      {icluster && (() => {
        const link: React.CSSProperties = { all: 'unset', cursor: 'pointer', color: 'var(--accent2)' };
        const sep = <span style={{ color: 'var(--text3)' }}>›</span>;
        return (
          <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap', fontSize: 12, marginBottom: 10 }}>
            <button type="button" style={link} title="Clear cluster filter"
              onClick={() => setICluster('')}>
              All clusters
            </button>
            {sep}<span className="mono" style={{ color: 'var(--text)' }}>{icluster}</span>
          </div>
        );
      })()}
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

      {/* Cluster çipleri — tıklanınca tablo + grafikler o cluster'a
          daralır. Yalnız eşleşen pod'u OLAN cluster'lar (v0.9.56). */}
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

      {/* KPI satırı (§8): Running / CPU / Memory / Restarts. Restarts
          kube sayacının TOPLAMI (24h increase değil) — dürüst etiket. */}
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(220px, 1fr))', gap: 14 }}>
        <Card density="tight" header={phaseKnown ? 'Running pods' : 'Pods'} {...kpiClick('pods')}
          title={phaseKnown ? 'Go to the pod list'
            : 'Pod status comes from kube_pod_status_phase (kube-state-metrics), which this cluster does not expose — the count shows matched pods instead. Click for the pod list.'}>
          <div className="mono" style={{ ...kpiVal, color: phaseKnown ? 'var(--ok)' : undefined }}>
            {fmtNum(phaseKnown ? running : visRows.length)}
          </div>
          <div style={kpiSub}>{phaseKnown
            ? (visRows.length - running > 0 ? `${fmtNum(visRows.length - running)} not running` : 'all pods healthy')
            : 'status unknown — kube-state-metrics not visible on this cluster'}</div>
        </Card>
        <Card density="tight" header="CPU used (cores)" {...kpiClick('cpu')} title="Go to the CPU chart">
          <div className="mono" style={kpiVal}>{visRows.length ? fmtCores(cpuSum) : '—'}</div>
        </Card>
        <Card density="tight" header="Memory used" {...kpiClick('mem')} title="Go to the memory chart">
          <div className="mono" style={kpiVal}>{visRows.length ? fmtBytes(memSum) : '—'}</div>
        </Card>
        <Card density="tight" header="Restarts (total)" {...kpiClick('pods', { sortRestarts: true })}
          title="Go to the pod list sorted by restarts">
          <div className="mono" style={{ ...kpiVal, color: restartColor(restartSum) }}>{fmtNum(restartSum)}</div>
        </Card>
      </div>

      {/* CPU/Mem area — MetricArea (Clusters ile ortak), By pod toggle. */}
      {((cpuTrendQ.data?.series?.length ?? 0) > 0 || (memTrendQ.data?.series?.length ?? 0) > 0) && (
        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 14, marginTop: 14 }}>
          <div ref={cpuChartRef} style={flashStyle('cpu')}>
            <MetricArea title={`CPU (cores) · ${chartCluster}${clamped ? ' (last 6h)' : ''}`} byLabel="By pod"
              by={cpuByPod} onToggle={setCpuByPod} onZoom={onZoom}
              series={cpuTrendQ.data?.series} seriesName="CPU" />
          </div>
          <div ref={memChartRef} style={flashStyle('mem')}>
            <MetricArea title={`Memory · ${chartCluster}${clamped ? ' (last 6h)' : ''}`} byLabel="By pod"
              by={memByPod} onToggle={setMemByPod} onZoom={onZoom}
              series={memTrendQ.data?.series} seriesName="Memory" unit="bytes" />
          </div>
        </div>
      )}

      {/* JVM/JBoss (JMX) · Thanos auto-discovery (v0.9.144). Keşfedilen her
          jvm_/jboss_ metriği için pod-başı MetricArea; serisi boş → görünmez. */}
      {jmxMetrics.length > 0 && (
        <>
          <div style={{ display: 'flex', alignItems: 'baseline', justifyContent: 'space-between', gap: 12, margin: '18px 0 8px', flexWrap: 'wrap' }}>
            <h3 style={{ fontSize: 13, margin: 0 }}>
              JVM / JBoss (JMX) · <span className="mono">{chartCluster}</span>
              <span style={{ fontWeight: 400, color: 'var(--text3)' }}> · {jmxMetrics.length} metrics</span>
            </h3>
            {/* v0.9.149 — Pod (backend $pod) + Datasource (client izole)
                seçiciler. Boş liste → gizli (görünmez-düşer). */}
            <div style={{ display: 'flex', alignItems: 'center', gap: 10, fontSize: 11, color: 'var(--text3)' }}>
              {podOptions.length > 0 && (
                <label style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }}>
                  Pod
                  <select value={effJpod} onChange={e => setJmxParam('jpod', e.target.value)}
                    style={{ fontSize: 11, maxWidth: 220 }} title="Grafana $pod — sorguyu tek pod'a daraltır">
                    <option value="">All pods</option>
                    {podOptions.map(p => <option key={p} value={p}>{p}</option>)}
                  </select>
                </label>
              )}
              {dsOptions.length > 0 && (
                <label style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }}>
                  Datasource
                  <select value={effJds} onChange={e => setJmxParam('jds', e.target.value)}
                    style={{ fontSize: 11, maxWidth: 220 }} title="Grafana $datasource — panelleri seçili datasource'a izole eder">
                    <option value="">All datasources</option>
                    {dsOptions.map(d => <option key={d} value={d}>{d}</option>)}
                  </select>
                </label>
              )}
            </div>
          </div>
          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 14 }}>
            {jmxMetrics.map((m, i) => {
              const data = jmxPanelQs[i]?.data?.series;
              if (!data || data.length === 0) return null;
              const unit = m.includes('bytes') ? 'bytes' : m.includes('seconds') ? 's' : undefined;
              // jboss datasource: off = By datasource (data_source+xa_data_source),
              // on = By pod (pod başına datasource). jvm: off = Total, on = By pod.
              const isJboss = m.startsWith('jboss_');
              // effJds seçiliyse jboss panelini o datasource'a izole et (client
              // taraf, re-fetch yok). applyDsIsolate YALNIZ gerçekten datasource
              // taşıyan panellere uygular: undertow/threads/transactions gibi
              // datasource'suz jboss metrikleri (tek boş-adlı seri) izolede
              // GİZLENMEZ (review #1). Saf mantık jmxSelectors.ts + testli.
              const shown = isJboss ? applyDsIsolate(data, effJds) : data;
              if (shown.length === 0) return null;
              return (
                <MetricArea key={m}
                  title={`${m}${clamped ? ' (last 6h)' : ''}`}
                  byLabel="By pod" totalLabel={isJboss ? 'By datasource' : 'Total'}
                  by={jmxBy[m] ?? !isJboss} onToggle={v => setJmxBy(s => ({ ...s, [m]: v }))}
                  series={shown} seriesName={m} unit={unit}
                  maxSeries={isJboss ? 40 : undefined} onZoom={onZoom} />
              );
            })}
          </div>
        </>
      )}

      {/* Pod listesi — cluster'a göre AÇILIR GRUP (v0.9.155, mock A): her
          cluster kart, tıkla-kapat; pod satırı tıkla → yerinde JVM/JBoss JMX
          paneli (PodJmxInline) + "Tam detay → /pod". Sıralama/resize dt'de. */}
      <h3 ref={podsRef} style={{ fontSize: 13, margin: '18px 0 8px', ...flashStyle('pods') }}>
        Pods ({visRows.length}{icluster ? ` in ${icluster}` : ''})
      </h3>
      {visRows.length === 0 ? (
        <Empty icon="▦" title="No pods matched">
          The Thanos pod list has no pods for this namespace/deployment right now.
        </Empty>
      ) : (
        <ServiceClusterPods dt={dt} effNs={effNs} effDeploy={effDeploy}
          cFrom={cFrom} cTo={cTo} colCount={POD_COLS.length} onOpenPod={openPod} />
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
