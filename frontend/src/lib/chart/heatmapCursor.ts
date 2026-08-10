// heatmapCursor — paylaşılan crosshair'in heatmap ızgarasındaki karşılığı
// (v0.9.948, UX denetimi D5 / Ö26).
//
// LatencyHeatmap bir CANVAS: uPlot.sync'e katılamaz (uPlot yalnız kendi
// örneklerini senkronlar), yani kardeş grafiklerin imleci ona ancak
// cursorBus üzerinden ulaşır. Bu modül o zamanı bir SÜTUNA çeviriyor.
//
// SÜTUN, piksel değil: heatmap kovalıdır ve iz kovanın ORTASINA oturmalı.
// Ham zamanı doğrudan piksele çevirmek, izi kovanın kenarına düşürür ve
// operatör bir kova sağdaki/soldaki hücreyi okuduğunu sanır.
//
// SAF — tablo testleri heatmapCursor.test.ts.

/**
 * heatmapCursorCol — cursorSec (unix SANİYE) hangi kovaya düşer?
 *
 * times: kova başlangıçları, unix NANOS (LatencyHeatmap veri şekli),
 * artan sırada.
 *
 * PENCERE DIŞI null döner ve bu bir kelepçe DEĞİL: kardeş grafik daha
 * geniş bir pencere çiziyorsa (ör. heatmap 6h örneklenmiş, çizgi 24h),
 * imleci en yakın kenara YAPIŞTIRMAK yanlış bir anı işaretlerdi. Yarım
 * kova tolerans, ızgaranın kendi çözünürlüğü.
 */
export function heatmapCursorCol(times: number[], cursorSec: number): number | null {
  if (times.length === 0 || !isFinite(cursorSec)) return null;
  const target = cursorSec * 1e9;
  // Kova genişliği ızgaradan; tek kovalı ızgarada tolerans olarak 1 dk.
  const step = times.length >= 2 ? times[1] - times[0] : 60e9;
  const half = Math.abs(step) / 2;
  if (target < times[0] - half || target > times[times.length - 1] + half) return null;
  // İkili arama: hedeften küçük olmayan ilk kova.
  let lo = 0, hi = times.length - 1;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if (times[mid] < target) lo = mid + 1;
    else hi = mid;
  }
  let best = lo;
  if (lo > 0 && Math.abs(times[lo - 1] - target) <= Math.abs(times[lo] - target)) {
    best = lo - 1;
  }
  return best;
}

/**
 * heatmapCursorX — sütunun piksel merkezi. Çizim geometrisi çağırandan
 * gelir (padding + hücre genişliği), böylece bu modül düzenden habersiz
 * kalır ve tek işi yapar.
 */
export function heatmapCursorX(col: number, padLeft: number, cellW: number): number {
  return padLeft + (col + 0.5) * cellW;
}
