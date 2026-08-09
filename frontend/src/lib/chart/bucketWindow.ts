// bucketWindow — "spike → exemplar" tıkının çözdüğü ZAMAN PENCERESİ.
//
// v0.7.22'den beri MultiLineChart'ın `ready` hook'unun içinde YAŞIYORDU.
// v0.9.789'da bucket-tık CorePanel'e taşınınca hesap İKİ motorda birden
// gerekti — ve iki kopya, aynı tıkın iki farklı pencere üretmesi
// demekti (aynı ekranda iki farklı exemplar). Eski motor v0.9.844'te
// söküldü; modül kaldı, çünkü hesabın SAF ve testli olması motor
// sayısından bağımsız bir kazanç.
//
// KURAL (MLC'nin v0.7.22 davranışının birebiri):
//   1. İmlecin en yakın x noktası bulunur.
//   2. Adım = o noktanın YEREL boşluğu: min(soldaki fark, sağdaki fark).
//      Düzensiz aralıklı ekseni (union x, kaçmış scrape) doğru okur.
//   3. Yerel boşluk yoksa/bozuksa ilk boşluğa (xs[1]-xs[0]) düşer.
//   4. O da yoksa (tek nokta / bozuk eksen) 60 sn.
//   5. Pencere = [x − adım/2, x + adım/2] — bucket'ın MERKEZİ tıklanan
//      an, kenarları değil.
//
// BİRİM: uPlot v1 gövdesi (MLC) x'i SANİYE tutar, v2 motoru (CorePanel /
// @grafana/ui köprüsü) MİLİSANİYE. Hesabı iki kez yazmamak için birim
// çağrıda bildirilir (`unitsPerSec`: 1 = sn, 1000 = ms) ve DÖNÜŞ her
// zaman NANOSANİYE'dir — API sözleşmesi (api.spanExemplar) ns konuşur.
// Dizi kopyalamak yerine bölme yapılır: tık sıcak yol değil ama 500
// noktalık bir ekseni her tıkta yeniden ayırmak da gereksiz.

export const DEFAULT_BUCKET_STEP_SEC = 60;

// bucketStepSec — tıklanan noktanın bucket genişliği, SANİYE.
// `xs` ve `x` aynı birimde (unitsPerSec ile saniyeye çevrilir).
export function bucketStepSec(
  xs: ArrayLike<number>,
  x: number,
  unitsPerSec = 1,
): number {
  if (xs.length < 2) return DEFAULT_BUCKET_STEP_SEC;
  // En yakın nokta + yerel boşluk. Doğrusal tarama bilinçli: eksen
  // sıralı olsa da ikili arama bu boyutta (≤ birkaç bin nokta, tık
  // başına bir kez) ölçülebilir bir şey kazandırmaz.
  let nearestI = 0;
  let best = Infinity;
  for (let i = 0; i < xs.length; i++) {
    const d = Math.abs(xs[i] - x);
    if (d < best) { best = d; nearestI = i; }
  }
  const lo = nearestI > 0 ? xs[nearestI] - xs[nearestI - 1] : Infinity;
  const hi = nearestI < xs.length - 1 ? xs[nearestI + 1] - xs[nearestI] : Infinity;
  const gap = Math.min(lo, hi);
  if (isFinite(gap) && gap > 0) return gap / unitsPerSec;
  // Yerel boşluk kullanılamaz (tek yönlü uç + bozuk fark): ilk boşluk.
  const fallback = xs[1] - xs[0];
  if (fallback > 0) return fallback / unitsPerSec;
  return DEFAULT_BUCKET_STEP_SEC;
}

// bucketWindowNs — tıklanan andan [from, to] NANOSANİYE penceresi.
// Math.round: *1e9 ölçeğinde float kayması sunucunun BETWEEN'ini
// ıskalar (v0.7.22 notu) — yuvarlama sözleşmenin parçası.
export function bucketWindowNs(
  xs: ArrayLike<number>,
  x: number,
  unitsPerSec = 1,
): { fromNs: number; toNs: number } {
  const xSec = x / unitsPerSec;
  const half = bucketStepSec(xs, x, unitsPerSec) / 2;
  return {
    fromNs: Math.round((xSec - half) * 1e9),
    toNs: Math.round((xSec + half) * 1e9),
  };
}
