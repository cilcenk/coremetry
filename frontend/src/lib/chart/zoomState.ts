import { yRangeHeadroom } from './yRange';

// zoomState — setData fast-path'inde "operatör drag-zoom yaptı mı"
// kararının saf çekirdeği (v0.9.78, uPlot Aşama 1 bug fix).
//
// Bug: MultiLineChart / OverviewChart / TimeChart 30s poll'de
// `u.setData(data)` çağırıyordu (resetScales=true) — bu x-ekseni tüm
// veri aralığına RESETLER, operatörü drag-zoom'undan atar. TSP bunu
// zaten controlled zoomWindow ile çözüyordu; diğer üçünde controlled
// zoom prop'u YOK (uPlot local setScale). Çözüm: fast-path'te mevcut
// x-scale'in daralmış (zoomlu) olup olmadığına bak; zoomsuz ise eski
// davranış (setData true — yeni bucket'lar x'e girsin), zoomlu ise
// setData(data,false) (x korunur) + y'yi elle refit.

// isXZoomed — u.scales.x'in tüm veri x-aralığından belirgin daha dar
// olup olmadığı. tolSec: bucket step'ine göre küçük tolerans (kayan
// nokta + kenar bucket'ları için); zaman saniye biriminde.
export function isXZoomed(
  times: number[],
  sMin: number | null | undefined,
  sMax: number | null | undefined,
  tolSec = 0.5,
): boolean {
  if (!times.length || sMin == null || sMax == null) return false;
  const fullMin = times[0];
  const fullMax = times[times.length - 1];
  return sMin > fullMin + tolSec || sMax < fullMax - tolSec;
}

// alignedSeriesMax — uPlot AlignedData'da verilen seri indekslerinin
// (1-based; data[0] = x ekseni) sonlu max değeri; null/NaN atlanır.
// y'yi elle refit ederken (setData(...,false) sonrası) tüm-veri max'ı.
export function alignedSeriesMax(
  data: ReadonlyArray<ReadonlyArray<number | null>>,
  seriesIdxs: number[],
): number {
  let mx = 0;
  for (const si of seriesIdxs) {
    const col = data[si];
    if (!col) continue;
    for (const v of col) {
      if (v != null && isFinite(v) && v > mx) mx = v;
    }
  }
  return mx;
}

// yRefitScale — bir eksenin [min,max]'ını tüm-veri max'ından üretir
// (yRangeHeadroom ile aynı 0-tabanlı %10-headroom kuralı); setScale'e
// verilecek {min,max} objesi. Eski setData(true)'nun y davranışını
// zoomlu setData(false) yolunda birebir yeniden üretir.
export function yRefitScale(
  data: ReadonlyArray<ReadonlyArray<number | null>>,
  seriesIdxs: number[],
): { min: number; max: number } {
  const [min, max] = yRangeHeadroom(
    // yRangeHeadroom u argümanını kullanmıyor (imza uyumu için cast).
    undefined as never,
    0,
    alignedSeriesMax(data, seriesIdxs),
  );
  return { min, max };
}
