// v0.9.645 — trace listesinin sayfalamayla ULAŞABİLDİĞİ tavan.
//
// MV hızlı yolu aşama-1'de topladığı trace kimliklerini aşama-2'ye bir
// IN listesiyle veriyor ve o liste sınırlı: clickhouse-go bind
// argümanlarını istemci tarafında yerleştirdiği için her kimlik ~35
// bayt sorgu metni tutuyor ve sunucunun max_query_size'ı (256 KiB) var
// (v0.8.363'te kod 62 "Syntax error at position 262126" ile bulundu).
//
// Bu yüzden "Son sayfa" düğmesi HER ZAMAN sunulamaz: toplam sayı bilinse
// bile ötesindeki sayfalar SUNULAMIYOR. v0.9.638 tam bu yüzden total'ı
// Pager'dan kesmişti — sayı asla sayfalama sınırı değil.
//
// Sabit backend'den KOPYA ve bu tehlikeli: ayrışırsa Last düğmesi
// sunulamayan bir sayfaya götürür. traceReach.test.ts Go kaynağını
// okuyup iki tarafın eşleştiğini doğruluyor.
export const TRACE_STAGE2_MAX_IDS = 6000;

/**
 * lastReachablePage — "Son sayfa" düğmesi hangi sayfaya götürsün?
 *
 * undefined = düğme ÇİZİLMEZ. Üç durumda:
 *   · sayı yok (sayım reddedildi ya da operatör istemedi)
 *   · sayı TAVANLI ("10.000+") — gerçek son sayfa bilinmiyor
 *   · toplam, sunulabilir tavanın ÖTESİNDE — düğme boşluğa götürürdü
 *
 * SAF — tablo testli.
 */
export function lastReachablePage(
  total: number | undefined,
  atLeast: boolean,
  pageSize: number,
): number | undefined {
  if (total === undefined || atLeast || pageSize <= 0) return undefined;
  if (total <= 0) return undefined;
  if (total > TRACE_STAGE2_MAX_IDS) return undefined;
  return Math.max(0, Math.ceil(total / pageSize) - 1);
}
