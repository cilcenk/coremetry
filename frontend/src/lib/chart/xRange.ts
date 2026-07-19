// xRangePinned — x-scale'i SORGU penceresine sabitleme kararının saf
// çekirdeği (v0.9.8x, uPlot Aşama 2 madde 2).
//
// Sorun: dört grafik bileşeninde de x sadece { time: true } — uPlot
// ekseni VERİYE fit eder. Emit etmeyi bırakmış servisin grafiği erken
// biter ve seçili aralık dar görünür; operatör "veri yok"u "aralık
// farklı"dan ayıramaz. from/to zaten URL'den (?range=) geliyor.
//
// Tasarım: x-scale'e SABİT min/max koymak drag-zoom'u kırar (uPlot her
// setScale'i range fonksiyonundan geçirir). Bu yüzden karar ikili:
//   - AUTO-FIT isteği (istek tüm veri aralığını kapsıyor — ilk çizim /
//     setData refit / çift-tık reset) → sorgu penceresi ∪ veri aralığı.
//     Union, pencere dışına taşan veriyi (saat kayması, geniş fetch)
//     asla gizlemez.
//   - ZOOM isteği (veriden dar) → istek AYNEN geçer; drag-zoom çalışır.
export interface XPin {
  from: number; // unix saniye
  to: number;
}

export function xRangePinned(
  times: ReadonlyArray<number>,
  pin: XPin | null | undefined,
  reqMin: number,
  reqMax: number,
  tolSec = 0.5,
): [number, number] {
  if (!pin || !(pin.to > pin.from)) return [reqMin, reqMax];
  if (!times.length) return [pin.from, pin.to];
  const fullMin = times[0];
  const fullMax = times[times.length - 1];
  const autoFit = reqMin <= fullMin + tolSec && reqMax >= fullMax - tolSec;
  return autoFit
    ? [Math.min(pin.from, fullMin), Math.max(pin.to, fullMax)]
    : [reqMin, reqMax];
}
