// endpointCols — pure codec for the /endpoints visible-column URL
// param (v0.8.574, audit seçenek 3). Mirrors the Logs `?cols=`
// contract (Logs.tsx colsParam): the param is OMITTED when the view
// equals the default (all columns visible) so plain URLs stay clean,
// and any garbage value falls back to the default instead of
// rendering an empty table. URL — not localStorage — per the house
// rule: column VISIBILITY changes what's on screen, so Copy link /
// SavedViewsBar must reproduce it (widths stay localStorage-only).

// parseColsParam — `?cols=` raw value → the set of visible column
// ids. null/'' → all visible (default). Unknown ids (stale links
// from a future/past column schema) are dropped; if nothing valid
// survives, fall back to all visible — a shared link must never
// produce a column-less table.
// v0.9.642 — VARSAYILAN artık "hepsi" olmak zorunda değil.
// /endpoints 16 kolonla ~1664px sürüyor; tipik dizüstünde içerik alanı
// ~1180px, yani tablo yatay kayıyordu (operatör-bildirimli). Varsayılanı
// daraltmak HİÇBİR ŞEY SİLMİYOR: gizlenen kolon ColumnManager'da bir tık
// ötede ve ?cols= ile hâlâ adreslenebilir.
//
// defaultIds atlanırsa davranış eskisiyle birebir aynı (hepsi görünür).
export function parseColsParam(
  raw: string | null,
  allIds: readonly string[],
  defaultIds: readonly string[] = allIds,
): Set<string> {
  if (!raw) return new Set(defaultIds);
  const known = new Set(allIds);
  const picked = raw.split(',').map(s => s.trim()).filter(id => known.has(id));
  // v0.9.642 — geçersiz/bayat ?cols= VARSAYILANA düşüyor, "hepsi"ne
  // değil: aksi halde eski bir link operatörü tam da şikâyet ettiği
  // 16 kolonluk geniş tabloya geri atardı.
  return picked.length > 0 ? new Set(picked) : new Set(defaultIds);
}

// formatColsParam — visible set → `?cols=` value; '' when all
// columns are visible (caller deletes the param). Emits ids in
// canonical column order — not insertion order — so the same view
// always produces the same URL (shareable links dedupe cleanly).
export function formatColsParam(
  visible: Set<string>,
  allIds: readonly string[],
  defaultIds: readonly string[] = allIds,
): string {
  const inOrder = allIds.filter(id => visible.has(id));
  // Param yalnız görünüm VARSAYILANDAN farklıysa yazılır — varsayılan
  // artık alt küme olabildiği için "hepsi mi" değil "varsayılan mı"
  // karşılaştırması yapılıyor.
  const isDefault = inOrder.length === defaultIds.length
    && inOrder.every(id => defaultIds.includes(id));
  return isDefault ? '' : inOrder.join(',');
}
