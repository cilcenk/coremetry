// v0.9.574 — pod açılır panelinde CPU / bellek / ağ.
//
// Operatör: "Podlara tıklayınca cpu memory utilizasyon gelsin demiştik."
//
// v0.9.546'da bu vardı ama YANLIŞ KAPIDAYDI: PodResourceCharts sekme
// seviyesinde ve `!runtimeFamily` koşuluna bağlıydı — yani servisin
// runtime ailesi HİÇ tespit edilmediğinde çiziliyordu.
//
// Operatörün vakası tam aradaki boşluk: aile TESPİT EDİLMİŞ (JVM/JBoss)
// ama Thanos'ta o metrikler YOK. Kapı kapalı kaldığı için pod açılınca
// tek gördüğü "Bu servis için Thanos'ta JMX metriği keşfedilmedi"
// cümlesiydi — bir açıklama, ama bir veri değil.
//
// Doğru kural: aile tespit edilmiş olsa bile METRİK GERÇEKTEN YOKSA
// kaynak grafikleri gelir. Bir servisin dilini bilmek, o servisin
// kaynak kullanımını göstermemek için sebep değil.
import { useQuery } from '@tanstack/react-query';
import { thanosMaxDataPoints } from '@/lib/chartStep';
import { api } from '@/lib/api';
import { MetricArea } from '@/pages/clusters/MetricArea';
import type { ClusterNamedSeries } from '@/lib/types';

// keepPod — byPod serilerinden YALNIZ bu pod'unkini süzer.
//
// /api/clusters/deploy-trend pod parametresi almıyor (JMX ikizinin
// aksine), o yüzden daraltma istemcide. Maliyet yok: aynı sorgu
// anahtarı sekme seviyesindeki PodResourceCharts ile PAYLAŞILIYOR —
// o bileşen çizildiğinde bu fetch cache'ten gelir.
export function keepPod(series: ClusterNamedSeries[] | null | undefined, pod: string): ClusterNamedSeries[] {
  if (!series || !pod) return [];
  return series.filter(s => s.name === pod);
}

export function PodInlineResourceCharts({ cluster, ns, deploy, pod, cFrom, cTo }: {
  cluster: string;
  ns: string;
  deploy: string;
  pod: string;
  cFrom: number;
  cTo: number;
}) {
  const ok = cluster !== '' && ns !== '' && deploy !== '' && pod !== '';
  // Dört sorgu AÇIK yazıldı (PodResourceCharts emsali): hook'ları bir
  // yardımcıdan çağırmak Rules of Hooks ihlali.
  // retry:false — yanıt vermeyen cluster'ı yeniden denemek beklemeyi
  // ikiye katlıyor; panel zaten açılınca mount oluyor.
  const mdp = thanosMaxDataPoints(2); // v0.10.287 — sunucu nokta bütçesi
  const cpuQ = useQuery({
    queryKey: ['deploy-trend', cluster, ns, deploy, 'cpu', true, cFrom, cTo, mdp],
    queryFn: () => api.clusterDeployTrend(cluster, ns, deploy, 'cpu', true, cFrom, cTo, mdp),
    staleTime: 60_000, retry: false, enabled: ok,
  });
  const memQ = useQuery({
    queryKey: ['deploy-trend', cluster, ns, deploy, 'mem', true, cFrom, cTo, mdp],
    queryFn: () => api.clusterDeployTrend(cluster, ns, deploy, 'mem', true, cFrom, cTo, mdp),
    staleTime: 60_000, retry: false, enabled: ok,
  });
  const inQ = useQuery({
    queryKey: ['deploy-trend', cluster, ns, deploy, 'netin', true, cFrom, cTo, mdp],
    queryFn: () => api.clusterDeployTrend(cluster, ns, deploy, 'netin', true, cFrom, cTo, mdp),
    staleTime: 60_000, retry: false, enabled: ok,
  });
  const outQ = useQuery({
    queryKey: ['deploy-trend', cluster, ns, deploy, 'netout', true, cFrom, cTo, mdp],
    queryFn: () => api.clusterDeployTrend(cluster, ns, deploy, 'netout', true, cFrom, cTo, mdp),
    staleTime: 60_000, retry: false, enabled: ok,
  });

  const cpu = keepPod(cpuQ.data?.series, pod);
  const mem = keepPod(memQ.data?.series, pod);
  const nin = keepPod(inQ.data?.series, pod);
  const nout = keepPod(outQ.data?.series, pod);
  const any = cpu.length > 0 || mem.length > 0 || nin.length > 0 || nout.length > 0;

  const loading = cpuQ.isLoading || memQ.isLoading || inQ.isLoading || outQ.isLoading;
  if (loading && !any) {
    return <div style={{ fontSize: 12, color: 'var(--text3)' }}>Kaynak metrikleri yükleniyor…</div>;
  }
  // Hiç seri yoksa DÜRÜST kal: uydurma bir boş kart duvarı yerine tek
  // cümle. Bu pod Thanos'ta hiç görünmüyor olabilir (yeni pod, farklı
  // cluster, cAdvisor kapalı) ve bunu söylemek, dört boş grafik
  // çizmekten bilgilendirici.
  if (!any) {
    return (
      <div style={{ fontSize: 12, color: 'var(--text3)' }}>
        Bu pod için Thanos'ta kaynak metriği (CPU/bellek/ağ) de bulunamadı.
      </div>
    );
  }

  return (
    <div style={{ display: 'grid', gridTemplateColumns: 'repeat(2, minmax(0, 1fr))', gap: 12 }}>
      <MetricArea title="CPU (cores)" series={cpu} seriesName="CPU" height={150} />
      <MetricArea title="Memory" unit="bytes" series={mem} seriesName="Memory" height={150} />
      <MetricArea title="Network in (B/s)" unit="bytes" series={nin} seriesName="In" height={150} />
      <MetricArea title="Network out (B/s)" unit="bytes" series={nout} seriesName="Out" height={150} />
    </div>
  );
}
