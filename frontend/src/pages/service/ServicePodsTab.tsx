import { useSearchParams, useNavigate } from 'react-router-dom';
import { useDataTable } from '@/components/DataTable';
import { Spinner, Empty } from '@/components/Spinner';
import { RuntimeCharts } from './RuntimeCharts';
import { ServiceClusterPods } from './ServiceClusterPods';
import { ServiceJmxPanels } from './ServiceJmxPanels';
import { useServicePods } from './useServicePods';
import { podDetailPath } from './podDetailPath';
import type { DataTableColumn } from '@/lib/dataTable';
import type { ClusterPodRow, TimeRange } from '@/lib/types';

// ServicePodsTab (v0.9.158) — eski "Metrics" sekmesi "Pods" olarak yeniden
// adlandırıldı ve operatör isteğiyle pod-merkezli her şey buraya toplandı:
//   1. Cluster'a göre açılır pod grupları (ServiceClusterPods, Infra'dan
//      taşındı) — pod tıkla → yerinde JMX (PodJmxInline).
//   2. JVM / JBoss JMX panelleri (ServiceJmxPanels, Infra'dan taşındı) —
//      "Pods tabında JVM metrikleri de panellerde olsun".
//   3. RuntimeCharts — OTel dil-runtime (heap/GC/threads by pod).
// Infrastructure sekmesi artık cluster-seviyesi (çipler/KPI/CPU-Mem/PromQL).
// Pod-envanteri paylaşılan useServicePods hook'undan (Infra ile aynı veri).

const POD_COLS: DataTableColumn<ClusterPodRow>[] = [
  { id: 'pod',      label: 'Pod',      sortValue: r => r.pod,      naturalDir: 'asc', width: 300 },
  { id: 'phase',    label: 'Status',   sortValue: r => r.phase ?? '', naturalDir: 'asc', width: 100 },
  { id: 'cpuCores', label: 'CPU',      sortValue: r => r.cpuCores, numeric: true, width: 90 },
  { id: 'memBytes', label: 'Memory',   sortValue: r => r.memBytes, numeric: true, width: 100 },
  { id: 'netIn',    label: 'Net in',   sortValue: r => r.netInBps ?? 0, numeric: true, width: 90 },
  { id: 'netOut',   label: 'Net out',  sortValue: r => r.netOutBps ?? 0, numeric: true, width: 90 },
  { id: 'restarts', label: 'Restarts', sortValue: r => r.restarts ?? 0, numeric: true, width: 84 },
];

export function ServicePodsTab({ service, range, onZoom }: {
  service: string;
  range: TimeRange;
  onZoom?: (fromUnixSec: number, toUnixSec: number) => void;
}) {
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const {
    metaQ, ns, deploy, matched, rows, clustersWithPods,
    effNs, effDeploy, from, to, cFrom, cTo, clamped,
    sourcesPending, noClusters, podsPending,
  } = useServicePods(service, range);

  // Accordion tek dt üstünde (sıralama/resize global; cluster'a göre gruplanır).
  const dt = useDataTable<ClusterPodRow>({
    storageKey: 'service-pods-tab', columns: POD_COLS,
    rows, initialSort: { id: 'cpuCores', dir: 'desc' },
  });

  // Pod'a tıkla → /pod tam detay (?range taşınır, from='pods' → geri-breadcrumb).
  const openPod = (r: ClusterPodRow) => navigate(podDetailPath({
    cluster: r.cluster, namespace: r.namespace, pod: r.pod,
    service, deploy: effDeploy, range: params.get('range'), from: 'infra',
  }));

  // ── Kapılar (hook'lardan SONRA) ──
  if (metaQ.isPending || sourcesPending) return <Spinner />;

  const jmxCluster = clustersWithPods[0] ?? '';
  return (
    <>
      {noClusters ? (
        <Empty icon="▦" title="No Thanos clusters configured">
          Add a remote cluster under Settings → Remote clusters to see pod-level metrics here.
        </Empty>
      ) : rows.length === 0 ? (
        podsPending ? <Spinner /> : (
          <Empty icon="▦" title="No pods matched">
            Tried {ns && deploy ? `k8s.namespace=${ns} · ${deploy}` : 'the k8s metadata mapping'}
            {' '}and pod-name matching (<span className="mono">{service}-*</span>) across{' '}
            {matched.length} Thanos cluster{matched.length > 1 ? 's' : ''} — nothing matched.
          </Empty>
        )
      ) : (
        <>
          {/* 1) Cluster'a göre açılır pod grupları — pod tıkla → yerinde JMX. */}
          <h3 style={{ fontSize: 13, margin: '4px 0 8px' }}>
            Pods ({rows.length}) · {clustersWithPods.length} cluster{clustersWithPods.length > 1 ? 's' : ''}
          </h3>
          <ServiceClusterPods dt={dt} effNs={effNs} effDeploy={effDeploy}
            cFrom={cFrom} cTo={cTo} colCount={POD_COLS.length} onOpenPod={openPod} />

          {/* 2) JVM / JBoss JMX panelleri (servis-seviyesi, ilk cluster). */}
          <ServiceJmxPanels cluster={jmxCluster} effNs={effNs} effDeploy={effDeploy}
            cFrom={cFrom} cTo={cTo} clamped={clamped} rows={rows} onZoom={onZoom} />
        </>
      )}

      {/* 3) OTel dil-runtime (heap/GC/threads by pod) — servis-scoped, her zaman. */}
      <div style={{ marginTop: 20 }}>
        <RuntimeCharts service={service} from={from} to={to} onZoom={onZoom} />
      </div>
    </>
  );
}
