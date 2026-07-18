import { useMemo, useRef, useState } from 'react';
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
import { podWorkloadName, workloadMatchesService } from '@/pages/clusters/podWorkload';
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

export function ServiceInfraTab({ service, range, onZoom }: {
  service: string;
  range: TimeRange;
  // v0.9.58 — grafik drag-seçimi global time picker'a yazar
  // (Service.tsx'in ServiceOverview'a verdiği handler'ın aynısı).
  onZoom?: (fromUnixSec: number, toUnixSec: number) => void;
}) {
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
  // v0.9.56 (operatör: breakdown'dan gidince de çalışmıyor) — span
  // attr'ları cluster çözemiyorsa (attr yok / ad Thanos kaynağıyla
  // uyuşmuyor) TÜM kaynaklara bakılır; çipler yalnız eşleşen pod
  // bulunan cluster'ları gösterir. Kaynak sayısı Settings'te sınırlı
  // olduğundan fan-out bounded, sorgular Clusters cache'ini paylaşır.
  const matched = useMemo(() => {
    const sources = sourcesQ.data?.clusters ?? [];
    const thanos = new Set(sources);
    const viaSpans = (svcClustersQ.data?.clusters ?? [])
      .map(c => c.cluster)
      .filter(c => thanos.has(c));
    return viaSpans.length > 0 ? viaSpans : sources;
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

  // Pod eşleşme zinciri (v0.9.56 — operatör vakası: metadata ns/deploy
  // türetilememiş servislerde sekme boştu; pod adı zaten servis adını
  // taşıyor: bsa-adkservices-login-prep-<rs>-<rand>):
  //   1. DeploymentRow.podNames (gerçek KSM eşlemesi) — ns+deploy varsa
  //   2. "<deploy>-" önek + ns — deploy var, KSM satırı yoksa
  //   3. YEDEK: pod'un zenginleştirilmiş service alanı (v0.9.12
  //      korelasyonu) YA DA soyulmuş iş-yükü adı == servis adı
  //      (podWorkloadName — prefix değil EŞİTLİK: kardeş servis
  //      önekleri karışmaz). ns türetildiyse süzgeç olarak uygulanır.
  // Bilinçli memo'suz: useQueries kimliği her render değişir, tarama
  // ≤ birkaç bin satır.
  const rows: ClusterPodRow[] = [];
  matched.forEach((c, i) => {
    const depRow = deploy
      ? (depQs[i]?.data?.deployments ?? []).find(d => d.deployment === deploy)
      : undefined;
    const podSet = depRow ? new Set(depRow.podNames) : null;
    for (const p of podQs[i]?.data?.pods ?? []) {
      if (ns && p.namespace !== ns) continue;
      const hit = podSet ? podSet.has(p.pod)
        : deploy ? p.pod.startsWith(deploy + '-')
        : (p.service === service || workloadMatchesService(podWorkloadName(p.pod), service));
      if (hit) rows.push(p);
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
  const [cpuByPod, setCpuByPod] = useState(false);
  const [memByPod, setMemByPod] = useState(false);
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
  if (metaQ.isPending || sourcesQ.isPending || svcClustersQ.isPending) return <Spinner />;
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

      {/* Pod tablosu — cluster-gruplu (Cluster kolonu + çip filtresi). */}
      <h3 ref={podsRef} style={{ fontSize: 13, margin: '18px 0 8px', ...flashStyle('pods') }}>
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
