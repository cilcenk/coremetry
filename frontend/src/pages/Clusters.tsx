import { useMemo } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { useQuery, useQueries } from '@tanstack/react-query';
import { ChartSpline } from 'lucide-react';
import { ThanosTrendPanel } from '@/pages/clusters/TrendPanel';
import { netTrendToSeries } from '@/pages/clusters/trendSeries';
import { MultiLineChart } from '@/components/MultiLineChart';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { TableSkeleton } from '@/components/Skeleton';
import { Card, Drawer, DrawerSection } from '@/components/ui';
import { api } from '@/lib/api';
import { useClusters } from '@/lib/queries';
import { timeRangeToNs, fmtBytes, fmtNum } from '@/lib/utils';
import { useUrlRange } from '@/lib/useUrlRange';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import type { ClusterPodRow, ClusterNodeRow, ClusterNamespaceRow, ClusterSummary, TimeRange } from '@/lib/types';

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
  { id: 'trend',     label: '',          width: 44 },
];

const POD_COLS: DataTableColumn<ClusterPodRow>[] = [
  { id: 'cluster',   label: 'Cluster',   sortValue: r => r.cluster,   naturalDir: 'asc', width: 130 },
  { id: 'namespace', label: 'Namespace', sortValue: r => r.namespace, naturalDir: 'asc', width: 160 },
  { id: 'pod',       label: 'Pod',       sortValue: r => r.pod,       naturalDir: 'asc', width: 260 },
  { id: 'cpuCores',  label: 'CPU',       sortValue: r => r.cpuCores,  numeric: true, width: 90 },
  { id: 'cpuPct',    label: 'CPU %',     sortValue: r => r.cpuPct ?? 0, numeric: true, width: 80 },
  { id: 'memBytes',  label: 'Memory',    sortValue: r => r.memBytes,  numeric: true, width: 100 },
  { id: 'memPct',    label: 'Mem %',     sortValue: r => r.memPct ?? 0, numeric: true, width: 80 },
  // v0.9.10 — network (best-effort).
  { id: 'netIn',     label: 'Net in',    sortValue: r => r.netInBps ?? 0, numeric: true, width: 90 },
  { id: 'netOut',    label: 'Net out',   sortValue: r => r.netOutBps ?? 0, numeric: true, width: 90 },
];

// fmtCores — 0.003 → "3m" (millicore okunuşu), 1.25 → "1.25".
function fmtCores(v: number): string {
  if (v < 0.01) return `${Math.round(v * 1000)}m`;
  if (v < 1) return `${(v * 1000).toFixed(0)}m`;
  return v.toFixed(2);
}

// fmtBps — ağ hızı: fmtBytes + '/s' (0 = bilinmiyor → çağıran '—' basar).
function fmtBps(v: number): string {
  return `${fmtBytes(v)}/s`;
}

// pctTitle — % hücresinin iki eksenli tooltip'i (v0.8.580): limit
// ekseni (throttle/OOM) hücrede, request ekseni (provisioning
// isabeti) title'da. Eksik eksen "bilinmiyor" okunur.
function pctTitle(what: string, ofLimit?: number, ofReq?: number): string {
  const lim = ofLimit ? `${ofLimit.toFixed(0)}% of limit` : 'limit unknown';
  const req = ofReq ? `${ofReq.toFixed(0)}% of request` : 'request unknown';
  return `${what}: ${lim} · ${req}`;
}

export default function ClustersPage() {
  const [range, setRange] = useUrlRange('15m'); // yalnız drawer trendi
  const [params, setParams] = useSearchParams();

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
    next.delete('tab');
    return next;
  }, { replace: true });
  const backToOverview = () => setParams(prev => {
    const next = new URLSearchParams(prev);
    next.delete('cluster');
    next.delete('namespace');
    next.delete('pod');
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

  // ?pod=<cluster>|<namespace>|<pod> — drawer kimliği.
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
    return nsFilterEarly ? 'pods' : 'overview';
  })();
  const setSection = (sec: string, extra?: (p: URLSearchParams) => void) =>
    setParams(prev => {
      const next = new URLSearchParams(prev);
      if (sec === 'overview') next.delete('section'); else next.set('section', sec);
      next.delete('tab');
      extra?.(next);
      return next;
    }, { replace: true });

  // Detay başlık rozeti + Overview kartları: aynı skaler summary
  // (genel görünümle queryKey paylaşır — cache ortak).
  const detailSummaryQ = useQuery({
    queryKey: ['cluster-summary', clusterParam],
    queryFn: () => api.clusterSummary(clusterParam),
    staleTime: 60_000,
    retry: 1,
    enabled: isDetail,
  });

  // v0.9.10 — Overview throughput grafiği: sayfa range'i penceresi
  // (audit §4 kararı — ?tw= drawer-yerel kalır), fetch yalnız
  // Overview sekmesi aktifken.
  const { from: rangeFrom, to: rangeTo } = useMemo(() => timeRangeToNs(range), [range]);
  const netTrendQ = useQuery({
    queryKey: ['cluster-net-trend', clusterParam, rangeFrom, rangeTo],
    queryFn: () => api.clusterNetworkTrend(clusterParam, rangeFrom, rangeTo),
    staleTime: 60_000,
    retry: 1,
    enabled: isDetail && section === 'overview',
  });

  // Detay: yalnız seçili cluster'ın AKTİF sekme sorguları.
  const detailList = isDetail ? [clusterParam] : [];
  const podQs = useQueries({
    queries: detailList.map(name => ({
      queryKey: ['cluster-pods', name],
      queryFn: () => api.clusterPods(name),
      staleTime: 60_000,
      retry: 1,
      enabled: section === 'pods',
    })),
  });
  const nodeQs = useQueries({
    queries: detailList.map(name => ({
      queryKey: ['cluster-nodes', name],
      queryFn: () => api.clusterNodes(name),
      staleTime: 60_000,
      retry: 1,
      enabled: section === 'nodes',
    })),
  });
  const nsQs = useQueries({
    queries: detailList.map(name => ({
      queryKey: ['cluster-namespaces', name],
      queryFn: () => api.clusterNamespaces(name),
      staleTime: 60_000,
      retry: 1,
      enabled: section === 'namespaces',
    })),
  });

  // v0.9.7 — ?q= metin süzgeci (operatör isteği): üç detay tablosunu
  // birden süzer (pod/namespace/node adında büyük-küçük duyarsız
  // substring). URL kaynak-of-truth, replace:true.
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
    return next;
  }, { replace: true });

  // useQueries dizi kimliği her render değişir — memo'lar sabit-
  // boyutlu içerik anahtarına bağlı (v0.8.578 deseni).
  const podDatas = podQs.map(q => q.data);
  const podDataKey = podDatas.map(d => (d ? `${d.cluster}:${d.count}` : '-')).join('|');
  const rows = useMemo(() => {
    let all = podDatas.flatMap(d => d?.pods ?? []);
    if (nsFilter) all = all.filter(r => r.namespace === nsFilter);
    if (qLower) all = all.filter(r =>
      r.pod.toLowerCase().includes(qLower) || r.namespace.toLowerCase().includes(qLower));
    return all;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [podDataKey, nsFilter, qLower]);

  const nodeDatas = nodeQs.map(q => q.data);
  const nodeDataKey = nodeDatas.map(d => (d ? `${d.cluster}:${d.count}` : '-')).join('|');
  const nodeRows = useMemo(() => {
    const all = nodeDatas.flatMap(d => d?.nodes ?? []);
    return qLower ? all.filter(r => r.node.toLowerCase().includes(qLower)) : all;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [nodeDataKey, qLower]);

  const nsDatas = nsQs.map(q => q.data);
  const nsDataKey = nsDatas.map(d => (d ? `${d.cluster}:${d.count}` : '-')).join('|');
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

        {/* ── Genel görünüm: cluster kartları ─────────────────── */}
        {!isDetail && sources.length > 0 && (
          <div style={{
            display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(260px, 1fr))',
            gap: 12,
          }}>
            {sources.map((name, i) => {
              const q = summaryQs[i];
              const sum: ClusterSummary | undefined = q?.data;
              const unreachable = q?.isError ?? false;
              const seen = observed.size === 0 || observed.has(name);
              return (
                <Card key={name}
                  onClick={() => openCluster(name)}
                  style={{ cursor: 'pointer' }}
                  header={
                    <span style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                      <span style={{ fontFamily: 'ui-monospace, monospace' }}>{name}</span>
                      {unreachable
                        ? <span className="badge b-err">unreachable</span>
                        : !seen
                          ? <span className="badge b-warn" title="Name not seen in the last 24h of telemetry — the service pivot will not match">not in telemetry</span>
                          : <span className="badge b-ok">reachable</span>}
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
                    <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 6, fontSize: 12 }}>
                      <div><span style={{ color: 'var(--text3)' }}>Nodes</span>{' '}
                        <strong className="mono">{sum.nodes ? fmtNum(sum.nodes) : '—'}</strong></div>
                      <div><span style={{ color: 'var(--text3)' }}>Pods</span>{' '}
                        <strong className="mono">{sum.pods ? fmtNum(sum.pods) : '—'}</strong></div>
                      <div><span style={{ color: 'var(--text3)' }}>CPU</span>{' '}
                        <strong className="mono">{sum.cpuUsedCores ? fmtCores(sum.cpuUsedCores) : '—'}</strong></div>
                      <div><span style={{ color: 'var(--text3)' }}>Memory</span>{' '}
                        <strong className="mono">{sum.memUsedBytes ? fmtBytes(sum.memUsedBytes) : '—'}</strong></div>
                    </div>
                  )}
                </Card>
              );
            })}
          </div>
        )}

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
                  {nodeQs[0]?.isPending && <TableSkeleton cols={6} wideFirst />}
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
                {section === 'namespaces' && <>
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
                                onClick={() => setSection(selected ? 'namespaces' : 'pods', p => {
                                  if (selected) p.delete('namespace');
                                  else p.set('namespace', r.namespace);
                                })}
                                title={selected
                                  ? 'Clear the namespace filter'
                                  : 'Open the pod list filtered to this namespace'}
                                style={{ cursor: 'pointer' }}>
                                <td className="mono" style={{ fontSize: 12 }}>{r.namespace}</td>
                                <td className="num mono">{r.pods ? fmtNum(r.pods) : '—'}</td>
                                <td className="num mono">{fmtCores(r.cpuCores)}</td>
                                <td className="num mono">{fmtBytes(r.memBytes)}</td>
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
                  {podQs[0]?.isPending && <TableSkeleton cols={7} wideFirst />}
                  {podErr && (
                    <div style={{ fontSize: 12, color: 'var(--text3)' }}>
                      Pod metrics unavailable — check the cluster entry in Settings.
                    </div>
                  )}
                  {!podErr && !podQs[0]?.isPending && rows.length === 0 && (
                    <div style={{ fontSize: 12, color: 'var(--text3)' }}>
                      {nsFilter
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
                {section === 'overview' && (
                  <div style={{
                    display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(220px, 1fr))',
                    gap: 12,
                  }}>
                    <Card density="tight" header="CPU used (cores)">
                      <div className="mono" style={{ fontSize: 22, fontWeight: 600 }}>
                        {detailSummaryQ.data?.cpuUsedCores ? fmtCores(detailSummaryQ.data.cpuUsedCores) : '—'}
                      </div>
                      <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
                        {detailSummaryQ.data?.nodes ? `${fmtNum(detailSummaryQ.data.nodes)} nodes` : ''}
                      </div>
                    </Card>
                    <Card density="tight" header="Memory used">
                      <div className="mono" style={{ fontSize: 22, fontWeight: 600 }}>
                        {detailSummaryQ.data?.memUsedBytes ? fmtBytes(detailSummaryQ.data.memUsedBytes) : '—'}
                      </div>
                      <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
                        {detailSummaryQ.data?.pods ? `${fmtNum(detailSummaryQ.data.pods)} pods` : ''}
                      </div>
                    </Card>
                    {/* v0.9.10 — net kartları yalnız veri VARSA (alan
                        yokluğu yanlış sıfır okutmaz — probe duruşu). */}
                    {(detailSummaryQ.data?.netInBps ?? 0) > 0 && (
                      <Card density="tight" header="Net in">
                        <div className="mono" style={{ fontSize: 22, fontWeight: 600 }}>
                          {fmtBps(detailSummaryQ.data!.netInBps!)}
                        </div>
                      </Card>
                    )}
                    {(detailSummaryQ.data?.netOutBps ?? 0) > 0 && (
                      <Card density="tight" header="Net out">
                        <div className="mono" style={{ fontSize: 22, fontWeight: 600 }}>
                          {fmtBps(detailSummaryQ.data!.netOutBps!)}
                        </div>
                      </Card>
                    )}
                  </div>
                )}
                {/* v0.9.10 — throughput grafiği: yalnız seri geldiyse
                    (node_network erişilemezse bölüm hiç görünmez). */}
                {section === 'overview' && (netTrendQ.data?.trend?.length ?? 0) > 0 && (
                  <Card header="Network throughput" style={{ marginTop: 14 }}>
                    <MultiLineChart
                      series={netTrendToSeries(netTrendQ.data!.trend!)}
                      height={200} />
                  </Card>
                )}
              </>
            )}
          </>
        )}

        {nsDrawerParam && (() => {
          const [c, ns] = nsDrawerParam.split('|');
          if (!c || !ns) return null;
          return <NamespaceDrawer cluster={c} namespace={ns} range={range} onClose={closeNsDrawer} />;
        })()}
        {podParam && (() => {
          const [c, ns, p] = podParam.split('|');
          if (!c || !ns || !p) return null;
          // Satır listede zaten yüklüyse drawer'a "current" kırılımı
          // için veriyoruz — deep-link'te satır henüz gelmemişse
          // drawer trend'le yetinir (ek istek YOK).
          const row = rows.find(r => r.cluster === c && r.namespace === ns && r.pod === p);
          return <PodDrawer cluster={c} namespace={ns} pod={p} row={row} range={range} onClose={closePod} />;
        })()}
      </div>
    </>
  );
}

// NamespaceDrawer — namespace'in pod başına trend grafikleri
// (v0.9.5, trend-upgrade audit T3): PodDrawer'ın aynası, panel
// multi-pod modda (top-10 seri + "Top N of M" etiketi). Yalnız
// açılınca fetch; ?tw= pencere seçicisi pod drawer'ıyla ortak.
function NamespaceDrawer({ cluster, namespace, range, onClose }: {
  cluster: string;
  namespace: string;
  range: TimeRange;
  onClose: () => void;
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
        <ThanosTrendPanel cluster={cluster} namespace={namespace}
          fromNs={from} toNs={to} />
      </DrawerSection>
    </Drawer>
  );
}

// PodDrawer — tek pod'un dakika-bucket'lı CPU/memory trendi.
// Yalnız açılınca fetch (ES-cost disiplininin Thanos karşılığı);
// staleTime = sunucu TTL'i.
// TREND_WINDOWS — drawer-yerel adaptif pencere rung'ları (v0.9.1,
// namespace-trend audit Dilim A). Sınırlı set → cache-key
// kardinalitesi bounded (v0.8.270 disiplini); '' = sayfa range'i.
const TREND_WINDOWS = [
  { key: '', label: 'Page range' },
  { key: '15m', label: '15m', ns: 15 * 60 * 1e9 },
  { key: '1h', label: '1h', ns: 3600 * 1e9 },
  { key: '6h', label: '6h', ns: 6 * 3600 * 1e9 },
] as const;

function PodDrawer({ cluster, namespace, pod, row, range, onClose }: {
  cluster: string;
  namespace: string;
  pod: string;
  row?: ClusterPodRow;
  range: TimeRange;
  onClose: () => void;
}) {
  // Adaptif pencere: ?tw= URL'de (kaynak-of-truth), sayfa range'inden
  // bağımsız — AnomalyDetailDrawer'ın chartRange yaklaşımının
  // kullanıcı-seçimli hali. Date.now() memo'su yalnız tw değişince
  // koşar (timeRangeToNs'in kurulu semantiğiyle aynı).
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
          {pod}
        </span>
        <span className="badge b-gray" title="namespace">{namespace}</span>
        <span className="badge b-gray" title="cluster">{cluster}</span>
      </>
    }>
      {/* v0.8.580 — iki eksenli anlık kırılım (limit + request). */}
      {row && (
        <DrawerSection title="Current">
          <table style={{ width: '100%', fontSize: 12 }}>
            <thead>
              <tr style={{ color: 'var(--text3)', fontSize: 11, textAlign: 'left' }}>
                <th></th><th className="num">Usage</th>
                <th className="num">of limit</th><th className="num">of request</th>
              </tr>
            </thead>
            <tbody>
              <tr>
                <td>CPU</td>
                <td className="num mono">{fmtCores(row.cpuCores)}</td>
                <td className="num mono">{row.cpuPct ? `${row.cpuPct.toFixed(0)}%` : '—'}</td>
                <td className="num mono" style={{
                  color: (row.cpuPctOfReq ?? 0) > 100 ? 'var(--warn)' : undefined,
                }}>{row.cpuPctOfReq ? `${row.cpuPctOfReq.toFixed(0)}%` : '—'}</td>
              </tr>
              <tr>
                <td>Memory</td>
                <td className="num mono">{fmtBytes(row.memBytes)}</td>
                <td className="num mono">{row.memPct ? `${row.memPct.toFixed(0)}%` : '—'}</td>
                <td className="num mono" style={{
                  color: (row.memPctOfReq ?? 0) > 100 ? 'var(--warn)' : undefined,
                }}>{row.memPctOfReq ? `${row.memPctOfReq.toFixed(0)}%` : '—'}</td>
              </tr>
            </tbody>
          </table>
        </DrawerSection>
      )}
      {/* v0.9.4 — Sparkline yerine tam MultiLineChart paneli
          (trend-upgrade audit T2): eksen + hover + limit/request
          threshold çizgileri; v0.9.1 tıkla-büyüt geçersiz kaldı. */}
      <DrawerSection title="Trend (per minute)">
        <div style={{ marginBottom: 8 }}>
          <select value={tw} onChange={e => setTw(e.target.value)}
            style={{ fontSize: 11 }}
            title="Trend window — independent of the page range">
            {TREND_WINDOWS.map(w => (
              <option key={w.key} value={w.key}>{w.label}</option>
            ))}
          </select>
        </div>
        <ThanosTrendPanel cluster={cluster} namespace={namespace} pod={pod}
          row={row} fromNs={from} toNs={to} />
      </DrawerSection>
    </Drawer>
  );
}
