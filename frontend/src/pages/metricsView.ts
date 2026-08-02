// v0.9.561 — /metrics görünüm kodeki.
//
// Operatör: "Metrics sayfasını direkt advanced query builder ile
// açabilir misin." Varsayılan görünüm katalogdan editöre geçiyor.
//
// Değişiklik tek satır GİBİ görünüyor ama üç sözleşmeye dokunuyor ve
// ikisi sessizce kırılabilir:
//
//  1. ESKİ DERİN LİNKLER. Sayfa `/metrics?metric=foo` linklerini
//     Explore'a yönlendiriyor ve koşul `!editor && legacyMetric`
//     yazıyordu. Varsayılan editöre çevrilince `!editor` HEP false
//     olur, yönlendirme hiç çalışmaz ve eski linkler sessizce boş bir
//     sorgu editörüne düşer. Yönlendirme kararı bu yüzden VARSAYILANA
//     değil, kullanıcının AÇIKÇA editör isteyip istemediğine bakmalı.
//
//  2. URL GİDİŞ-DÖNÜŞÜ. Yazma "varsayılansa yazma" der, okuma "yoksa
//     varsayılanı kullan" der; ikisi farklı varsayılan bilirse
//     kullanıcının seçimi yutulur. (Aynı sınıf v0.9.558'de topology
//     hop'unda çıktı; bu repoda üç kez daha tekrarlamış:
//     v0.8.256/265/267.)
//
//  3. YABANCI PARAMLAR. Eski toggle `/metrics?editor=1` diye TAM URL
//     yazıyordu ve `range`'i düşürüyordu — operatör zaman aralığını
//     seçip görünüm değiştirince aralık sıfırlanıyordu.

/** Sayfanın varsayılan görünümü (v0.9.561, operatör talebi). */
export const DEFAULT_METRICS_VIEW: MetricsView = 'editor';

export type MetricsView = 'editor' | 'catalogue';

/**
 * URL'den görünüm. Parametre yoksa varsayılan.
 *
 * `editor=1` → editör, `editor=0` → katalog. Başka her değer
 * varsayılana düşer: URL'i elle düzenleyen operatör boş ekranla
 * karşılaşmamalı.
 */
export function metricsViewFromParam(raw: string | null | undefined): MetricsView {
  if (raw === '1') return 'editor';
  if (raw === '0') return 'catalogue';
  return DEFAULT_METRICS_VIEW;
}

/** Görünümün URL değeri. `null` = parametreyi SİL (varsayılan). */
export function metricsViewUrlValue(v: MetricsView): string | null {
  if (v === DEFAULT_METRICS_VIEW) return null;
  return v === 'editor' ? '1' : '0';
}

/**
 * Eski `?metric=` derin linki Explore'a yönlendirilmeli mi?
 *
 * KRİTİK: karar varsayılan görünüme DEĞİL, kullanıcının açıkça editör
 * isteyip istemediğine bakar. `/metrics?metric=foo` bir metriği görmek
 * demektir ve sayfanın o günkü varsayılanı bunu değiştirmemeli.
 *
 * `/metrics?metric=foo&editor=1` yönlendirilmez — kullanıcı editörü
 * AÇIKÇA istemiş.
 */
export function shouldRedirectLegacyMetric(
  legacyMetric: string,
  editorParam: string | null | undefined,
): boolean {
  if (!legacyMetric) return false;
  return editorParam !== '1';
}
