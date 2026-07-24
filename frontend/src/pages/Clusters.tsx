import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Link, useSearchParams, useNavigate } from 'react-router-dom';
import { useQuery, useQueries } from '@tanstack/react-query';
import { ChartSpline } from 'lucide-react';
import { ThanosTrendPanel } from '@/pages/clusters/TrendPanel';
import { netTrendToSeries } from '@/pages/clusters/trendSeries';
import { Gauge } from '@/pages/clusters/Gauge';
import { PhaseDonut } from '@/pages/clusters/PhaseDonut';
import { safePct, restartColor, fmtCores, fmtBps, podPhaseBadge } from '@/pages/clusters/thresholds';
import { TREND_WINDOWS } from '@/pages/clusters/TrendPanel';
import { podWorkloadName } from '@/pages/clusters/podWorkload';
import { podDetailPath } from '@/pages/service/podDetailPath';
import { PromQLList } from '@/pages/clusters/PromQLList';
import { NodeHeatmap } from '@/pages/clusters/NodeHeatmap';
import { MiniBar } from '@/pages/clusters/MiniBar';
import { NamespaceCombobox } from '@/pages/clusters/NamespaceCombobox';
import { MetricArea } from '@/pages/clusters/MetricArea';
import { promQuote } from '@/pages/clusters/promQuote';
import { MultiLineChart } from '@/components/MultiLineChart';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { TableSkeleton } from '@/components/Skeleton';
import { Button, Card, Drawer, DrawerSection } from '@/components/ui';
import { api } from '@/lib/api';
import { useClusters } from '@/lib/queries';
import { timeRangeToNs, fmtBytes, fmtNum } from '@/lib/utils';
import { useUrlRange } from '@/lib/useUrlRange';
import { encodeRange } from '@/lib/urlState';
import { pushZoom, popZoom } from '@/lib/chart/zoomHistory';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import type { ClusterPodRow, ClusterNodeRow, ClusterNamespaceRow, ClusterDeploymentRow, ClusterAlertRow, ClusterSummary, TimeRange } from '@/lib/types';

// /clusters — uzak OpenShift cluster'larının Thanos metrikleri.
// v0.8.587 redesign (audit: docs/audit/clusters-overview-redesign-
// audit.md): ?cluster YOKSA genel görünüm (cluster kartları, yalnız
// SKALER summary uçları — N×topk pod vektörü çekilmez, sayfanın en
// pahalı yolu kalktı); ?cluster=X VARSA X'in detayı (geri linki →
// Nodes → Pods; namespace rollup S3'te araya girer). Eski linkler
// (Servis→Cluster pivotu ?cluster=&namespace=) kırılmadan detaya
// düşer; v0.8.584'ün geçici tab-strip'i kalktı (?tab yok sayılır).
//
// Fan-out İSTEMCİDE kalır: genel görünümde kart başına bir summary
// isteği (kendi 60s cache slotu, bozuk cluster kendi kartında
// "erişilemiyor"); detayda YALNIZ o cluster'ın nodes+pods sorguları
// koşar (fetch-on-open).

const NODE_COLS: DataTableColumn<ClusterNodeRow>[] = [
  { id: 'cluster',  label: 'Cluster', sortValue: r => r.cluster,  naturalDir: 'asc', width: 130 },
  { id: 'node',     label: 'Node',    sortValue: r => r.node,     naturalDir: 'asc', width: 260 },
  { id: 'role',     label: 'Role',    sortValue: r => r.role ?? '', naturalDir: 'asc', width: 90 },
  { id: 'cpuCores', label: 'CPU',     sortValue: r => r.cpuCores, numeric: true, width: 90 },
  { id: 'cpuPct',   label: 'CPU %',   sortValue: r => r.cpuPct ?? 0, numeric: true, width: 80 },
  { id: 'memBytes', label: 'Memory',  sortValue: r => r.memBytes, numeric: true, width: 100 },
  { id: 'memPct',   label: 'Mem %',   sortValue: r => r.memPct ?? 0, numeric: true, width: 80 },
  // v0.9.10 — network (best-effort; seri yoksa hücre '—').
  { id: 'netIn',    label: 'Net in',  sortValue: r => r.netInBps ?? 0, numeric: true, width: 90 },
  { id: 'netOut',   label: 'Net out', sortValue: r => r.netOutBps ?? 0, numeric: true, width: 90 },
];

// v0.8.588 — namespace rollup (satır tıklaması ?namespace= yazar);
// v0.9.5 — satır sonunda trend-drawer ikonu (filtreyle çakışmaz).
const NS_COLS: DataTableColumn<ClusterNamespaceRow>[] = [
  { id: 'namespace', label: 'Namespace', sortValue: r => r.namespace, naturalDir: 'asc', width: 220 },
  { id: 'pods',      label: 'Pods',      sortValue: r => r.pods ?? 0, numeric: true, width: 80 },
  { id: 'cpuCores',  label: 'CPU',       sortValue: r => r.cpuCores,  numeric: true, width: 90 },
  { id: 'memBytes',  label: 'Memory',    sortValue: r => r.memBytes,  numeric: true, width: 100 },
  { id: 'restarts',  label: 'Restarts',  sortValue: r => r.restarts ?? 0, numeric: true, width: 84 },
  { id: 'health',    label: 'Health',    sortValue: r => r.failing ?? 0, numeric: true, width: 90 },
  { id: 'trend',     label: '',          width: 44 },
];

// v0.9.23 — namespace içi iş yükü kademesi (Namespace → Deployment → Pod).
const DEP_COLS: DataTableColumn<ClusterDeploymentRow>[] = [
  { id: 'deployment', label: 'Workload', sortValue: r => r.deployment, naturalDir: 'asc', width: 240 },
  // v0.9.39 — KSM replicas/status (handoff §5). status boş = '—'.
  // v0.9.42 — scale-to-zero (0/0) sağlıklıdır: tam kesintinin (0/3=0)
  // yanına sıralanmasın diye ratio 1 sayılır.
  { id: 'replicas',   label: 'Replicas', sortValue: r => r.status ? (r.desiredReplicas > 0 ? r.readyReplicas / r.desiredReplicas : 1) : -1, numeric: true, width: 90 },
  { id: 'status',     label: 'Status',   sortValue: r => r.status ?? '', naturalDir: 'asc', width: 110 },
  { id: 'pods',       label: 'Pods',     sortValue: r => r.pods,       numeric: true, width: 80 },
  { id: 'cpuCores',   label: 'CPU',      sortValue: r => r.cpuCores,   numeric: true, width: 90 },
  { id: 'memBytes',   label: 'Memory',   sortValue: r => r.memBytes,   numeric: true, width: 100 },
];

const POD_COLS: DataTableColumn<ClusterPodRow>[] = [
  { id: 'cluster',   label: 'Cluster',   sortValue: r => r.cluster,   naturalDir: 'asc', width: 130 },
  { id: 'namespace', label: 'Namespace', sortValue: r => r.namespace, naturalDir: 'asc', width: 160 },
  { id: 'pod',       label: 'Pod',       sortValue: r => r.pod,       naturalDir: 'asc', width: 260 },
  // v0.9.12 — Coremetry servis eşleşmesi (korelasyon audit'i).
  { id: 'service',   label: 'Service',   sortValue: r => r.service ?? '', naturalDir: 'asc', width: 150 },
  { id: 'phase',     label: 'Status',    sortValue: r => r.phase ?? '', naturalDir: 'asc', width: 100 },
  { id: 'cpuCores',  label: 'CPU',       sortValue: r => r.cpuCores,  numeric: true, width: 90 },
  { id: 'cpuPct',    label: 'CPU %',     sortValue: r => r.cpuPct ?? 0, numeric: true, width: 80 },
  { id: 'memBytes',  label: 'Memory',    sortValue: r => r.memBytes,  numeric: true, width: 100 },
  { id: 'memPct',    label: 'Mem %',     sortValue: r => r.memPct ?? 0, numeric: true, width: 80 },
  // v0.9.10 — network (best-effort).
  { id: 'netIn',     label: 'Net in',    sortValue: r => r.netInBps ?? 0, numeric: true, width: 90 },
  { id: 'netOut',    label: 'Net out',   sortValue: r => r.netOutBps ?? 0, numeric: true, width: 90 },
  { id: 'restarts',  label: 'Restarts',  sortValue: r => r.restarts ?? 0, numeric: true, width: 84 },
];

// fmtCores v0.9.51'de thresholds.ts'e taşındı (PodDrawer + §8 ortak).

// fmtBps — ağ hızı: fmtBytes + '/s' (0 = bilinmiyor → çağıran '—' basar).
// fmtBps v0.9.51'de thresholds.ts'e taşındı.

// pctTitle — % hücresinin iki eksenli tooltip'i (v0.8.580): limit
// ekseni (throttle/OOM) hücrede, request ekseni (provisioning
// isabeti) title'da. Eksik eksen "bilinmiyor" okunur.
function pctTitle(what: string, ofLimit?: number, ofReq?: number): string {
  const lim = ofLimit ? `${ofLimit.toFixed(0)}% of limit` : 'limit unknown';
  const req = ofReq ? `${ofReq.toFixed(0)}% of request` : 'request unknown';
  return `${what}: ${lim} · ${req}`;
}

export default function ClustersPage() {
  const [range, setRange] = useUrlRange('15m'); // inline namespace/pod trendi
  const [params, setParams] = useSearchParams();
  const navigate = useNavigate();

  const sourcesQ = useQuery({
    queryKey: ['cluster-sources'],
    queryFn: () => api.clusterSources(),
    staleTime: 60_000,
  });
  const sources = sourcesQ.data?.clusters ?? [];

  // URL kaynak-of-truth (§4): ?cluster yoksa genel görünüm, varsa
  // o cluster'ın detayı. ?namespace= composable kalır (detayda pod
  // süzgeci). Eski ?tab= parametresi yok sayılır (v0.8.584 geçiciydi).
  const clusterParam = params.get('cluster') ?? '';
  const isDetail = clusterParam !== '';
  const openCluster = (name: string) => setParams(prev => {
    const next = new URLSearchParams(prev);
    next.set('cluster', name);
    // v0.9.17 — önceki cluster'ın sekmesi/drawer kimlikleri yeni
    // cluster'a taşınmaz (filtre çipleri görünür+temizlenebilir
    // olduklarından deep-link niyetine dokunulmaz).
    for (const k of ['tab', 'section', 'pod', 'ns']) next.delete(k);
    return next;
  }, { replace: true });
  // v0.9.17 — v0.9.12'nin ?service='i ve ?q/?section/?ns eklendikçe
  // temizlik güncellenmemişti: geri dönüşte sızan filtre bir SONRAKİ
  // cluster'a uygulanıyor, ?ns kalıntısı drawer'ı genel görünümün
  // üstünde bırakıyordu. Detaya ait TÜM paramlar temizlenir (tw
  // bilinçli kalır — drawer pencere tercihi, görünümü değiştirmez).
  const backToOverview = () => setParams(prev => {
    const next = new URLSearchParams(prev);
    for (const k of ['cluster', 'namespace', 'pod', 'service', 'q', 'section', 'ns', 'deployment']) {
      next.delete(k);
    }
    return next;
  }, { replace: true });

  // "telemetride görülmüyor" rozeti — Settings sekmesiyle aynı dil,
  // aynı kaynak (son 24h gözlenen cluster adları).
  const [obsFrom, obsTo] = useMemo(() => {
    const now = Date.now() * 1e6;
    return [now - 24 * 3600 * 1e9, now];
  }, []);
  const observedQ = useClusters(obsFrom, obsTo);
  const observed = useMemo(() => new Set(observedQ.data ?? []), [observedQ.data]);

  // ?ns=<cluster>|<namespace> — namespace trend drawer'ı (v0.9.5).
  // ?namespace= FİLTRESİNDEN bağımsız param: filtre ve drawer
  // birbirini engellemez (audit kısıtı).
  const nsDrawerParam = params.get('ns');
  const openNsDrawer = (r: ClusterNamespaceRow) => setParams(prev => {
    const next = new URLSearchParams(prev);
    next.set('ns', `${r.cluster}|${r.namespace}`);
    return next;
  }, { replace: true });
  const closeNsDrawer = () => setParams(prev => {
    const next = new URLSearchParams(prev);
    next.delete('ns');
    return next;
  }, { replace: true });

  // v0.9.151 — pod'a tıklama artık cramped drawer yerine TAM /pod detay
  // sayfasına gider (H.Polat önerisi). r.service (eşleşen Coremetry servisi,
  // boş olabilir) RED'i, pod adından türetilen deploy JMX keşfini sürer.
  const openPod = (r: ClusterPodRow) => navigate(podDetailPath({
    cluster: r.cluster, namespace: r.namespace, pod: r.pod,
    service: r.service ?? '', deploy: podWorkloadName(r.pod),
    range: params.get('range'), from: 'clusters',
  }));

  // Genel görünüm: yalnız summary fan-out'u (skaler, kart başına).
  const summaryQs = useQueries({
    queries: (isDetail ? [] : sources).map(name => ({
      queryKey: ['cluster-summary', name],
      queryFn: () => api.clusterSummary(name),
      staleTime: 60_000,
      retry: 1,
    })),
  });

  // v0.9.8 (L1, tabbed-detail audit §2) — sekme yönlendirmesi:
  // ?section=overview|nodes|namespaces|pods (yokluğu = overview).
  // Legacy ?tab=nodes → nodes; ?namespace taşıyan section'sız
  // deep-link (Servis→Cluster pivotu) → pods. v0.9.6'nın katlanabilir
  // panelleri sekmelerle GEÇERSİZ (bilinçli geri alma) — fetch-gating
  // deseni sekme-aktifliğine taşındı.
  const nsFilterEarly = params.get('namespace') ?? '';
  const section = (() => {
    const raw = params.get('section') ?? (params.get('tab') === 'nodes' ? 'nodes' : '');
    if (raw === 'nodes' || raw === 'namespaces' || raw === 'pods' || raw === 'overview') return raw;
    return (nsFilterEarly || params.get('service') || params.get('deployment')) ? 'pods' : 'overview';
  })();
  // v0.9.17 (self-review fix) — 'overview' PARAM YOKLUĞU olarak
  // kodlanmıştı; ?namespace=/?service= varken türetme 'pods'a
  // düştüğünden Overview düğmesi ölüyordu (majör). Kullanıcı
  // tıklaması artık overview dahil AÇIK yazar; yokluk-türetmesi
  // yalnız deep-link'ler için kalır.
  const setSection = (sec: string, extra?: (p: URLSearchParams) => void) =>
    setParams(prev => {
      const next = new URLSearchParams(prev);
      next.set('section', sec);
      next.delete('tab');
      extra?.(next);
      return next;
    }, { replace: true });

  // v0.9.38 (design handoff, Interactions §auto-refresh) — ?live=1
  // canlı yenileme: aktif detay sorgularına refetchInterval. Aralık
  // 10s (README 5s der; CLAUDE.md poll tabanı ≥10s kazanır). TanStack
  // refetchInterval gizli sekmede varsayılan olarak durur
  // (refetchIntervalInBackground=false) — document.hidden kuralı bedava.
  const live = params.get('live') === '1';
  const toggleLive = () => setParams(prev => {
    const next = new URLSearchParams(prev);
    if (live) next.delete('live'); else next.set('live', '1');
    return next;
  }, { replace: true });
  const liveMs = live ? 10_000 : undefined;
  // v0.9.58 — grafik drag-seçimi global time picker'a yazar (operatör
  // isteği; Service.tsx onZoom emsali). setRange useUrlRange'ten.
  // Madde 4 sweep — Service.tsx v0.9.199 GERİ-YIĞIN deseninin birebiri:
  // zoom öncesi görünüm yığına iter, çift-tık (chartZoomReset) bir adım
  // geri; out-of-band range değişimi (Topbar preset) yığını geçersizleştirir.
  const zoomStackRef = useRef<TimeRange[]>([]);
  const rangeRef = useRef(range); rangeRef.current = range;
  const zoomWroteRef = useRef(false);
  useEffect(() => {
    if (zoomWroteRef.current) { zoomWroteRef.current = false; return; }
    zoomStackRef.current = [];
  }, [range.preset, range.fromMs, range.toMs]);
  // v0.9.206 review-fix — range yazımı TEK URLSearchParams yazımı oldu
  // ki NamespaceDrawer ?tw= silmesini AYNI yazıma katabilsin
  // (opts.clearTw): react-router'da aynı-tick functional updater'lar
  // render-anı snapshot'ından beslenir (yazımlar zincirlenmez), ayrı
  // setTw('') + setRange çağrılarında son yazan kazanıp tw'yi hayatta
  // bırakıyordu — drawer grafiği tw'den pencerelendiğinden zoom ölü
  // görünüyor, sayfa range'i overlay arkasında sessizce değişiyordu.
  // setRange yerine doğrudan setParams güvenli: custom range
  // persistableRange dışı, localStorage yazımı zaten no-op'tu.
  // clearTw opt-in kalır — sayfa grafiği zoom'u operatörün kalıcı tw
  // tercihini silmemeli (v0.9.17 "tw bilinçli kalır").
  const chartZoom = useCallback((fromUnixSec: number, toUnixSec: number, opts?: { clearTw?: boolean }) => {
    zoomStackRef.current = pushZoom(zoomStackRef.current, rangeRef.current);
    zoomWroteRef.current = true;
    setParams(prev => {
      const next = new URLSearchParams(prev);
      if (opts?.clearTw) next.delete('tw');
      next.set('range', encodeRange({
        preset: 'custom',
        fromMs: Math.round(fromUnixSec * 1000),
        toMs: Math.round(toUnixSec * 1000),
      }));
      return next;
    }, { replace: true });
  }, [setParams]);
  const chartZoomReset = useCallback(() => {
    const { stack, view } = popZoom(zoomStackRef.current);
    zoomStackRef.current = stack;
    if (view) { zoomWroteRef.current = true; setRange(view); return; }
    if (rangeRef.current.preset === 'custom') { zoomWroteRef.current = true; setRange({ preset: '15m' }); }
  }, [setRange]);
  // v0.9.43 (review MAJÖR) — trend pencere ilerletici: [from,to]
  // memo'su Date.now()'u mount'ta bir kez yakalıyordu; live modda
  // 10s'lik her refetch AYNI geçmiş pencereyi sorguluyor (Prometheus
  // geçmişi immutable → grafikler donuk, istekler garantili no-op).
  // 60s'lik tick memo'yu tazeler — dakika bucket'ları + 60s sunucu
  // cache TTL'iyle hizalı, cache-key kardinalitesi sınırlı kalır.
  const [liveTick, setLiveTick] = useState(0);
  useEffect(() => {
    if (!live) return;
    const id = setInterval(() => {
      if (document.hidden) return; // gizli sekmede pencere ilerletme yok
      setLiveTick(t => t + 1);
    }, 60_000);
    return () => clearInterval(id);
  }, [live]);

  // Detay başlık rozeti + Overview kartları: aynı skaler summary
  // (genel görünümle queryKey paylaşır — cache ortak).
  const detailSummaryQ = useQuery({
    queryKey: ['cluster-summary', clusterParam],
    queryFn: () => api.clusterSummary(clusterParam),
    staleTime: 60_000,
    retry: 1,
    enabled: isDetail,
    refetchInterval: liveMs,
  });

  // v0.9.10 — Overview throughput grafiği: sayfa range'i penceresi
  // (audit §4 kararı — ?tw= drawer-yerel kalır), fetch yalnız
  // Overview sekmesi aktifken.
  const { from: rangeFrom, to: rangeTo, netClamped } = useMemo(() => {
    void liveTick; // v0.9.43 — live pencere ilerletici, yalnız tetikleyici
    const { from, to } = timeRangeToNs(range);
    // v0.9.21 — sunucu 6h'e SESSİZCE clamp'liyordu; geniş sayfa
    // range'inde grafik başlıksız yanıltıyordu. İstemci de clamp'ler
    // ve başlığa "(last 6h)" düşer — eksen artık yalan söylemez.
    const sixH = 6 * 3600 * 1e9;
    if (to - from > sixH) return { from: to - sixH, to, netClamped: true };
    return { from, to, netClamped: false };
    // liveTick: v0.9.43 — live modda pencereyi 60s'de bir ilerletir.
  }, [range, liveTick]);
  const netTrendQ = useQuery({
    queryKey: ['cluster-net-trend', clusterParam, rangeFrom, rangeTo],
    queryFn: () => api.clusterNetworkTrend(clusterParam, rangeFrom, rangeTo),
    staleTime: 60_000,
    retry: 1,
    enabled: isDetail && section === 'overview',
    refetchInterval: liveMs,
  });
  // v0.9.35 (B2/F4) — CPU/Mem area chart'ları, per-kart Total/By-node
  // toggle (local UI state; fetch toggle'a göre). Pencere sayfa
  // range'i (server 6h clamp).
  const [cpuByNode, setCpuByNode] = useState(false);
  const [memByNode, setMemByNode] = useState(false);
  const cpuTrendQ = useQuery({
    queryKey: ['cluster-res-trend', clusterParam, 'cpu', cpuByNode, rangeFrom, rangeTo],
    queryFn: () => api.clusterResourceTrend(clusterParam, 'cpu', cpuByNode, rangeFrom, rangeTo),
    staleTime: 60_000, retry: 1, enabled: isDetail && section === 'overview',
    refetchInterval: liveMs,
  });
  const memTrendQ = useQuery({
    queryKey: ['cluster-res-trend', clusterParam, 'mem', memByNode, rangeFrom, rangeTo],
    queryFn: () => api.clusterResourceTrend(clusterParam, 'mem', memByNode, rangeFrom, rangeTo),
    staleTime: 60_000, retry: 1, enabled: isDetail && section === 'overview',
    refetchInterval: liveMs,
  });
  // v0.9.36 (B3) — firing alerts (Overview panel).
  const alertsQ = useQuery({
    queryKey: ['cluster-alerts', clusterParam],
    queryFn: () => api.clusterAlerts(clusterParam),
    staleTime: 60_000, retry: 1, enabled: isDetail && section === 'overview',
    refetchInterval: liveMs,
  });
  const [alertsCriticalOnly, setAlertsCriticalOnly] = useState(false);

  // Detay: yalnız seçili cluster'ın AKTİF sekme sorguları.
  const detailList = isDetail ? [clusterParam] : [];
  const podQs = useQueries({
    queries: detailList.map(name => ({
      queryKey: ['cluster-pods', name],
      queryFn: () => api.clusterPods(name),
      staleTime: 60_000,
      retry: 1,
      enabled: section === 'pods',
      refetchInterval: liveMs,
    })),
  });
  const nodeQs = useQueries({
    queries: detailList.map(name => ({
      queryKey: ['cluster-nodes', name],
      queryFn: () => api.clusterNodes(name),
      staleTime: 60_000,
      retry: 1,
      // v0.9.32 — Overview heatmap de node verisini kullanır (aynı
      // cache slotu; sekme geçişinde tekrar fetch yok).
      enabled: section === 'nodes' || section === 'overview',
      refetchInterval: liveMs,
    })),
  });
  const nsQs = useQueries({
    queries: detailList.map(name => ({
      queryKey: ['cluster-namespaces', name],
      queryFn: () => api.clusterNamespaces(name),
      staleTime: 60_000,
      retry: 1,
      // v0.9.34 — combobox tüm sekmelerde namespace listesine ihtiyaç
      // duyar; Namespaces sekmesiyle aynı cache slotu (tekrar fetch yok).
      enabled: isDetail,
      refetchInterval: liveMs,
    })),
  });

  // v0.9.7 — ?q= metin süzgeci (operatör isteği): üç detay tablosunu
  // birden süzer (pod/namespace/node adında büyük-küçük duyarsız
  // substring). URL kaynak-of-truth, replace:true.
  // v0.9.12 — ?service= süzgeci: servis sayfası pivotundan gelir,
  // zenginleştirilmiş service etiketi üzerinden süzer (korelasyon
  // audit'i §2.3 — pod listesi yerine kalıcı servis kimliği).
  const svcFilter = params.get('service') ?? '';
  const clearSvc = () => setParams(prev => {
    const next = new URLSearchParams(prev);
    next.delete('service');
    return next;
  }, { replace: true });

  const q = params.get('q') ?? '';
  const setQ = (v: string) => setParams(prev => {
    const next = new URLSearchParams(prev);
    if (v) next.set('q', v); else next.delete('q');
    return next;
  }, { replace: true });
  const qLower = q.trim().toLowerCase();

  const nsFilter = params.get('namespace') ?? '';
  const clearNs = () => setParams(prev => {
    const next = new URLSearchParams(prev);
    next.delete('namespace');
    next.delete('deployment'); // kademe: ns gidince deployment da gider
    return next;
  }, { replace: true });

  // v0.9.23 — ?deployment= süzgeci (kademe: Namespace → Deployment →
  // Pod). Pod üyeliği DeploymentRow.podNames'ten (gerçek eşleme).
  const depFilter = params.get('deployment') ?? '';
  const clearDep = () => setParams(prev => {
    const next = new URLSearchParams(prev);
    next.delete('deployment');
    return next;
  }, { replace: true });
  const depQ = useQuery({
    queryKey: ['cluster-deployments', clusterParam, nsFilter],
    queryFn: () => api.clusterDeployments(clusterParam, nsFilter),
    staleTime: 60_000,
    retry: 1,
    enabled: isDetail && nsFilter !== '',
    refetchInterval: liveMs,
  });
  const depRows = useMemo(() => {
    const all = depQ.data?.deployments ?? [];
    return qLower ? all.filter(r => r.deployment.toLowerCase().includes(qLower)) : all;
  }, [depQ.data, qLower]);
  const depPodSet = useMemo(() => {
    if (!depFilter) return null;
    const row = (depQ.data?.deployments ?? []).find(r => r.deployment === depFilter);
    return row ? new Set(row.podNames) : null;
  }, [depQ.data, depFilter]);

  // useQueries dizi kimliği her render değişir — memo'lar sabit-
  // boyutlu içerik anahtarına bağlı (v0.8.578 deseni).
  const podDatas = podQs.map(q => q.data);
  // v0.9.18 (self-review fix) — anahtar yalnız cluster:count idi:
  // aynı satır SAYISIYLA dönen refetch (olağan durum) yeni CPU/mem/
  // net/service değerlerini HİÇ render etmiyordu — tablo mount-anı
  // değerlerinde donuyordu. dataUpdatedAt her başarılı fetch'te
  // değişir → gerçek yenilemede memo koşar, render'lar arası sabit.
  const podDataKey = podQs.map(q =>
    (q.data ? `${q.data.cluster}:${q.data.count}:${q.dataUpdatedAt}` : '-')).join('|');
  const rows = useMemo(() => {
    let all = podDatas.flatMap(d => d?.pods ?? []);
    if (nsFilter) all = all.filter(r => r.namespace === nsFilter);
    if (depFilter && depPodSet) all = all.filter(r => depPodSet.has(r.pod));
    if (svcFilter) all = all.filter(r => r.service === svcFilter);
    if (qLower) all = all.filter(r =>
      r.pod.toLowerCase().includes(qLower) || r.namespace.toLowerCase().includes(qLower));
    return all;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [podDataKey, nsFilter, svcFilter, qLower, depFilter, depPodSet]);

  const nodeDatas = nodeQs.map(q => q.data);
  const nodeDataKey = nodeQs.map(q =>
    (q.data ? `${q.data.cluster}:${q.data.count}:${q.dataUpdatedAt}` : '-')).join('|');
  const nodeRows = useMemo(() => {
    const all = nodeDatas.flatMap(d => d?.nodes ?? []);
    return qLower ? all.filter(r => r.node.toLowerCase().includes(qLower)) : all;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [nodeDataKey, qLower]);

  const nsDatas = nsQs.map(q => q.data);
  const nsDataKey = nsQs.map(q =>
    (q.data ? `${q.data.cluster}:${q.data.count}:${q.dataUpdatedAt}` : '-')).join('|');
  const nsRows = useMemo(() => {
    const all = nsDatas.flatMap(d => d?.namespaces ?? []);
    return qLower ? all.filter(r => r.namespace.toLowerCase().includes(qLower)) : all;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [nsDataKey, qLower]);

  const dt = useDataTable<ClusterPodRow>({
    storageKey: 'clusterpods',
    columns: POD_COLS,
    rows,
    initialSort: { id: 'cpuCores', dir: 'desc' },
  });
  const nsdt = useDataTable<ClusterNamespaceRow>({
    storageKey: 'clusternamespaces',
    columns: NS_COLS,
    rows: nsRows,
    initialSort: { id: 'cpuCores', dir: 'desc' },
  });
  const depdt = useDataTable<ClusterDeploymentRow>({
    storageKey: 'clusterdeployments',
    columns: DEP_COLS,
    rows: depRows,
    initialSort: { id: 'cpuCores', dir: 'desc' },
  });
  const ndt = useDataTable<ClusterNodeRow>({
    storageKey: 'clusternodes',
    columns: NODE_COLS,
    rows: nodeRows,
    initialSort: { id: 'cpuPct', dir: 'desc' },
  });

  const podErr = podQs[0]?.isError ?? false;
  const nodeErr = nodeQs[0]?.isError ?? false;
  const nsErr = nsQs[0]?.isError ?? false;
  // Erişilemezlik (audit §5): summary VE aktif sekmenin sorgusu
  // birlikte düşerse gövde tek net Empty gösterir; tek taraf düşerse
  // sekme-içi mesajlar korunur.
  const activeErr = section === 'pods' ? podErr
    : section === 'nodes' ? nodeErr
    : section === 'namespaces' ? nsErr
    : false;
  const detailUnreachable = isDetail && detailSummaryQ.isError &&
    (section === 'overview' || activeErr);

  return (
    <>
      <Topbar title="Clusters" range={range} onRangeChange={setRange} />
      <div id="content">
        {sourcesQ.isPending && <Spinner />}
        {!sourcesQ.isPending && sources.length === 0 && (
          <Empty icon="◇" title="No remote clusters configured">
            Add Thanos Querier endpoints under{' '}
            <Link to="/settings/clusters">Settings → Remote clusters</Link>.
            Read-only; a viewer-role ServiceAccount token per cluster is enough.
          </Empty>
        )}

        {/* ── Genel görünüm: alert banner + cluster kartları ──── */}
        {!isDetail && sources.length > 0 && (() => {
          // v0.9.33 (F5) — fleet toplam firing-alert (banner).
          const fleetAlerts = summaryQs.reduce((n, q) =>
            n + (q?.data ? (q.data.alertsCritical ?? 0) + (q.data.alertsWarning ?? 0) : 0), 0);
          return (
            <>
              {fleetAlerts > 0 && (
                <div style={{
                  display: 'flex', alignItems: 'center', gap: 10, marginBottom: 14,
                  padding: '12px 16px', borderRadius: 6,
                  border: '1px solid color-mix(in srgb, var(--err) 40%, transparent)',
                  background: 'color-mix(in srgb, var(--err) 7%, transparent)',
                }}>
                  <span className="pulse-dot" style={{ width: 9, height: 9, background: 'var(--err)', flexShrink: 0 }} />
                  <span style={{ fontWeight: 600, fontSize: 13 }}>
                    {fmtNum(fleetAlerts)} alert{fleetAlerts === 1 ? '' : 's'} firing across the fleet
                  </span>
                  <span style={{ fontSize: 12, color: 'var(--text3)' }}>— open a cluster to triage</span>
                </div>
              )}
              <div style={{
                display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(300px, 1fr))', gap: 14,
              }}>
                {sources.map((name, i) => {
                  const q = summaryQs[i];
                  const sum: ClusterSummary | undefined = q?.data;
                  const unreachable = q?.isError ?? false;
                  const seen = observed.size === 0 || observed.has(name);
                  const alerts = sum ? (sum.alertsCritical ?? 0) + (sum.alertsWarning ?? 0) : 0;
                  const cpuP = sum ? safePct(sum.cpuUsedCores, sum.cpuCapacityCores) : null;
                  const memP = sum ? safePct(sum.memUsedBytes, sum.memCapacityBytes) : null;
                  // Status: unreachable > degraded (crit alert) > healthy.
                  const statusBadge = unreachable
                    ? <span className="badge b-err">unreachable</span>
                    : (sum?.alertsCritical ?? 0) > 0
                      ? <span className="badge b-warn">degraded</span>
                      : <span className="badge b-ok">healthy</span>;
                  return (
                    <Card key={name}
                      onClick={() => openCluster(name)}
                      style={{ cursor: 'pointer' }}
                      header={
                        <span style={{ display: 'flex', alignItems: 'center', gap: 8, justifyContent: 'space-between' }}>
                          <span style={{ fontFamily: 'ui-monospace, monospace' }}>{name}</span>
                          <span style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
                            {!unreachable && !seen && (
                              <span className="badge b-gray" title="Name not seen in the last 24h of telemetry — the service pivot will not match">not in telemetry</span>
                            )}
                            {statusBadge}
                          </span>
                        </span>
                      }>
                      {q?.isPending && <Spinner />}
                      {unreachable && (
                        <div style={{ fontSize: 12, color: 'var(--text3)' }}
                          title={q?.error instanceof Error ? q.error.message : undefined}>
                          Thanos Querier unreachable — check the token/route in Settings.
                        </div>
                      )}
                      {sum && (
                        <div style={{ display: 'grid', gap: 10 }}>
                          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr 1fr', gap: 6 }}>
                            {([['Nodes', sum.nodes], ['Pods', sum.pods], ['Alerts', alerts]] as const).map(([label, v]) => (
                              <div key={label}>
                                <div style={{ fontSize: 10, textTransform: 'uppercase', letterSpacing: '.06em', color: 'var(--text3)' }}>{label}</div>
                                <div className="mono" style={{ fontSize: 15, fontWeight: 700,
                                  color: label === 'Alerts' && (v ?? 0) > 0 ? 'var(--err)' : undefined }}>
                                  {v != null ? fmtNum(v) : '—'}
                                </div>
                              </div>
                            ))}
                          </div>
                          {(cpuP != null || memP != null) && (
                            <div style={{ display: 'grid', gap: 6 }}>
                              <div style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 11 }}>
                                <span style={{ width: 34, color: 'var(--text3)' }}>CPU</span>
                                <MiniBar pct={cpuP} />
                                <span className="mono" style={{ width: 34, textAlign: 'right', color: 'var(--text3)' }}>
                                  {cpuP != null ? `${Math.round(cpuP)}%` : '—'}</span>
                              </div>
                              <div style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 11 }}>
                                <span style={{ width: 34, color: 'var(--text3)' }}>Mem</span>
                                <MiniBar pct={memP} />
                                <span className="mono" style={{ width: 34, textAlign: 'right', color: 'var(--text3)' }}>
                                  {memP != null ? `${Math.round(memP)}%` : '—'}</span>
                              </div>
                            </div>
                          )}
                        </div>
                      )}
                    </Card>
                  );
                })}
              </div>
            </>
          );
        })()}

        {/* ── Detay: geri → Nodes → Pods ───────────────────────── */}
        {isDetail && (
          <>
            <div style={{ marginBottom: 12, display: 'flex', alignItems: 'center', gap: 10 }}>
              <button type="button" onClick={backToOverview}
                style={{ all: 'unset', cursor: 'pointer', color: 'var(--accent2)', fontSize: 12 }}>
                ← All clusters
              </button>
              <span style={{ fontFamily: 'ui-monospace, monospace', fontSize: 14, fontWeight: 600 }}>
                {clusterParam}
              </span>
              {detailSummaryQ.isError
                ? <span className="badge b-err">unreachable</span>
                : (observed.size > 0 && !observed.has(clusterParam))
                  ? <span className="badge b-warn" title="Name not seen in the last 24h of telemetry — the service pivot will not match">not in telemetry</span>
                  : <span className="badge b-ok">reachable</span>}
              {nsFilter && (
                <span className="badge b-info" style={{ cursor: 'pointer' }}
                  onClick={clearNs}
                  title="Namespace filter (service-page pivot) — click to clear">
                  namespace: {nsFilter} ✕
                </span>
              )}
              {depFilter && (
                <span className="badge b-info" style={{ cursor: 'pointer' }}
                  onClick={clearDep}
                  title="Workload filter — click to clear">
                  workload: {depFilter} ✕
                </span>
              )}
              {svcFilter && (
                <span className="badge b-info" style={{ cursor: 'pointer' }}
                  onClick={clearSvc}
                  title="Service filter (service-page pivot) — click to clear">
                  service: {svcFilter} ✕
                </span>
              )}
              <input value={q}
                onChange={e => setQ(e.target.value)}
                placeholder="Filter by name…"
                title="Filters nodes, namespaces and pods by name substring"
                style={{ marginLeft: 'auto', width: 220, padding: '4px 10px', fontSize: 12,
                         background: 'var(--bg)', color: 'var(--text)',
                         border: '1px solid var(--border)', borderRadius: 4 }} />
            </div>

            {/* v0.9.8 — sekme şeridi (OpenShift konsolu tarzı). Sayaçlar
                yalnız veri yüklendiyse — sayaç için önden fetch YOK. */}
            <div className="tab-strip" style={{ marginBottom: 12 }}>
              <button className={section === 'overview' ? 'active' : ''}
                onClick={() => setSection('overview')}>Overview</button>
              <button className={section === 'nodes' ? 'active' : ''}
                onClick={() => setSection('nodes')}>
                Nodes{section === 'nodes' && nodeRows.length > 0 ? ` (${nodeRows.length})` : ''}
              </button>
              <button className={section === 'namespaces' ? 'active' : ''}
                onClick={() => setSection('namespaces')}>
                Namespaces{section === 'namespaces' && nsRows.length > 0 ? ` (${nsRows.length})` : ''}
              </button>
              <button className={section === 'pods' ? 'active' : ''}
                onClick={() => setSection('pods')}>
                Pods{section === 'pods' && rows.length > 0 ? ` (${rows.length})` : ''}
              </button>
              {/* v0.9.34 (F3) — namespace typeahead, sağ hizalı. Seçim
                  ?namespace= yazıp Namespaces (deployments) görünümüne
                  geçer; native select DEĞİL. */}
              <div style={{ marginLeft: 'auto', display: 'flex', alignItems: 'center', gap: 8 }}>
                {/* v0.9.38 — auto-refresh toggle (design handoff §2):
                    açıkken yeşil pulse + "Live · 10s", kapalıyken gri
                    nokta + "Paused · Ns ago". Durum ?live=1 URL'de. */}
                <LiveToggle live={live} onToggle={toggleLive}
                  updatedAt={detailSummaryQ.dataUpdatedAt} />
                <NamespaceCombobox
                  namespaces={(nsQs[0]?.data?.namespaces ?? []).map(r => r.namespace)}
                  value={nsFilter}
                  onPick={ns => setSection('namespaces', p => { p.set('namespace', ns); p.delete('deployment'); })}
                  onClear={() => setSection('namespaces', p => { p.delete('namespace'); p.delete('deployment'); })} />
              </div>
            </div>

            {detailUnreachable ? (
              <Empty icon="✗" title={`${clusterParam} is unreachable`}>
                Thanos Querier did not respond — check token expiry/route in{' '}
                <Link to="/settings/clusters">Settings → Remote clusters</Link>{' '}
                entry.
              </Empty>
            ) : (
              <>
                {section === 'nodes' && <>
                  {nodeQs[0]?.isPending && <TableSkeleton cols={NODE_COLS.length} wideFirst />}
                  {nodeErr && (
                    <div style={{ fontSize: 12, color: 'var(--text3)' }}>
                      Node metrics unavailable (possibly the tenancy port — see the runbook probe step).
                    </div>
                  )}
                  {!nodeErr && !nodeQs[0]?.isPending && nodeRows.length === 0 && (
                    <div style={{ fontSize: 12, color: 'var(--text3)' }}>
                      node-exporter series came back empty — see the runbook probe step.
                    </div>
                  )}
                  {nodeRows.length > 0 && (
                    <div className="table-wrap">
                      <table style={{ tableLayout: 'fixed', width: '100%' }}>
                        <DataTableColgroup dt={ndt} />
                        <DataTableHead dt={ndt} />
                        <tbody>
                          {ndt.sortedRows.map(r => (
                            <tr key={`${r.cluster}|${r.node}`}
                              style={{ contentVisibility: 'auto', containIntrinsicSize: 'auto 36px' }}>
                              <td style={{ fontSize: 11, color: 'var(--text2)' }}>{r.cluster}</td>
                              <td>
                                <span style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12, fontWeight: 500 }}
                                  title={r.node}>
                                  {r.node}
                                </span>
                              </td>
                              <td>{r.role
                                ? <span className={`badge ${r.role === 'master' || r.role === 'control-plane' ? 'b-info' : 'b-gray'}`}>{r.role}</span>
                                : <span style={{ color: 'var(--text3)' }}>—</span>}</td>
                              <td className="num mono">{fmtCores(r.cpuCores)}</td>
                              <td className="num mono" style={{
                                color: (r.cpuPct ?? 0) > 85 ? 'var(--err)' : (r.cpuPct ?? 0) > 60 ? 'var(--warn)' : 'var(--text3)',
                              }}>{r.cpuPct ? r.cpuPct.toFixed(0) : '—'}</td>
                              <td className="num mono">{fmtBytes(r.memBytes)}</td>
                              <td className="num mono" style={{
                                color: (r.memPct ?? 0) > 85 ? 'var(--err)' : (r.memPct ?? 0) > 60 ? 'var(--warn)' : 'var(--text3)',
                              }}>{r.memPct ? r.memPct.toFixed(0) : '—'}</td>
                              <td className="num mono">{r.netInBps ? fmtBps(r.netInBps) : '—'}</td>
                              <td className="num mono">{r.netOutBps ? fmtBps(r.netOutBps) : '—'}</td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    </div>
                  )}
                </>}

                {/* v0.8.588 — namespace rollup: TAM toplamlar (pod
                    topk kesmesinden bağımsız); satır tıklaması filtre +
                    Pods sekmesine geçiş (v0.9.8, audit §2). */}
                {section === 'namespaces' && nsFilter && <>
                  {/* v0.9.23 — Namespace → Deployment ara kademesi:
                      çip nsFilter'ı temizleyip ns listesine döndürür. */}
                  {depQ.isPending && <TableSkeleton cols={DEP_COLS.length} wideFirst />}
                  {depQ.isError && (
                    <div style={{ fontSize: 12, color: 'var(--text3)' }}>
                      Workload rollup unavailable — check the cluster entry in Settings.
                    </div>
                  )}
                  {!depQ.isPending && !depQ.isError && depRows.length === 0 && (
                    <div style={{ fontSize: 12, color: 'var(--text3)' }}>
                      No workload samples in this namespace.
                    </div>
                  )}
                  {depRows.length > 0 && (
                    <div className="table-wrap">
                      <table style={{ tableLayout: 'fixed', width: '100%' }}>
                        <DataTableColgroup dt={depdt} />
                        <DataTableHead dt={depdt} />
                        <tbody>
                          {depdt.sortedRows.map(r => (
                            <tr key={r.deployment}
                              className={r.deployment === depFilter ? 'row-selected' : undefined}
                              onClick={() => setSection('pods', p => p.set('deployment', r.deployment))}
                              title="Open the pod list filtered to this workload"
                              style={{ cursor: 'pointer' }}>
                              <td className="mono" style={{ fontSize: 12 }}>{r.deployment}</td>
                              {/* v0.9.39 — ready/desired rozeti + statü; KSM
                                  yoksa '—'. v0.9.42: surge'de (ready>desired,
                                  backend Available der) rozet de yeşil —
                                  strict eşitlik aynı satırda sarı 4/3 + yeşil
                                  Available çelişkisi üretiyordu. */}
                              <td>{r.status
                                ? <span className={`badge ${r.readyReplicas >= r.desiredReplicas ? 'b-ok' : 'b-warn'}`}>
                                    {r.readyReplicas}/{r.desiredReplicas}
                                  </span>
                                : <span style={{ color: 'var(--text3)' }}>—</span>}</td>
                              <td>{r.status
                                ? <span className={`badge ${depStatusBadge(r.status)}`}>{r.status}</span>
                                : <span style={{ color: 'var(--text3)' }}>—</span>}</td>
                              <td className="num mono">{fmtNum(r.pods)}</td>
                              <td className="num mono">{fmtCores(r.cpuCores)}</td>
                              <td className="num mono">{fmtBytes(r.memBytes)}</td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    </div>
                  )}
                </>}
                {section === 'namespaces' && !nsFilter && <>
                  {nsRows.length === 0 && (
                    <div style={{ fontSize: 12, color: 'var(--text3)' }}>
                      {nsQs[0]?.isPending ? 'Loading…' : nsErr ? 'Namespace rollup unavailable — check the cluster entry in Settings.' : 'No namespace samples.'}
                    </div>
                  )}
                  {nsRows.length > 0 && (
                    <div className="table-wrap">
                      <table style={{ tableLayout: 'fixed', width: '100%' }}>
                        <DataTableColgroup dt={nsdt} />
                        <DataTableHead dt={nsdt} />
                        <tbody>
                          {nsdt.sortedRows.map(r => {
                            const selected = r.namespace === nsFilter;
                            return (
                              <tr key={r.namespace}
                                className={selected ? 'row-selected' : undefined}
                                onClick={() => setSection('namespaces', p => {
                                  // v0.9.23 — ara kademe: seçim iş yükü
                                  // rollup'unu açar (pods'a atlamaz);
                                  // deployment seçimi pods'a götürür.
                                  if (selected) { p.delete('namespace'); p.delete('deployment'); }
                                  else { p.set('namespace', r.namespace); p.delete('deployment'); }
                                })}
                                title={selected
                                  ? 'Clear the namespace selection'
                                  : 'Show workloads in this namespace'}
                                style={{ cursor: 'pointer' }}>
                                <td className="mono" style={{ fontSize: 12 }}>{r.namespace}</td>
                                <td className="num mono">{r.pods ? fmtNum(r.pods) : '—'}</td>
                                <td className="num mono">{fmtCores(r.cpuCores)}</td>
                                <td className="num mono">{fmtBytes(r.memBytes)}</td>
                                {/* v0.9.37 (B4/F6) — restart toplamı + health. */}
                                <td className="num mono" style={{ color: restartColor(r.restarts ?? 0) }}>
                                  {r.restarts != null ? fmtNum(r.restarts) : '—'}</td>
                                <td className="num" onClick={e => e.stopPropagation()}>
                                  {(r.failing ?? 0) > 0
                                    ? <span className="badge b-err">{r.failing} failing</span>
                                    : <span className="badge b-ok">healthy</span>}
                                </td>
                                <td style={{ textAlign: 'center' }}>
                                  {/* v0.9.5 — trend drawer'ı; satırın filtre
                                      davranışına karışmaz (stopPropagation). */}
                                  <button type="button"
                                    onClick={e => { e.stopPropagation(); openNsDrawer(r); }}
                                    title="Per-pod trend charts for this namespace"
                                    style={{ all: 'unset', cursor: 'pointer', color: 'var(--accent2)', display: 'inline-flex' }}>
                                    <ChartSpline size={14} strokeWidth={1.75} />
                                  </button>
                                </td>
                              </tr>
                            );
                          })}
                        </tbody>
                      </table>
                    </div>
                  )}
                </>}

                {section === 'pods' && <>
                  {podQs[0]?.isPending && <TableSkeleton cols={POD_COLS.length} wideFirst />}
                  {podErr && (
                    <div style={{ fontSize: 12, color: 'var(--text3)' }}>
                      Pod metrics unavailable — check the cluster entry in Settings.
                    </div>
                  )}
                  {!podErr && !podQs[0]?.isPending && rows.length === 0 && (
                    <div style={{ fontSize: 12, color: 'var(--text3)' }}>
                      {depFilter
                        ? `No pods in workload "${depFilter}" — clear the chip to see the namespace.`
                        : svcFilter
                        ? `No pods matched service "${svcFilter}" — the label comes from Coremetry telemetry matching (best-effort); clear the chip to see all pods.`
                        : nsFilter
                          ? `No pod samples in namespace "${nsFilter}" — clear the chip to see the whole cluster.`
                          : 'Queries returned no series — check the namespace filter on the cluster entry.'}
                    </div>
                  )}
                  {rows.length > 0 && (
                    <div className="table-wrap">
                      <table style={{ tableLayout: 'fixed', width: '100%' }}>
                        <DataTableColgroup dt={dt} />
                        <DataTableHead dt={dt} />
                        <tbody>
                          {dt.sortedRows.map(r => (
                            <tr key={`${r.cluster}|${r.namespace}|${r.pod}`}
                              onClick={() => openPod(r)}
                              style={{
                                cursor: 'pointer',
                                contentVisibility: 'auto',
                                containIntrinsicSize: 'auto 36px',
                              }}>
                              <td style={{ fontSize: 11, color: 'var(--text2)' }}>{r.cluster}</td>
                              <td style={{ fontSize: 11, color: 'var(--text2)' }}>{r.namespace}</td>
                              <td>
                                <span style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12, fontWeight: 500 }}
                                  title={r.pod}>
                                  {r.pod}
                                </span>
                              </td>
                              <td onClick={e => e.stopPropagation()}>
                                {r.service ? (
                                  <Link to={`/service?name=${encodeURIComponent(r.service)}`}
                                    style={{ fontSize: 11 }}>{r.service}</Link>
                                ) : (
                                  <span style={{ fontSize: 11, color: 'var(--text3)' }}
                                    title="No matching Coremetry service (uninstrumented, infra pod, or ambiguous)">—</span>
                                )}
                              </td>
                              {/* v0.9.37 (B4/F6) — Status: kube_pod_status_phase. */}
                              <td>{r.phase
                                ? <span className={`badge ${podPhaseBadge(r.phase)}`}>{r.phase}</span>
                                : <span style={{ color: 'var(--text3)' }}>—</span>}</td>
                              <td className="num mono">{fmtCores(r.cpuCores)}</td>
                              {/* v0.8.580 — % hücresi limit-bazlı; request
                                  ekseni title'da (clamp'siz, aşım sinyal). */}
                              <td className="num mono" style={{
                                color: (r.cpuPct ?? 0) > 85 ? 'var(--err)' : (r.cpuPct ?? 0) > 60 ? 'var(--warn)' : 'var(--text3)',
                              }} title={pctTitle('CPU', r.cpuPct, r.cpuPctOfReq)}>
                                {r.cpuPct ? r.cpuPct.toFixed(0) : '—'}</td>
                              <td className="num mono">{fmtBytes(r.memBytes)}</td>
                              <td className="num mono" style={{
                                color: (r.memPct ?? 0) > 85 ? 'var(--err)' : (r.memPct ?? 0) > 60 ? 'var(--warn)' : 'var(--text3)',
                              }} title={pctTitle('Memory', r.memPct, r.memPctOfReq)}>
                                {r.memPct ? r.memPct.toFixed(0) : '—'}</td>
                              <td className="num mono">{r.netInBps ? fmtBps(r.netInBps) : '—'}</td>
                              <td className="num mono">{r.netOutBps ? fmtBps(r.netOutBps) : '—'}</td>
                              <td className="num mono" style={{ color: restartColor(r.restarts ?? 0) }}>
                                {r.restarts != null ? fmtNum(r.restarts) : '—'}</td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    </div>
                  )}
                </>}

                {/* v0.9.8 — Overview sekmesi: 2 skaler kart (CPU/Mem;
                    Net kartları + throughput grafiği L2/L3 probe
                    sonrası — alan yokluğu yanlış sıfır okutmaz). */}
                {section === 'overview' && (() => {
                  // v0.9.41 (handoff §3 KPI polish) — 26/700 değer
                  // tipografisi, kapasiteli alt yazılar, Running pods
                  // + Active alerts kartları. Best-effort: kapasite/faz
                  // yoksa alt yazı ve kart görünmez-düşer; alerts kartı
                  // yalnız ALERTS ailesi cevap verdiyse (alertsQ.data).
                  const d = detailSummaryQ.data;
                  const kpiVal = { fontSize: 26, fontWeight: 700 } as const;
                  const kpiSub = { fontSize: 11, color: 'var(--text3)', marginTop: 4 } as const;
                  const phaseTotal = (d?.podsRunning ?? 0) + (d?.podsPending ?? 0) + (d?.podsFailed ?? 0);
                  const alertCrit = d?.alertsCritical ?? 0;
                  const alertWarn = d?.alertsWarning ?? 0;
                  const showAlerts = alertsQ.data != null || alertCrit + alertWarn > 0;
                  return (
                  <div style={{
                    display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(220px, 1fr))',
                    gap: 14,
                  }}>
                    <Card density="tight" header="CPU used (cores)">
                      <div className="mono" style={kpiVal}>
                        {d?.cpuUsedCores ? fmtCores(d.cpuUsedCores) : '—'}
                      </div>
                      <div style={kpiSub}>
                        {[
                          d?.cpuCapacityCores ? `of ${fmtCores(d.cpuCapacityCores)} cores` : '',
                          d?.nodes ? `${fmtNum(d.nodes)} nodes` : '',
                        ].filter(Boolean).join(' · ')}
                      </div>
                    </Card>
                    <Card density="tight" header="Memory used">
                      <div className="mono" style={kpiVal}>
                        {d?.memUsedBytes ? fmtBytes(d.memUsedBytes) : '—'}
                      </div>
                      <div style={kpiSub}>
                        {d?.memCapacityBytes ? `of ${fmtBytes(d.memCapacityBytes)}`
                          : d?.pods ? `${fmtNum(d.pods)} pods` : ''}
                      </div>
                    </Card>
                    {phaseTotal > 0 && (
                      <Card density="tight" header="Running pods">
                        <div className="mono" style={{ ...kpiVal, color: 'var(--ok)' }}>
                          {fmtNum(d?.podsRunning ?? 0)}
                        </div>
                        <div style={kpiSub}>
                          {`${fmtNum(d?.podsPending ?? 0)} pending · ${fmtNum(d?.podsFailed ?? 0)} failed`}
                        </div>
                      </Card>
                    )}
                    {/* v0.9.44 — yeşil 0'ın sınırı title'da: ALERTS
                        serisi yalnız firing/pending alarm varken var
                        olur; kural tanımsız cluster ile alarmsız
                        sağlıklı cluster ayırt EDİLEMEZ. */}
                    {showAlerts && (
                      <Card density="tight" header="Active alerts"
                        title="Counts firing/pending ALERTS series from Prometheus. 0 also appears when the cluster has no alerting rules configured — the ALERTS metric cannot distinguish the two.">
                        <div className="mono" style={{
                          ...kpiVal,
                          color: alertCrit > 0 ? 'var(--err)' : alertWarn > 0 ? 'var(--warn)' : 'var(--ok)',
                        }}>
                          {fmtNum(alertCrit + alertWarn)}
                        </div>
                        <div style={kpiSub}>
                          {`${fmtNum(alertCrit)} critical · ${fmtNum(alertWarn)} warning`}
                        </div>
                      </Card>
                    )}
                    {/* v0.9.10 — net kartları yalnız veri VARSA (alan
                        yokluğu yanlış sıfır okutmaz — probe duruşu). */}
                    {(d?.netInBps ?? 0) > 0 && (
                      <Card density="tight" header="Net in">
                        <div className="mono" style={kpiVal}>
                          {fmtBps(d!.netInBps!)}
                        </div>
                      </Card>
                    )}
                    {(d?.netOutBps ?? 0) > 0 && (
                      <Card density="tight" header="Net out">
                        <div className="mono" style={kpiVal}>
                          {fmtBps(d!.netOutBps!)}
                        </div>
                      </Card>
                    )}
                  </div>
                  );
                })()}
                {/* v0.9.31 (design handoff F1) — Utilization gauges (2fr)
                    + Pod phase donut (1fr). Gauge'lar kapasite VARSA
                    (safePct null→gizli); donut herhangi bir faz sayısı
                    varsa. Kısmi veri kısmi görsel — best-effort. */}
                {section === 'overview' && detailSummaryQ.data && (() => {
                  const d = detailSummaryQ.data;
                  const cpuP = safePct(d.cpuUsedCores, d.cpuCapacityCores);
                  const memP = safePct(d.memUsedBytes, d.memCapacityBytes);
                  const phaseTotal = (d.podsRunning ?? 0) + (d.podsPending ?? 0) + (d.podsFailed ?? 0);
                  const healthP = phaseTotal > 0 ? ((d.podsRunning ?? 0) / phaseTotal) * 100 : null;
                  const hasGauges = cpuP != null || memP != null || healthP != null;
                  const hasDonut = phaseTotal > 0;
                  if (!hasGauges && !hasDonut) return null;
                  return (
                    <div style={{ display: 'grid', gridTemplateColumns: hasGauges && hasDonut ? '2fr 1fr' : '1fr', gap: 14, marginTop: 14 }}>
                      {hasGauges && (
                        <Card header="Utilization">
                          <div style={{ display: 'flex', justifyContent: 'space-around', flexWrap: 'wrap', gap: 16, paddingTop: 6 }}>
                            <Gauge pct={cpuP} label="CPU" sub="utilized" />
                            <Gauge pct={memP} label="Memory" sub="utilized" />
                            <Gauge pct={healthP} label="Pod health" sub="running"
                              color={healthP != null && healthP > 95 ? 'var(--ok)' : 'var(--warn)'} />
                          </div>
                        </Card>
                      )}
                      {hasDonut && (
                        <Card header="Pod phase">
                          <div style={{ paddingTop: 6 }}>
                            <PhaseDonut running={d.podsRunning ?? 0} pending={d.podsPending ?? 0} failed={d.podsFailed ?? 0} />
                          </div>
                        </Card>
                      )}
                    </div>
                  );
                })()}
                {/* v0.9.35 (B2/F4) — CPU + Mem area, Total/By-node
                    toggle; seri yoksa kart gizli. v0.9.49: MetricArea
                    ortak bileşeni (§8 Service sekmesiyle paylaşılır). */}
                {section === 'overview' && ((cpuTrendQ.data?.series?.length ?? 0) > 0 || (memTrendQ.data?.series?.length ?? 0) > 0) && (
                  <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 14, marginTop: 14 }}>
                    {/* v0.9.58 — drag-seçim global range'e yazar
                        (Service.tsx emsali; sayfa range'i zaten
                        useUrlRange'ten geliyor). */}
                    {/* Madde 4 sweep — ortak 'clusters' crosshair grubu +
                        çift-tık zoom geri-yığını (chartZoomReset). */}
                    <MetricArea title="CPU usage (cores)" byLabel="By node"
                      by={cpuByNode} onToggle={setCpuByNode} onZoom={chartZoom} onZoomReset={chartZoomReset}
                      syncKey="clusters"
                      series={cpuTrendQ.data?.series} seriesName="CPU" />
                    <MetricArea title="Memory usage" byLabel="By node"
                      by={memByNode} onToggle={setMemByNode} onZoom={chartZoom} onZoomReset={chartZoomReset}
                      syncKey="clusters"
                      series={memTrendQ.data?.series} seriesName="Memory" unit="bytes" />
                  </div>
                )}
                {/* v0.9.10 — throughput grafiği: yalnız seri geldiyse
                    (node_network erişilemezse bölüm hiç görünmez). */}
                {section === 'overview' && (netTrendQ.data?.trend?.length ?? 0) > 0 && (
                  <Card header={`Network throughput${netClamped ? ' (last 6h)' : ''}`} style={{ marginTop: 14 }}>
                    {/* Madde 4 sweep — eksen/tooltip byte birimi (değerler
                        Bps; seri etiketleri "(B/s)" taşır) + drag-zoom
                        global range'e, çift-tık geri-yığınına. */}
                    <MultiLineChart
                      series={netTrendToSeries(netTrendQ.data!.trend!)}
                      unit="bytes" onZoom={chartZoom} onZoomReset={chartZoomReset}
                      height={200} />
                  </Card>
                )}
                {/* v0.9.32 (F2) — node utilization heatmap (Overview);
                    aynı clusterNodes verisi, seri yoksa gizli. */}
                {section === 'overview' && nodeRows.length > 0 && (
                  <Card header={`Node utilization (${nodeRows.length})`} style={{ marginTop: 14 }}>
                    <NodeHeatmap nodes={nodeRows} />
                  </Card>
                )}
                {/* v0.9.36 (B3/F4) — firing alerts (1/2) + PromQL (1/2). */}
                {section === 'overview' && (
                  <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 14, marginTop: 14 }}>
                    {(alertsQ.data?.alerts?.length ?? 0) > 0 && (
                      <AlertsPanel alerts={alertsQ.data!.alerts!}
                        criticalOnly={alertsCriticalOnly} onToggle={setAlertsCriticalOnly} />
                    )}
                    <ClusterPromQLCard cluster={clusterParam} />
                  </div>
                )}
              </>
            )}
          </>
        )}

        {nsDrawerParam && (() => {
          const [c, ns] = nsDrawerParam.split('|');
          if (!c || !ns) return null;
          return <NamespaceDrawer cluster={c} namespace={ns} range={range} onClose={closeNsDrawer}
            onZoom={chartZoom} onZoomReset={chartZoomReset} />;
        })()}
      </div>
    </>
  );
}

// NamespaceDrawer — namespace'in pod başına trend grafikleri
// (v0.9.5, trend-upgrade audit T3): PodDrawer'ın aynası, panel
// multi-pod modda (top-10 seri + "Top N of M" etiketi). Yalnız
// açılınca fetch; ?tw= pencere seçicisi pod drawer'ıyla ortak.
function NamespaceDrawer({ cluster, namespace, range, onClose, onZoom, onZoomReset }: {
  cluster: string;
  namespace: string;
  range: TimeRange;
  onClose: () => void;
  // Madde 4 sweep — trend drag-zoom sayfa range'ine, çift-tık geri-yığınına.
  // v0.9.206 review-fix: opts.clearTw → ?tw= silme range ile aynı yazımda.
  onZoom?: (fromUnixSec: number, toUnixSec: number, opts?: { clearTw?: boolean }) => void;
  onZoomReset?: () => void;
}) {
  const [params, setParams] = useSearchParams();
  const tw = params.get('tw') ?? '';
  const setTw = (v: string) => setParams(prev => {
    const next = new URLSearchParams(prev);
    if (v) next.set('tw', v); else next.delete('tw');
    return next;
  }, { replace: true });
  const { from, to } = useMemo(() => {
    const w = TREND_WINDOWS.find(x => x.key === tw);
    if (w && 'ns' in w && w.ns) {
      const now = Date.now() * 1e6;
      return { from: now - w.ns, to: now };
    }
    return timeRangeToNs(range);
  }, [range, tw]);

  return (
    <Drawer onClose={onClose} header={
      <>
        <span style={{ fontFamily: 'ui-monospace, monospace', fontSize: 14, fontWeight: 600 }}>
          {namespace}
        </span>
        <span className="badge b-gray" title="cluster">{cluster}</span>
      </>
    }>
      <DrawerSection title="Per-pod trend (per minute)">
        <div style={{ marginBottom: 8 }}>
          <select value={tw} onChange={e => setTw(e.target.value)}
            style={{ fontSize: 11 }}
            title="Trend window — independent of the page range">
            {TREND_WINDOWS.map(w => (
              <option key={w.key} value={w.key}>{w.label}</option>
            ))}
          </select>
        </div>
        {/* Madde 4 sweep — drawer'da zoom sayfa range'ine yazar; ?tw= yerel
            penceresi zoom'un görünmesini engellemesin diye '' (Page range)'e
            döndürülür. v0.9.206 review-fix: silme ayrı setTw('') çağrısı
            DEĞİL, range ile aynı yazım (opts.clearTw) — aynı-tick iki
            setParams'ta son yazan kazanıp tw'yi bırakıyor, drawer grafiği
            yeniden pencerelenmiyordu. Çift-tık da simetrik: tw aktifken
            sayfa yığınını görünmez poplamak yerine yerel pencereyi Page
            range'e döndürür; yığın ancak tw=''ken yürür. */}
        <ThanosTrendPanel cluster={cluster} namespace={namespace}
          fromNs={from} toNs={to}
          onZoom={onZoom ? (f, t) => onZoom(f, t, { clearTw: true }) : undefined}
          onZoomReset={onZoomReset ? () => { if (tw) { setTw(''); return; } onZoomReset(); } : undefined} />
      </DrawerSection>
    </Drawer>
  );
}

// PodDrawer + TREND_WINDOWS v0.9.51'de pages/clusters/PodDrawer.tsx'e
// taşındı (§8 Service→Infrastructure sekmesiyle ortak).


// ResToggleHeader v0.9.49'da MetricArea'ya taşındı
// (pages/clusters/MetricArea.tsx) — §8 Service sekmesiyle ortak.

// fmtAgeShort — saniye → "Nm"/"Nh"/"Nd" (design handoff alert "Nm ago").
// LiveToggle — auto-refresh anahtarı (v0.9.38, design handoff §2).
// Açıkken aktif sorgular 10s refetchInterval alır (TanStack gizli
// sekmede kendiliğinden durdurur); kapalıyken temsilci sorgunun
// (summary) yaşı gösterilir. "ago" tazelemesi 10s'lik YEREL bir
// re-render tick'idir, sunucuya istek atmaz.
function LiveToggle({ live, onToggle, updatedAt }: {
  live: boolean; onToggle: () => void; updatedAt: number;
}) {
  const [, setTick] = useState(0);
  useEffect(() => {
    if (live || !updatedAt) return;
    const id = setInterval(() => {
      if (document.hidden) return; // gizli sekmede boşuna render yok
      setTick(t => t + 1);
    }, 10_000);
    return () => clearInterval(id);
  }, [live, updatedAt]);
  const agoSec = updatedAt ? Math.max(0, Math.round((Date.now() - updatedAt) / 1000)) : 0;
  const ago = agoSec < 90 ? `${agoSec}s` : fmtAgeShort(agoSec);
  // v0.9.43 — "Live · 10s" vaadi abartıyordu: sunucu cache TTL'i 60s,
  // efektif veri tazeliği ~60s. Etiket "Live", ayrıntı title'da.
  const label = live ? 'Live'
    : updatedAt ? `Paused · ${ago} ago` : 'Paused';
  return (
    <Button variant="secondary" size="sm" onClick={onToggle}
      title={live
        ? 'Auto-refresh on — polls every 10s; data freshness follows the 60s server cache. Click to pause.'
        : 'Auto-refresh off — click to go live'}
      style={{ whiteSpace: 'nowrap' }}
      leftIcon={<span className={live ? 'pulse-dot' : ''} style={{
        display: 'inline-block', width: 8, height: 8, borderRadius: '50%',
        background: live ? 'var(--ok)' : 'var(--text3)',
      }} />}>
      {label}
    </Button>
  );
}

function fmtAgeShort(sec?: number): string {
  if (!sec || sec <= 0) return '';
  if (sec < 3600) return `${Math.round(sec / 60)}m`;
  if (sec < 86400) return `${Math.round(sec / 3600)}h`;
  return `${Math.round(sec / 86400)}d`;
}

// AlertsPanel — firing alerts (v0.9.36, design handoff). Kritik-önce
// (backend sıralı), severity badge + alertname + ns/pod + yaş;
// criticalOnly flag filtreler. Count badge başlıkta.
function AlertsPanel({ alerts, criticalOnly, onToggle }: {
  alerts: ClusterAlertRow[];
  criticalOnly: boolean;
  onToggle: (v: boolean) => void;
}) {
  const shown = criticalOnly ? alerts.filter(a => a.severity === 'critical') : alerts;
  return (
    <Card header={
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 8 }}>
        <span>Firing alerts <span className="badge b-err" style={{ marginLeft: 4 }}>{alerts.length}</span></span>
        <label style={{ fontSize: 11, color: 'var(--text3)', display: 'inline-flex', alignItems: 'center', gap: 5, cursor: 'pointer', fontWeight: 400 }}>
          <input type="checkbox" checked={criticalOnly} onChange={e => onToggle(e.target.checked)} />
          critical only
        </label>
      </div>
    }>
      <div style={{ display: 'grid', gap: 2, maxHeight: 320, overflowY: 'auto' }}>
        {shown.map((a, i) => {
          const age = fmtAgeShort(a.ageSec);
          return (
            <div key={`${a.alertName}|${a.namespace}|${a.pod}|${i}`}
              style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '5px 6px', borderRadius: 3 }}
              onMouseEnter={e => (e.currentTarget.style.background = 'var(--bg2)')}
              onMouseLeave={e => (e.currentTarget.style.background = 'transparent')}>
              <span className={`badge ${a.severity === 'critical' ? 'b-err' : 'b-warn'}`}>{a.severity}</span>
              <span style={{ fontWeight: 600, fontSize: 12 }}>{a.alertName}</span>
              {(a.namespace || a.pod) && (
                <span className="mono" style={{ fontSize: 11, color: 'var(--text3)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                  {a.namespace}{a.pod ? `/${a.pod}` : ''}
                </span>
              )}
              {age && <span style={{ marginLeft: 'auto', fontSize: 11, color: 'var(--text3)', flexShrink: 0 }}>{age} ago</span>}
            </div>
          );
        })}
        {shown.length === 0 && (
          <div style={{ fontSize: 12, color: 'var(--text3)', padding: 6 }}>No critical alerts.</div>
        )}
      </div>
    </Card>
  );
}

// ClusterPromQLCard — README §Overview PromQL bloğu (display-only,
// $c yerine cluster adı). Operatörün canlıda çalıştırabileceği
// referans sorgular.
function ClusterPromQLCard({ cluster }: { cluster: string }) {
  const c = promQuote(cluster); // v0.9.44 — operatör serbest metni, kaçışla
  const queries: [string, string][] = [
    ['CPU by namespace', `sum by (namespace) (rate(container_cpu_usage_seconds_total{cluster="${c}"}[5m]))`],
    ['Working-set memory', `sum by (namespace) (container_memory_working_set_bytes{cluster="${c}"})`],
    ['Pod phase count', `count by (phase) (kube_pod_status_phase{cluster="${c}"} == 1)`],
    ['Restarts (1h)', `sum by (pod) (increase(kube_pod_container_status_restarts_total{cluster="${c}"}[1h]))`],
  ];
  return (
    <Card header="Prometheus / Thanos queries">
      <PromQLList queries={queries} />
    </Card>
  );
}

// PromQLList v0.9.51'de pages/clusters/PromQLList.tsx'e taşındı.

// podPhaseBadge — kube_pod_status_phase → badge sınıfı (v0.9.37).
// depStatusBadge — Deployment statü rozeti (v0.9.39, handoff §5):
// Available yeşil, Progressing sarı (rollout sürüyor), Degraded kırmızı.
function depStatusBadge(status: string): string {
  if (status === 'Available') return 'b-ok';
  if (status === 'Progressing') return 'b-warn';
  return 'b-err';
}

// podPhaseBadge v0.9.51'de thresholds.ts'e taşındı.
