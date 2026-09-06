// openHref — v0.10.460: sunucunun `open` verdiği uygulama-içi adres, AYNI
// sayfadaysa mevcut sorgu paramlarıyla birleştirilir (range/env/chat gibi
// yabancı paramlar korunur — URL tek gerçek kaynak ev kuralı); farklı
// sayfaysa aynen gidilir. Yalnız kök-göreli href; dış adres asla.
// pageOwnedParams — v0.10.495: sunucunun `open` verdiği sayfanın KENDİ
// süzgeç/sıralama paramları. Aynı sayfadayken birleştirme bunları önce
// SİLER: "trace'leri getir" cevabı /traces'e yeni bir arama taşıyor;
// ekranda kalan eski rootOnly/hasError/filters yeni aramaya sessizce
// karışırdı (birleştirilmiş URL aracın sorguladığı kümeyi göstermezdi).
// Yabancı paramlar (range/env/chat/cols/viz) korunmaya devam eder.
// Liste Traces.tsx'in okuduğu searchParams anahtarlarından (K4 disiplini).
const pageOwnedParams: Record<string, readonly string[]> = {
  '/traces': [
    'service', 'services', 'search', 'filters', 'filterGroup', 'hasError', 'rootOnly',
    'minMs', 'maxMs', 'sort', 'order', 'page', 'view', 'groupBy', 'groupAttr', 'having',
    'aggSort', 'aggOrder', 'cluster', 'traceId',
  ],
};

export function mergeOpenHref(href: string, currentPathname: string, currentSearch: string): string | null {
  if (!href.startsWith('/') || href.startsWith('//')) return null;
  const q = href.indexOf('?');
  const path = q >= 0 ? href.slice(0, q) : href;
  if (path !== currentPathname) return href;
  const next = new URLSearchParams(currentSearch);
  for (const k of pageOwnedParams[path] ?? []) next.delete(k);
  new URLSearchParams(q >= 0 ? href.slice(q + 1) : '').forEach((v, k) => next.set(k, v));
  const qs = next.toString();
  return qs ? `${path}?${qs}` : path;
}
