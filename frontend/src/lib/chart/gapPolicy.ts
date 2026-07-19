import type uPlot from 'uplot';

// Gap politikası (v0.9.84, uPlot Aşama 2 madde 3) — spanGaps repoda hiç
// kullanılmıyordu: HER null bucket çizgiyi kırıyordu. Tek kaçmış scrape
// ile gerçek kesinti (outage) grafikte aynı görünüyordu.
//
// Politika: ardışık iki DOLU nokta arası süre `gapSec`, bucket adımı
// `stepSec` ise null-run = gapSec - stepSec. Null-run < 1.5×step →
// KÖPRÜLE (tek kaçmış scrape; gapSec = 2×step < 2.5×step). Daha uzunu →
// KIR (gerçek veri kesintisi görünür kalır).

// medianStep — x eksenindeki tipik bucket adımı (sn). Median, birkaç
// gap'in ortalamayı şişirmesine dayanıklı.
export function medianStep(times: ReadonlyArray<number>): number {
  if (times.length < 2) return 0;
  const diffs: number[] = [];
  for (let i = 1; i < times.length; i++) diffs.push(times[i] - times[i - 1]);
  diffs.sort((a, b) => a - b);
  return diffs[Math.floor(diffs.length / 2)];
}

// isBridgeableGap — iki dolu nokta arası gapSec köprülenebilir mi.
export function isBridgeableGap(gapSec: number, stepSec: number, factor = 1.5): boolean {
  if (stepSec <= 0) return false;
  return gapSec - stepSec < factor * stepSec;
}

// filterGapsPx — uPlot'un px cinsinden null-gap listesinden köprülenebilir
// (kısa) olanları çıkarır; kalanlar gap olarak çizilir. Saf: px→sn çevrimi
// pxPerSec ile parametre.
export function filterGapsPx(
  nullGaps: ReadonlyArray<[number, number]>,
  pxPerSec: number,
  stepSec: number,
  factor = 1.5,
): [number, number][] {
  if (stepSec <= 0 || pxPerSec <= 0) return nullGaps.slice() as [number, number][];
  return nullGaps.filter(g => !isBridgeableGap((g[1] - g[0]) / pxPerSec, stepSec, factor)) as [number, number][];
}

// stepGapsRefiner — uPlot series.gaps callback'i: line/area serilerde
// kısa boşlukları köprüler. Bileşenler doğrudan `gaps: stepGapsRefiner`
// bağlar (durum yok, tüm bilgi u'dan).
export function stepGapsRefiner(
  u: uPlot,
  _sidx: number,
  _idx0: number,
  _idx1: number,
  nullGaps: [number, number][],
): [number, number][] {
  if (!nullGaps.length) return nullGaps;
  const xs = u.data[0] as number[];
  const step = medianStep(xs);
  if (step <= 0) return nullGaps;
  const x0 = u.scales.x.min ?? xs[0];
  const pxPerSec = (u.valToPos(x0 + step, 'x', true) - u.valToPos(x0, 'x', true)) / step;
  return filterGapsPx(nullGaps, pxPerSec, step);
}

// nearestFilledIdx (madde 4) — cursor'ın en yakın DOLU örneğe snap'i.
// Null destekli seyrek serilerde hover sürekli "—" göstermesin; arama
// penceresi bucket cinsinden sınırlı (maxDist), sınırsız arama YOK.
// Eşit uzaklıkta soldaki (daha eski gerçek örnek) kazanır.
export function nearestFilledIdx(
  values: ReadonlyArray<number | null | undefined>,
  idx: number,
  maxDist = 2,
): number {
  if (idx < 0 || idx >= values.length) return idx;
  if (values[idx] != null) return idx;
  for (let d = 1; d <= maxDist; d++) {
    const l = idx - d;
    const r = idx + d;
    if (l >= 0 && values[l] != null) return l;
    if (r < values.length && values[r] != null) return r;
  }
  return idx;
}
