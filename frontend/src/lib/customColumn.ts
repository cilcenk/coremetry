// customColumn — v0.9.870 (tutarlılık denetimi mK3).
//
// ColumnManager'ın boş-sonuç mesajı "Press Enter to add it as a custom
// column." diyordu; input'ta onKeyDown YOKTU. Enter hiçbir şey yapmıyordu —
// operatör aradığı attr'ı bulamayınca metnin söylediğini yapıyor ve hiçbir
// şey olmuyordu. (Aynı panelde bir "Add custom column" BUTONU vardı, ama
// yalnız mesajın ALTINDA ve farklı bir koşulla.)
//
// Buradaki predicate'in varlık sebebi: Enter ile butonun AYNI koşula
// bağlanması. İkisi ayrı ayrı yazılsaydı, mesajın göründüğü ama Enter'ın
// (ya da butonun) çalışmadığı bir aralık kalırdı — mK3'ün ta kendisi, tersten.
//
// Koşul BİLEREK butonun bugünkü davranışının birebir aynısı; bu sürüm
// davranış eklemiyor, yalnız ikinci bir tetikleyici veriyor.

/** Geçerli bir öznitelik anahtarı: OTel semconv noktalı adları + tire/alt tire. */
export const CUSTOM_COL_KEY_RE = /^[a-zA-Z0-9._-]+$/;

export function canAddCustomColumn(opts: {
  /** Ham input değeri (trim'lenmemiş olabilir). */
  query: string;
  /** /api/attribute-keys döndü mü — null iken "eşleşme yok" demek anlamsız. */
  keysLoaded: boolean;
  /** Filtrelenmiş liste uzunluğu. 0 değilse zaten var olan bir anahtar seçilir. */
  filteredCount: number;
}): boolean {
  const q = opts.query.trim();
  if (!q) return false;
  if (!opts.keysLoaded) return false;
  if (opts.filteredCount !== 0) return false;
  return CUSTOM_COL_KEY_RE.test(q);
}
