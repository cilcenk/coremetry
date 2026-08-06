// visibleStats — legend istatistikleri GÖRÜNEN x-aralığından (FAZ 2).
//
// Spec: "İstatistikler GÖRÜNEN aralıktan hesaplanır, zoom'da güncellenir.
// Sorgu tetiklemez." Yani zoom bir fetch DEĞİL, eldeki dizinin
// pencerelenmesidir — hesap saf kalır ve legendStats.seriesStats'ın
// üstüne yalnız aralık filtresi biner.
//
// null'lar seriesStats'ın kendi sözleşmesiyle elenir (eksik veri
// istatistiğe girmez; 0 sayılsaydı ortalama/min yalan söylerdi —
// gapPolicy/dataFrame ile aynı ilke).

import { seriesStats, type SeriesStat } from './legendStats';

// visibleRangeStats — [minSec, maxSec] penceresine düşen noktaların
// istatistiği. times UNIX SANİYE (uPlot x ekseni birimi; usePageZoomRange
// sözleşmesindeki birimle aynı). Sınırlar dahil: uPlot'un scale min/max'i
// görünen uç noktaları kapsar.
export function visibleRangeStats(
  times: ReadonlyArray<number>,
  values: ReadonlyArray<number | null | undefined>,
  minSec: number,
  maxSec: number,
): SeriesStat {
  // Tam pencere = tüm dizi: kırpmadan geç (her redraw'da slice çöpü olmasın).
  if (times.length === 0 || (minSec <= times[0] && maxSec >= times[times.length - 1])) {
    return seriesStats(values);
  }
  // times artan sıralı (sunucu toStartOfInterval hizası) → ikili arama.
  let lo = 0, hi = times.length;
  while (lo < hi) { const m = (lo + hi) >> 1; if (times[m] < minSec) lo = m + 1; else hi = m; }
  let end = lo;
  { let l = lo, h = times.length; while (l < h) { const m = (l + h) >> 1; if (times[m] <= maxSec) l = m + 1; else h = m; } end = l; }
  return seriesStats(values.slice(lo, end));
}
