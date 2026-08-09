// volumeSeries.ts — /traces üst histogramının SAF konfig katmanı (v0.9.843).
//
// Neden ayrı dosya: "hangi seri hangi eksende" bir DÜZEN kararıdır ve
// v0.9.843'te operatör isteğiyle bir kez TAKAS edildi (süre sağdan sola,
// span sayısı soldan sağa — Grafana düzeni). Bir daha sessizce geri
// dönmesin diye eşleme tablo-testli; VolumeChart.tsx TimeChart→uPlot'a
// bağlı olduğundan node ortamındaki vitest onu import edemezdi, bu modül
// ise saf (tip dışında hiçbir çizim bağımlılığı yok).
//
// EKSEN SÖZLEŞMESİ (v0.9.843): SÜRE hep SOL eksende, SAYI hep SAĞ eksende.
// TimeChart sol ekseni fmtLeft ile, sağ ekseni fmtRight ile biçimlendirir —
// yani takasla birlikte biçimlendirici de taşınmak ZORUNDA (sol: ms/s,
// sağ: TimeChart'ın fmtAxisTick kısaltması "30.9M"). VolumeChart bu yüzden
// fmtLeft={fmtVolumeDuration} geçer ve fmtRight vermez.

import type { SpanMetricSeries } from '@/lib/types';
import type { TimeChartSeries } from '@/components/charts/TimeChart';
import { statusColor } from '@/lib/statusColor';

/** Süre ekseni formatı (v0.9.73; v0.9.843'e dek SAĞ eksendeydi, artık SOL):
 *  <1000ms ms, aksi s. "3100 ms" gibi okunmaz büyük değerleri "3.1s" yapar;
 *  küçük gecikmelerde tam sayı ms okunur. Değer+birim şablonu → her birim
 *  dalı testli (v0.6.36 birim-karışımı disiplini). */
export function fmtVolumeDuration(v: number): string {
  if (v >= 1000) return `${(v / 1000).toFixed(v >= 10000 ? 0 : 1)}s`;
  return `${v.toFixed(v < 10 ? 1 : 0)}ms`;
}

export interface VolumeChartConfig {
  /** unix saniye, artan — paylaşılan x ekseni. */
  times: number[];
  series: TimeChartSeries[];
  /** bucket genişliği (dakika), başlık ipucu için. */
  bucketMin: number;
}

/** buildVolumeSeries — count/errors/p50 serilerini TimeChart konfigine çevirir.
 *  Çizim sırası = üst üste binme sırası: tam bar (accent) önce, hata payı
 *  (kırmızı) üstüne — böylece her barın DİBİNDE okunur; p50 çizgisi en son. */
export function buildVolumeSeries(
  count: SpanMetricSeries[] | null,
  errors: SpanMetricSeries[] | null,
  p50: SpanMetricSeries[] | null,
): VolumeChartConfig {
  const cPts = count?.[0]?.points ?? [];
  if (!cPts.length) return { times: [], series: [], bucketMin: 1 };
  const eMap = new Map((errors?.[0]?.points ?? []).map(p => [p.time, p.value]));
  const pMap = new Map((p50?.[0]?.points ?? []).map(p => [p.time, p.value]));
  const t = cPts.map(p => Math.round(p.time / 1e9)); // ns → unix sec
  const total = cPts.map(p => p.value);
  const err = cPts.map(p => Math.min(eMap.get(p.time) ?? 0, p.value));
  // v0.9.73 — p50 GAP'li: örnek olmayan (ya da 0 dönen) bucket'ta null →
  // çizgi tabana çakmaz, gerçek boşluk gösterir. Eski `?? 0` her boş
  // bucket'ı 0ms'e çekip sahte iniş-çıkış üretiyordu.
  const p50d: (number | null)[] = cPts.map(p => {
    const v = pMap.get(p.time);
    return v && v > 0 ? v : null;
  });
  const dt = t.length > 1 ? Math.round((t[1] - t[0]) / 60) : 1;
  const series: TimeChartSeries[] = [
    // v0.9.843 — bar'lar SAĞ eksende (span sayısı).
    { key: 'total', label: 'ok spans', data: total, color: 'var(--accent)', type: 'bar', axis: 'right' },
    { key: 'error', label: 'errors', data: err, color: statusColor('error'), type: 'bar', axis: 'right' },
    // v0.9.73 — kalın çizgi + nokta: seyrek p50 örnekleri artık okunur.
    // v0.9.843 — süre SOL eksende (Grafana düzeni).
    { key: 'p50', label: 'p50 latency', data: p50d, color: 'var(--orange)', type: 'line', axis: 'left', width: 2.2, pointsShow: true },
  ];
  return { times: t, series, bucketMin: Math.max(1, dt) };
}
