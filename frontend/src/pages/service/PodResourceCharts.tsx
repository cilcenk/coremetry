import { useQuery } from '@tanstack/react-query';
import { clampSuffix } from '@/lib/thanosWindow';
import { api } from '@/lib/api';
import { MetricArea } from '@/pages/clusters/MetricArea';

// PodResourceCharts — v0.9.546. Pods sekmesinin OTel-runtime YEDEĞİ.
//
// Operatör: "Pods sayfasında jvm metrikleri yoksa cpu memory NETWORK
// grafikleri gözükebilir."
//
// Bugüne kadar Runtime bölümü yalnız BİLİNEN bir dil ailesi varken
// çiziliyordu (jvm / dotnet / go — RuntimeCharts.familyOf); Node.js,
// Python ya da dili hiç raporlanmayan bir servis Pods sekmesinin
// altında BOŞLUK görüyordu. Oysa o servisin CPU/bellek/ağ verisi
// Thanos'ta zaten var ve pod tablosunda ANLIK kolon olarak
// gösteriliyor — eksik olan yalnız zaman serisiydi.
//
// Kaynak bilinçli olarak Thanos: OTel runtime metriği YOK diye buraya
// düşüyoruz, yani OTel'den ikinci bir aile denemek anlamsız. Aynı
// endpoint Infrastructure sekmesinin CPU/Mem grafiklerini de besliyor,
// yani yeni sorgu yolu açmıyoruz — cache paylaşılıyor.
//
// Ağ ayrı bir not hak ediyor: cAdvisor ağ sayaçlarını pod'un ALTYAPI
// (pause) container'ına yazar, o yüzden sunucudaki seçici CPU/Mem'den
// farklı (promql.go v0.9.546). Burada görünmeyen ama sessizce boş
// grafik üretebilecek bir ayrım.
export function PodResourceCharts({ service, cluster, ns, deploy, cFrom, cTo, clamped, onZoom, onZoomReset }: {
  service: string;
  cluster: string;
  ns: string;
  deploy: string;
  cFrom: number;
  cTo: number;
  clamped: boolean;
  onZoom?: (fromSec: number, toSec: number) => void;
  onZoomReset?: () => void;
}) {
  const ok = cluster !== '' && ns !== '' && deploy !== '';
  // Dört sorgu AÇIK yazıldı: hook'ları bir yardımcı fonksiyondan
  // çağırmak Rules of Hooks ihlali (çağrı sırası sabit olsa bile
  // eslint haklı olarak uyarır ve okuyan "koşullu mu?" diye durur).
  // retry:false — v0.9.539 gerekçesi: yanıt vermeyen cluster'ı
  // yeniden denemek beklemeyi ikiye katlıyor, 60s poll zaten doğal
  // yeniden deneme.
  const cpuQ = useQuery({
    queryKey: ['deploy-trend', cluster, ns, deploy, 'cpu', true, cFrom, cTo],
    queryFn: () => api.clusterDeployTrend(cluster, ns, deploy, 'cpu', true, cFrom, cTo),
    staleTime: 60_000, retry: false, enabled: ok,
  });
  const memQ = useQuery({
    queryKey: ['deploy-trend', cluster, ns, deploy, 'mem', true, cFrom, cTo],
    queryFn: () => api.clusterDeployTrend(cluster, ns, deploy, 'mem', true, cFrom, cTo),
    staleTime: 60_000, retry: false, enabled: ok,
  });
  const inQ = useQuery({
    queryKey: ['deploy-trend', cluster, ns, deploy, 'netin', true, cFrom, cTo],
    queryFn: () => api.clusterDeployTrend(cluster, ns, deploy, 'netin', true, cFrom, cTo),
    staleTime: 60_000, retry: false, enabled: ok,
  });
  const outQ = useQuery({
    queryKey: ['deploy-trend', cluster, ns, deploy, 'netout', true, cFrom, cTo],
    queryFn: () => api.clusterDeployTrend(cluster, ns, deploy, 'netout', true, cFrom, cTo),
    staleTime: 60_000, retry: false, enabled: ok,
  });

  const any =
    (cpuQ.data?.series?.length ?? 0) > 0 || (memQ.data?.series?.length ?? 0) > 0 ||
    (inQ.data?.series?.length ?? 0) > 0 || (outQ.data?.series?.length ?? 0) > 0;
  // Hiç seri yoksa bölüm hiç çizilmez — boş kart duvarı göstermek,
  // "veri yok" demenin en gürültülü hâli (ServiceInfraTab emsali).
  if (!any) return null;

  const suffix = `${cluster}${clampSuffix(clamped)}`;
  return (
    <div style={{ marginTop: 18 }}>
      <div style={{ fontSize: 13, fontWeight: 700, color: 'var(--text)' }}>
        Kaynak kullanımı — pod başına
      </div>
      <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 8 }}>
        Bu serviste OTel dil-runtime metriği (jvm/dotnet/go) yok; CPU, bellek ve
        ağ Thanos'tan geliyor. Lejantta bir pod'a tıkla = izole.
      </div>
      <div className="grid-2" style={{ display: 'grid', gap: 14 }}>
        <MetricArea title={`CPU (cores) · ${suffix}`} labelTrimPrefix={deploy}
          series={cpuQ.data?.series} seriesName="CPU" totalSeries={cpuQ.data?.totalSeries}
          onZoom={onZoom} onZoomReset={onZoomReset} syncKey={`podres:${service}`} />
        <MetricArea title={`Memory · ${suffix}`} unit="bytes" labelTrimPrefix={deploy}
          series={memQ.data?.series} seriesName="Memory" totalSeries={memQ.data?.totalSeries}
          onZoom={onZoom} onZoomReset={onZoomReset} syncKey={`podres:${service}`} />
        <MetricArea title={`Network in (B/s) · ${suffix}`} unit="bytes" labelTrimPrefix={deploy}
          series={inQ.data?.series} seriesName="In" totalSeries={inQ.data?.totalSeries}
          onZoom={onZoom} onZoomReset={onZoomReset} syncKey={`podres:${service}`} />
        <MetricArea title={`Network out (B/s) · ${suffix}`} unit="bytes" labelTrimPrefix={deploy}
          series={outQ.data?.series} seriesName="Out" totalSeries={outQ.data?.totalSeries}
          onZoom={onZoom} onZoomReset={onZoomReset} syncKey={`podres:${service}`} />
      </div>
    </div>
  );
}
