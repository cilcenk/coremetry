// scrollParent.ts — v0.10.324 (operatör, prod: 1000+ span'lik trace'in
// şelalesi kesiliyor). Uygulama PENCEREDE kaydırmaz: kaydırma kabı
// `#content` (flex:1 + overflow:auto), trace sayfasında `.tc-wf`. Pencere
// sanallaştırıcısı (useWindowVirtualizer) bu kaplardan hiç scroll olayı
// almaz → yalnız ilk ekran kadar satır çizilir, gerisi boş. Sanallaştırıcı
// GERÇEK kaydırma kabına bağlanmalı; bu yardımcı onu bulur. Saf DOM.
export function findScrollParent(el: Element | null): HTMLElement | null {
  let node: Element | null = el?.parentElement ?? null;
  while (node && node !== document.body && node !== document.documentElement) {
    const cs = window.getComputedStyle(node);
    // jsdom overflowY'yi 'visible' doldurur, kısa yazım overflow'da kalır →
    // ikisine de bak.
    const scrolls = (v: string) => v === 'auto' || v === 'scroll' || v === 'overlay';
    if (scrolls(cs.overflowY) || scrolls(cs.overflow)) return node as HTMLElement;
    node = node.parentElement;
  }
  return null;
}

/** Listenin kaydırma kabı içindeki başlangıç ofseti (px) — virtualizer scrollMargin. */
export function offsetWithinScrollParent(list: HTMLElement, scrollEl: HTMLElement): number {
  const lr = list.getBoundingClientRect();
  const sr = scrollEl.getBoundingClientRect();
  return Math.max(0, lr.top - sr.top + scrollEl.scrollTop);
}
