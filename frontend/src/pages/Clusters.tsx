import { useMemo } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { useQuery, useQueries } from '@tanstack/react-query';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { TableSkeleton } from '@/components/Skeleton';
import { Drawer, DrawerSection, DrawerTrendRow } from '@/components/ui';
import { api } from '@/lib/api';
import { timeRangeToNs, fmtBytes } from '@/lib/utils';
import { useUrlRange } from '@/lib/useUrlRange';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import type { ClusterPodRow, TimeRange } from '@/lib/types';

// /clusters — remote OpenShift clusters' pod CPU+memory via their
// Thanos Queriers (v0.8.578, audit: docs/audit/thanos-multicluster-
// metrics-audit.md §6-7). Hosts.tsx'in anatomik kopyası ama ayrı
// şekil: satır ekseni (cluster, namespace, pod), CPU çekirdek
// cinsinden (uygulamaların OTel process oranı DEĞİL — cAdvisor).
//
// Fan-out İSTEMCİDE (audit §6): enabled cluster başına bir istek
// (useQueries) — her cluster kendi 60s cache slotunda, bozuk/yavaş
// cluster yalnız kendi hata çipini gösterir, diğer paneller dolu
// kalır. Kısmi sonuç böylece React Query'den bedava.

const POD_COLS: DataTableColumn<ClusterPodRow>[] = [
  { id: 'cluster',   label: 'Cluster',   sortValue: r => r.cluster,   naturalDir: 'asc', width: 130 },
  { id: 'namespace', label: 'Namespace', sortValue: r => r.namespace, naturalDir: 'asc', width: 160 },
  { id: 'pod',       label: 'Pod',       sortValue: r => r.pod,       naturalDir: 'asc', width: 260 },
  { id: 'cpuCores',  label: 'CPU',       sortValue: r => r.cpuCores,  numeric: true, width: 90 },
  { id: 'cpuPct',    label: 'CPU %',     sortValue: r => r.cpuPct ?? 0, numeric: true, width: 80 },
  { id: 'memBytes',  label: 'Memory',    sortValue: r => r.memBytes,  numeric: true, width: 100 },
  { id: 'memPct',    label: 'Mem %',     sortValue: r => r.memPct ?? 0, numeric: true, width: 80 },
];

// fmtCores — 0.003 → "3m" (millicore okunuşu), 1.25 → "1.25".
function fmtCores(v: number): string {
  if (v < 0.01) return `${Math.round(v * 1000)}m`;
  if (v < 1) return `${(v * 1000).toFixed(0)}m`;
  return v.toFixed(2);
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

  // URL kaynak-of-truth (§4): cluster filtresi + drawer kimliği.
  const clusterFilter = params.get('cluster') ?? '';
  const setClusterFilter = (v: string) => setParams(prev => {
    const next = new URLSearchParams(prev);
    if (v) next.set('cluster', v); else next.delete('cluster');
    return next;
  }, { replace: true });
  // ?pod=<cluster>|<namespace>|<pod> — üçlü kimlik tek param.
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

  const active = clusterFilter ? sources.filter(s => s === clusterFilter) : sources;
  const podQs = useQueries({
    queries: active.map(name => ({
      queryKey: ['cluster-pods', name],
      queryFn: () => api.clusterPods(name),
      staleTime: 60_000, // = sunucu TTL'i (ES-cost disiplini)
      retry: 1,
    })),
  });

  // v0.8.579 — servis sayfasından gelen pivot linki namespace taşır
  // (?namespace=, audit §7.5). URL kaynak-of-truth; çip ile temizlenir.
  const nsFilter = params.get('namespace') ?? '';
  const clearNs = () => setParams(prev => {
    const next = new URLSearchParams(prev);
    next.delete('namespace');
    return next;
  }, { replace: true });

  // useQueries her render'da yeni dizi kimliği döndürür — memo'yu
  // içeriğe vekil sabit-boyutlu bir string anahtara bağlarız ki
  // useDataTable'ın sort memo'su her render'da yeniden koşmasın.
  const datas = podQs.map(q => q.data);
  const dataKey = datas.map(d => (d ? `${d.cluster}:${d.count}` : '-')).join('|');
  const rows = useMemo(() => {
    const all = datas.flatMap(d => d?.pods ?? []);
    return nsFilter ? all.filter(r => r.namespace === nsFilter) : all;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [dataKey, nsFilter]);

  const failing = active
    .map((name, i) => ({ name, q: podQs[i] }))
    .filter(x => x.q?.isError);
  const anyLoading = podQs.some(q => q.isPending);

  const dt = useDataTable<ClusterPodRow>({
    storageKey: 'clusterpods',
    columns: POD_COLS,
    rows,
    initialSort: { id: 'cpuCores', dir: 'desc' },
  });

  return (
    <>
      <Topbar title="Clusters" range={range} onRangeChange={setRange} />
      <div id="content">
        <div className="controls" style={{ marginBottom: 12 }}>
          {sources.length > 1 && (
            <select value={clusterFilter}
              onChange={e => setClusterFilter(e.target.value)}
              style={{ minWidth: 160 }}>
              <option value="">All clusters ({sources.length})</option>
              {sources.map(c => <option key={c} value={c}>{c}</option>)}
            </select>
          )}
          {nsFilter && (
            <span className="badge b-info" style={{ cursor: 'pointer' }}
              onClick={clearNs}
              title="Namespace filtresi (servis sayfasından pivot) — kaldırmak için tıkla">
              namespace: {nsFilter} ✕
            </span>
          )}
          <span style={{ color: 'var(--text2)', fontSize: 12 }}>
            Per-pod CPU + memory from each cluster's Thanos Querier —
            current values, refreshed on the server every 60s. Row click
            opens the per-minute trend.
          </span>
        </div>

        {/* Kısmi-sonuç sözleşmesi: bozuk cluster çip olur, tablo yaşar. */}
        {failing.length > 0 && (
          <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', marginBottom: 10 }}>
            {failing.map(f => (
              <span key={f.name} className="badge b-err"
                title={f.q.error instanceof Error ? f.q.error.message : 'fetch failed'}>
                {f.name}: unreachable
              </span>
            ))}
          </div>
        )}

        {sourcesQ.isPending && <Spinner />}
        {!sourcesQ.isPending && sources.length === 0 && (
          <Empty icon="◇" title="No remote clusters configured">
            Add Thanos Querier endpoints under{' '}
            <Link to="/settings/clusters">Settings → Remote clusters</Link>.
            Read-only; a viewer-role ServiceAccount token per cluster is enough.
          </Empty>
        )}
        {sources.length > 0 && anyLoading && rows.length === 0 && <TableSkeleton cols={7} wideFirst />}
        {sources.length > 0 && !anyLoading && rows.length === 0 && failing.length === 0 && (
          <Empty icon="∅" title="No pod samples">
            Queries returned no series — check the namespace filter on the
            cluster entry, or verify the metric names exist on this Thanos
            tenancy (audit §4).
          </Empty>
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
                    {/* v0.8.580 — % hücresi limit-bazlı kalır (throttle/
                        OOM ekseni); request ekseni title'da (provisioning
                        isabeti — >%100 aşım sinyaldir, clamp'siz). */}
                    <td className="num mono" style={{
                      color: (r.cpuPct ?? 0) > 85 ? 'var(--err)' : (r.cpuPct ?? 0) > 60 ? 'var(--warn)' : 'var(--text3)',
                    }} title={pctTitle('CPU', r.cpuPct, r.cpuPctOfReq)}>
                      {r.cpuPct ? r.cpuPct.toFixed(0) : '—'}</td>
                    <td className="num mono">{fmtBytes(r.memBytes)}</td>
                    <td className="num mono" style={{
                      color: (r.memPct ?? 0) > 85 ? 'var(--err)' : (r.memPct ?? 0) > 60 ? 'var(--warn)' : 'var(--text3)',
                    }} title={pctTitle('Memory', r.memPct, r.memPctOfReq)}>
                      {r.memPct ? r.memPct.toFixed(0) : '—'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}

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

// PodDrawer — tek pod'un dakika-bucket'lı CPU/memory trendi.
// Yalnız açılınca fetch (ES-cost disiplininin Thanos karşılığı);
// staleTime = sunucu TTL'i.
function PodDrawer({ cluster, namespace, pod, row, range, onClose }: {
  cluster: string;
  namespace: string;
  pod: string;
  row?: ClusterPodRow;
  range: TimeRange;
  onClose: () => void;
}) {
  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);
  const q = useQuery({
    queryKey: ['cluster-pod-detail', cluster, namespace, pod, from, to],
    queryFn: () => api.clusterPodDetail(cluster, namespace, pod, from, to),
    staleTime: 60_000,
  });
  const detail = q.isPending ? undefined : q.isError ? null : q.data;
  const trend = detail?.trend ?? [];

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
      {detail === undefined && <Spinner />}
      {detail === null && <Empty icon="✗" title="Failed to load pod detail" />}
      {detail && (
        <DrawerSection title="Trend (per minute)">
          {trend.length === 0 ? (
            <div style={{ fontSize: 12, color: 'var(--text3)' }}>No samples in this window.</div>
          ) : (
            <div style={{ display: 'grid', gap: 6 }}>
              <DrawerTrendRow label="CPU (cores)" values={trend.map(t => t.cpuCores)} color="var(--warn)" />
              <DrawerTrendRow label="Memory" values={trend.map(t => t.memBytes)} color="var(--accent2)" />
            </div>
          )}
        </DrawerSection>
      )}
    </Drawer>
  );
}
