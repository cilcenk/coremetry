import { lazy, Suspense } from 'react';
import { severityThresholdLines, type Threshold } from '@/lib/chart/thresholdLines';
import { msSyncKey } from '@/lib/chart/syncNamespace';
import type { SpanMetricSeries } from '@/lib/types';
import { Spinner } from '@/components/Spinner';
import { type XPin } from '@/lib/chart/xRange';
import { foldTopN, foldNote, isOthersSeries } from '@/lib/chart/foldTopN';
import { type ChartTimeRegion } from '@/lib/chart/overlays';

// MultiLineChart — çok serili zaman grafiği adaptörü.
//
// v0.9.844 — ESKİ MOTOR SÖKÜLDÜ (operatör onayı 2026-08-09: "Eski motoru
// tamamen sök"). Bu dosya v0.8.520'den beri İKİ gövde taşıyordu: kendi
// uPlot'unu kuran `MultiLineChartInner` (~850 satır) ve CorePanel'e köprü
// kuran sarmalayıcı. Seçimi bir motor bayrağı yapıyordu; v0.9.743'ten beri
// varsayılan v2, yani v1 gövdesi yalnız kaçış kapısında çalışıyordu. Bayrak
// da gövde de gitti — geriye TEK yol kaldı: CorePanelMulti.
//
// Geriye kalan iş ADAPTASYONDUR, çizim değil: seri katlaması (foldTopN),
// karşılaştırma hayaletinin eksene kaydırılması, deploy işaretinin bölgeye
// çevrilmesi ve '-ms' senkron ad alanı. Çizimin tamamı
// components/chart/CorePanel.tsx'te — "her yerde aynı grafik" artık bir
// vaat değil, yapısal olarak tek yol.

// DeployMarker — lightweight shape the chart consumes for the
// dashed vertical "deploy line" overlay. Avoids leaking the
// fuller Deploy type into chart code so non-service callers
// (eg. Dashboards) can synthesise markers from any source.
export interface DeployMarker {
  timeUnixNs: number;
  label: string;        // service.version or whatever the caller wants
  // v0.9.844 — ÇİZİLMİYOR. v1 gövdesinin tooltip'i bunu deploy satırının
  // yanında gösteriyordu; CorePanel'in bölge etiketi yalnız `label` taşır.
  // Alan duruyor çünkü çağıranlar (ServiceCharts / Clusters) marker'ı zaten
  // bu şekilde kuruyor — kaldırmak çizime hiçbir şey katmadan üç dosyayı
  // dolaşmak olurdu. CorePanel bölge tooltip'i kazanırsa hazır.
  description?: string;
}

// Threshold — horizontal line at a y-value, optionally coloured
// by severity. Used for SLO targets, alert rule thresholds,
// "this is the limit" indicators. Same idea as Grafana's "alert
// threshold" overlay or Datadog's "warning / critical bands".
// v0.10.314 — tip lib/chart/thresholdLines.ts'e taşındı (tek kapı);
// buradan yeniden dışa aktarılır, importlar değişmez.
export type { Threshold };

// MultiLineChartProps — v0.9.844'te BAĞIMSIZ yazıldı. Eskiden
// `Parameters<typeof MultiLineChartInner>[0]` idi, yani sarmalayıcının
// sözleşmesi silinen gövdeden türüyordu; Inner giderken 12 tüketici dosya
// aynı anda tip kaybederdi. Sözleşme artık burada ve okunan tek şey bu.
export interface MultiLineChartProps {
  series: SpanMetricSeries[];
  unit?: string;
  height?: number;
  // Fold threshold — series past this count collapse into "others"
  // (default 8). Raise it for panels that must show every series with
  // no tail cut, e.g. JBoss datasource breakdowns (v0.9.148).
  maxSeries?: number;
  // Optional vertical deploy markers; drawn as thin time-regions.
  deploys?: DeployMarker[];
  // Optional horizontal threshold lines at given y values (SLO / alert
  // limits). severity picks the colour token (warn → amber, err → red).
  thresholds?: Threshold[];
  // Grafana-parite M3 — problem/anomali x-bölgeleri (arka-plan gölge + üst
  // şerit + etiket; lib/chart/overlays.ts).
  regions?: ChartTimeRegion[];
  // syncKey — when set, multiple charts on the same page sync their cursor
  // positions. Hovering one chart paints the crosshair on every chart
  // sharing the key (Grafana / Datadog dashboard behaviour).
  syncKey?: string;
  // onCursorTime (v0.9.948, D5/Ö26) — imleç zamanı (unix SANİYE; null =
  // ayrıldı). uPlot.sync YALNIZ uPlot örneklerini senkronlar; aynı zaman
  // eksenini paylaşan CANVAS yüzeyler (LatencyHeatmap) ancak bu kanaldan
  // haberdar olabilir. Verilmezse hiçbir maliyet yok.
  onCursorTime?: (timeSec: number | null) => void;
  // onZoom — click-drag a horizontal range to zoom. Receives unix SECONDS;
  // the page maps it onto its TimeRange state and re-fetches.
  onZoom?: (fromUnixSec: number, toUnixSec: number) => void;
  // onZoomReset (Grafana-parite M1) — çift-tık: sayfa geri-yığınını bir adım
  // pop eder. Verilmezse motorun yerleşik autoscale'i sürer.
  onZoomReset?: () => void;
  // onBucketClick — "spike → exemplar" (v0.7.22). Düz tık, tıklanan
  // bucket penceresini NANOSANİYE olarak verir; çağıran tipik olarak o
  // pencerenin temsilci trace'ini açar. TAM opsiyonel: prop yoksa grafik
  // ek bir tık affordance'ı kazanmaz.
  onBucketClick?: (fromNs: number, toNs: number) => void;
  // compareSeries — önceki dönem verisi; offset kadar ileri kaydırılıp
  // kesikli/soluk hayalet olarak biner ("dün bu saatte çizgi neredeydi").
  compareSeries?: SpanMetricSeries[];
  compareOffsetNs?: number;
  // logScale — v0.5.484. SRE'ler metrik büyüklük mertebesi atlarken log'a
  // geçer (HTTP durum sayıları, kuyruk derinlikleri, 5ms→5s yüzdelikler).
  logScale?: boolean;
  // v0.9.83 — x-eksenini sorgu penceresine sabitle (unix sec); zoom isteği
  // aynen geçer. Verilmezse veriden türetilir.
  xRange?: XPin | null;
  // Lejant seçimi bu anahtar altında localStorage'da KALICI; rebuild'de geri
  // uygulanır. Kullanıcı seçimi her zaman defaultHidden'ı ezer.
  legendStorageKey?: string;
  // Kayıtlı kullanıcı seçimi YOKKEN gizli başlayacak etiketler (operatör
  // default'u: latency panellerinde p99).
  defaultHidden?: readonly string[];
}

// v0.9.760 (operatör: "Endpoint/Database sayfalarındaki chartlar da yeni
// Grafana stili") — TEK KALDIRAÇ: her MLC çağrısı (Endpoints detayları,
// Databases/Dependencies panelleri, drawer'lar, ServiceCharts) CorePanel
// gövdesiyle çizilir.
const CoreMultiLazy = lazy(() =>
  import('@/components/chart/corePanelEntry').then(m => ({ default: m.CorePanelMulti })));

export function MultiLineChart(props: MultiLineChartProps) {
  const {
    series, unit, height = 320, deploys, thresholds, regions, syncKey,
    onZoom, onZoomReset, onCursorTime, xRange, legendStorageKey, defaultHidden,
    compareSeries, onBucketClick, logScale, maxSeries,
  } = props;
  // v0.9.807 — "others" katlaması. v0.9.789'da v2 kapısı açılırken bu adım
  // atlanmıştı: v1 gövdesi >N seriyi katlarken v2 yolu HEPSİNİ çiziyordu
  // (ServiceCharts by-op error-rate/latency/RPS üçlüsünde gözle görülür).
  // maxSeries çağıranın tavanı — JBoss datasource panelleri 40 veriyor
  // (v0.9.148), yani orada katlama pratikte hiç olmuyor.
  const eff = foldTopN(series, unit, maxSeries ?? undefined);
  // v0.9.764 — önceki-dönem hayaleti: compareSeries offset'le bugünün
  // eksenine kaydırılıp kesikli/soluk ghost olarak biner (mockup dilim
  // 3; MLC'nin compare bindirmesinin v2 karşılığı).
  const offsetNs = props.compareOffsetNs ?? 0;
  const ghostItems = (compareSeries?.length && offsetNs > 0)
    ? compareSeries.map((s0, i) => ({
        name: s0.groupKey?.length ? s0.groupKey.join(' · ') : `seri ${i + 1}`,
        role: 'muted' as const,
        series: [{ groupKey: s0.groupKey, points: s0.points.map(pt => ({ ...pt, time: pt.time + offsetNs })) }],
      }))
    : undefined;
  return (
    <Suspense fallback={<div style={{ height, display: 'grid', placeItems: 'center' }}><Spinner /></div>}>
      <CoreMultiLazy
        title=""
        storageKey={legendStorageKey ?? 'mlc'}
        height={height}
        unit={unit}
        items={eff.map((s0, i) => ({
          name: s0.groupKey?.length ? s0.groupKey.join(' · ') : `seri ${i + 1}`,
          // Katlanan kuyruk SESSİZ gri: v1 gövdesinin mutedGray'inin rol
          // karşılığı (seriesRoleColor('muted') → var(--text3)). Renk yine
          // tek kanaldan gelir, burada ikinci bir palet doğmaz.
          role: (isOthersSeries(s0) ? 'muted' : 'data') as 'muted' | 'data',
          series: [s0],
        }))}
        // Kırpma notu (dürüstlük satırı): kaç seri katlandı ve NEYE göre.
        // Katlama yoksa null → satır hiç çizilmez.
        note={foldNote(series.length, maxSeries ?? undefined)}
        defaultHidden={defaultHidden ? [...defaultHidden] : undefined}
        xRange={xRange}
        logScale={logScale}
        regions={[
          ...(regions ?? []),
          ...(deploys ?? []).map(d => ({
            fromSec: d.timeUnixNs / 1e9, toSec: d.timeUnixNs / 1e9,
            color: 'var(--accent2)', label: d.label,
          })),
        ]}
        thresholds={severityThresholdLines(thresholds)}
        ghostItems={ghostItems}
        // '-ms' eki MOTOR AD ALANIDIR, süs değil: saniye-eksenli motorlar
        // (TimeChart / OverviewChart / ChartCard) hâlâ yaşıyor ve uPlot.sync
        // değeri karşı grafiğin ölçeğine VALUE olarak taşır. Aynı anahtarı
        // paylaşan iki motor crosshair'i 1000× yanlış yere koyardı (Pod
        // sayfası: ChartCard + MLC aynı `podjmx:` kökünü kullanıyor).
        // Doğrudan CorePanel çağıranları da bu gruba girer
        // (DetailsMetricsSection, v0.9.789). v0.9.844'te motor sökümünden
        // SONRA DA ŞART — sökülen v1 gövdesiydi, saniye-eksenli kardeşler
        // değil.
        syncKey={msSyncKey(syncKey)} // v0.10.289 — ad alanı tek yerden (lib/chart/syncNamespace)
        onZoom={onZoom} onZoomReset={onZoomReset} onCursorTime={onCursorTime}
        onBucketClick={onBucketClick}
      />
    </Suspense>
  );
}
