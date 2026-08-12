// lib/traceNameCol.ts — şelale isim kolonu genişliği (v0.9.983).
//
// SAF: DOM yok, tarih yok. `TraceWaterfall.tsx`ten çıkarıldı ki dar ekran
// düzeltmesi (denetim B1) tablo-güdümlü sınanabilsin.
//
// BULGU (B1, "Yolculuk B kırık"): isim kolonunun TABANI konteyner
// genişliğinden BAĞIMSIZDI. `depthMin` yalnız ağaç derinliğine bakıp
// 220–320px arası bir taban dayatıyordu ve dıştaki `Math.max` onu her
// koşulda uyguluyordu. 366px'lik bir telefonda derin bir trace'te
// isim kolonu 320px alıyor, BAR ALANINA 46px kalıyordu — yani şelalenin
// tek varlık sebebi olan "hangi span ne kadar sürdü" karşılaştırması
// ölçülemez hâle geliyordu.
//
// DÜZELTME: `depthMin` konteynerin YARISIYLA da sınırlanıyor.
//
// Masaüstü DEĞİŞMEZ ve bu ispatlanabilir: yeni sınır ancak
// `containerWidth * 0.5 < 320`, yani `containerWidth < 640` iken
// bağlayıcı olur. ≥640px'te dönen değer bit bit eskisiyle aynı —
// testte hem eski hem yeni formül koşturularak çivileniyor.

export const NAME_MIN = 160;
export const NAME_MAX = 800;
export const INDENT_PX = 16;

/** Konteyner ölçülmeden önceki ilk render'ın kullandığı değer. */
export const NAME_UNMEASURED = 380;

export function nameColWidth(containerWidth: number, maxDepth: number): number {
  if (containerWidth <= 0) return NAME_UNMEASURED;
  const target = Math.round((containerWidth - 6) * 0.4);
  // Derinlik tabanı: girintili ağaç okunaklı kalsın diye. Artık
  // konteynerin yarısını AŞAMAZ — dar ekranda bar alanını yiyen buydu.
  const depthMin = Math.min(320, 220 + maxDepth * INDENT_PX, containerWidth * 0.5);
  return Math.max(NAME_MIN, depthMin, Math.min(target, NAME_MAX, containerWidth * 0.65));
}
