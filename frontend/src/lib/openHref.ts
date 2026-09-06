// openHref — v0.10.460: sunucunun `open` verdiği uygulama-içi adres, AYNI
// sayfadaysa mevcut sorgu paramlarıyla birleştirilir (range/env/chat gibi
// yabancı paramlar korunur — URL tek gerçek kaynak ev kuralı); farklı
// sayfaysa aynen gidilir. Yalnız kök-göreli href; dış adres asla.
export function mergeOpenHref(href: string, currentPathname: string, currentSearch: string): string | null {
  if (!href.startsWith('/') || href.startsWith('//')) return null;
  const q = href.indexOf('?');
  const path = q >= 0 ? href.slice(0, q) : href;
  if (path !== currentPathname) return href;
  const next = new URLSearchParams(currentSearch);
  new URLSearchParams(q >= 0 ? href.slice(q + 1) : '').forEach((v, k) => next.set(k, v));
  const qs = next.toString();
  return qs ? `${path}?${qs}` : path;
}
