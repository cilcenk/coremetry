import { useSearchParams, useNavigate } from 'react-router-dom';
import { useDataTable } from '@/components/DataTable';
import { Spinner, Empty } from '@/components/Spinner';
import { RuntimeCharts } from './RuntimeCharts';
import { ServiceClusterPods } from './ServiceClusterPods';
import { useServicePods } from './useServicePods';
import { podDetailPath } from './podDetailPath';
import { servicePodRegex } from '@/pages/clusters/podWorkload';
import type { DataTableColumn } from '@/lib/dataTable';
import type { ClusterPodRow, TimeRange } from '@/lib/types';

// ServicePodsTab (v0.9.158) — eski "Metrics" sekmesi "Pods" olarak yeniden
// adlandırıldı ve operatör isteğiyle pod-merkezli her şey buraya toplandı:
//   1. Cluster'a göre açılır pod grupları (ServiceClusterPods, Infra'dan
//      taşındı) — pod tıkla → yerinde JMX (PodJmxInline).
//   2. RuntimeCharts — OTel dil-runtime (heap/GC/threads by pod).
// (Aradaki bağımsız "JVM / JBoss (JMX)" bölümü — ServiceJmxPanels,
// ?jcluster/?jds — v0.9.533'te operatör isteğiyle kaldırıldı: pod
// genişletmesi zaten pod-başına JMX veriyor, ikinci cluster seçtirmek
// gereksizdi. ?jpod= artık satırı otomatik açan derin link.)
// Infrastructure sekmesi artık cluster-seviyesi (çipler/KPI/CPU-Mem/PromQL).
// Pod-envanteri paylaşılan useServicePods hook'undan (Infra ile aynı veri).

const POD_COLS: DataTableColumn<ClusterPodRow>[] = [
  { id: 'pod',      label: 'Pod',      sortValue: r => r.pod,      naturalDir: 'asc', width: 300 },
  { id: 'phase',    label: 'Status',   sortValue: r => r.phase ?? '', naturalDir: 'asc', width: 100 },
  { id: 'cpuCores', label: 'CPU',      sortValue: r => r.cpuCores, numeric: true, width: 90 },
  { id: 'memBytes', label: 'Memory',   sortValue: r => r.memBytes, numeric: true, width: 100 },
  { id: 'netIn',    label: 'Net in',   sortValue: r => r.netInBps ?? 0, numeric: true, width: 90 },
  { id: 'netOut',   label: 'Net out',  sortValue: r => r.netOutBps ?? 0, numeric: true, width: 90 },
  { id: 'restarts', label: 'Restarts', sortValue: r => (r.restartsUnknown ? -1 : r.restarts ?? 0), numeric: true, width: 84 },
];

export function ServicePodsTab({ service, range, onZoom, onZoomReset }: {
  service: string;
  range: TimeRange;
  onZoom?: (fromUnixSec: number, toUnixSec: number) => void;
  // Grafana-parite M1 — çift-tık: Service.tsx zoom geri-yığınını pop eder.
  onZoomReset?: () => void;
}) {
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const {
    metaQ, ns, deploy, matched, rows, clustersWithPods,
    effNs, effDeploy, from, to, cFrom, cTo, clamped,
    sourcesPending, noClusters, podsPending, sourcesError, podErrors, truncatedClusters } = useServicePods(service, range);

  // Accordion tek dt üstünde (sıralama/resize global; cluster'a göre gruplanır).
  const dt = useDataTable<ClusterPodRow>({
    storageKey: 'service-pods-tab', columns: POD_COLS,
    rows, initialSort: { id: 'cpuCores', dir: 'desc' },
  });

  // Pod'a tıkla → /pod tam detay (?range taşınır, from='pods' → geri-breadcrumb
  // "← <svc> · Pods" ve ?tab=pods'a döner, v0.9.159 review).
  const openPod = (r: ClusterPodRow) => navigate(podDetailPath({
    cluster: r.cluster, namespace: r.namespace, pod: r.pod,
    service, deploy: effDeploy, range: params.get('range'), from: 'pods',
  }));

  // Pod bölümü Thanos keşfine kapılı; RuntimeCharts (OTel, Thanos'suz) HER
  // ZAMAN render — erken-return ardında değil (review v0.9.159 #2): cold-nav'da
  // sources beklerken heap/GC grafikleri de gizlenmesin.
  return (
    <>
      {(metaQ.isPending || sourcesPending) ? (
        <Spinner />
      ) : sourcesError ? (
        /* v0.9.363 — başarısızlık, boşlukmuş gibi çizilmez. */
        <Empty icon="⚠" title="Thanos kaynaklarına erişilemedi">
          Pod keşfi başarısız — yapılandırma değil, erişim sorunu olabilir:{' '}
          <span className="mono">{sourcesError}</span>
        </Empty>
      ) : noClusters ? (
        <Empty icon="▦" title="No Thanos clusters configured">
          Add a remote cluster under Settings → Remote clusters to see pod-level metrics here.
        </Empty>
      ) : rows.length === 0 ? (
        podsPending ? <Spinner /> : podErrors.length > 0 ? (
          <Empty icon="⚠" title="Pod metrikleri okunamadı">
            {podErrors.join(', ')} cluster{podErrors.length > 1 ? "'ları" : "'ı"} sorguya
            yanıt vermedi — liste bu yüzden boş olabilir, workload yok demek değil.
          </Empty>
        ) : (
          <Empty icon="▦" title="No pods matched">
            {/* v0.9.536 — metin GERÇEK aday kalıbını gösterir (eskiden
                yalnız servis adını yazıyor ve operatörü yanıltıyordu:
                "aslında araması gereken mobile-overview-bff"). */}
            Tried {ns && deploy ? `k8s.namespace=${ns} · ${deploy}` : 'the k8s metadata mapping'}
            {' '}and pod-name matching (<span className="mono">{servicePodRegex(service, deploy)}</span>) across{' '}
            {matched.length} Thanos cluster{matched.length > 1 ? 's' : ''} — nothing matched.
            {truncatedClusters.length > 0 && (
              <div style={{ marginTop: 6 }}>
                ⚠ {truncatedClusters.join(', ')}: sunucu en işlek 500 pod'u döndürdü
                (topk tavanı) — sakin bir servisin pod'ları bu listenin DIŞINDA
                kalmış olabilir; "yok" kesin değil.
              </div>
            )}
          </Empty>
        )
      ) : (
        <>
          {/* v0.9.383 (redesign D7, mockup af7419e5) — yapışkan bölüm
              şeridi + kaynak rozetleri: üç bölümün ÜÇ AYRI veri kaynağı
              var (Thanos envanteri / Thanos JMX / OTel runtime) ve
              "neden bir kısmı çalışıyor?" sorusu ilk kez ekranda
              cevaplanıyor. Şerit anchor'ları uzun tek-akış sayfada GC
              grafiğine tek tık verir. */}
          <div style={{
            position: 'sticky', top: 0, zIndex: 5, display: 'flex', gap: 14,
            alignItems: 'center', fontSize: 12, padding: '6px 0',
            background: 'var(--bg)', borderBottom: '1px solid var(--border)',
            marginBottom: 8,
          }}>
            {([['pods-sec', `Pods (${rows.length})`], ['runtime-sec', 'Runtime']] as const).map(([id, label]) => (
              <span key={id} onClick={() => document.getElementById(id)?.scrollIntoView({ behavior: 'smooth', block: 'start' })}
                style={{ cursor: 'pointer', color: 'var(--accent)' }}>{label}</span>
            ))}
            <span style={{ marginLeft: 'auto' }} />
            {truncatedClusters.length > 0 && (
              <span className="badge b-warn"
                title={`${truncatedClusters.join(', ')}: sunucu en işlek 500 pod'u döndürdü (topk tavanı).`}>
                kısmi liste — topk(500)
              </span>
            )}
          </div>
          {/* 1) Cluster'a göre açılır pod grupları — pod tıkla → yerinde JMX. */}
          <h3 id="pods-sec" style={{ fontSize: 13, margin: '4px 0 8px', scrollMarginTop: 40 }}>
            Pods ({rows.length}) · {clustersWithPods.length} cluster{clustersWithPods.length > 1 ? 's' : ''}
            <span className="badge b-gray" style={{ marginLeft: 8 }}
              title="Bu bölümün kaynağı: Thanos kube-state-metrics/cAdvisor envanteri. Thanos erişilemezse bu bölüm düşer; alttaki Runtime bölümü OTel'den bağımsız çalışmaya devam eder.">
              Thanos · envanter
            </span>
          </h3>
          <ServiceClusterPods dt={dt} effNs={effNs} effDeploy={effDeploy}
            cFrom={cFrom} cTo={cTo} colCount={POD_COLS.length} onOpenPod={openPod} />
          {/* v0.9.533 (operatör) — buradaki bağımsız "JVM / JBoss (JMX)"
              bölümü (kendi cluster/pod/datasource seçicileriyle,
              ?jcluster/?jds) KALDIRILDI: pod satırı genişletmesi zaten
              pod-başına JMX veriyor, ikinci bir cluster seçtirmek
              gereksizdi. Deployment-geneli by-pod/by-datasource
              toplulaştırma bu bölümle gitti — geri istenirse tek-commit
              revert. ?jpod= derin linki (Problems→pod) yeniden amaçlandı:
              ServiceClusterPods ilgili satırı otomatik açıp kaydırıyor. */}
        </>
      )}

      {/* 3) OTel dil-runtime (heap/GC/threads by pod) — servis-scoped, her zaman. */}
      <div id="runtime-sec" style={{ marginTop: 20, scrollMarginTop: 40 }}>
        <div style={{ fontSize: 11, marginBottom: 4 }}>
          <span className="badge b-ok"
            title="Bu bölümün kaynağı: OTel runtime metrikleri (ClickHouse) — Thanos'tan tamamen bağımsız; Thanos düşse de burası çalışır.">
            OTel — Thanos'tan bağımsız
          </span>
        </div>
        <RuntimeCharts service={service} from={from} to={to} onZoom={onZoom} onZoomReset={onZoomReset} />
      </div>
    </>
  );
}
