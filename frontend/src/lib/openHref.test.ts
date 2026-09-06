import { describe, it, expect } from 'vitest';
import { mergeOpenHref } from './openHref';

// v0.10.460 — aynı sayfada `open` mevcut paramları korur; farklı sayfa aynen; dış adres null.
describe('mergeOpenHref', () => {
  it('merges into the current page query and preserves foreign params', () => {
    expect(mergeOpenHref('/trace?id=abc&ai=trace%3Aabc&aisrc=chat', '/trace', '?id=abc&range=30m&chat=c1'))
      .toBe('/trace?id=abc&range=30m&chat=c1&ai=trace%3Aabc&aisrc=chat');
  });
  it('navigates verbatim to another page', () => {
    expect(mergeOpenHref('/service?name=x', '/trace', '?id=abc')).toBe('/service?name=x');
  });
  // v0.10.495 — /traces aynı sayfa: sayfanın kendi süzgeçleri (rootOnly/hasError/
  // filters/service…) silinir, yabancı paramlar (range/cols/chat) kalır; yeni arama
  // olduğu gibi gelir. Operatör: "traceleri getir" cevabı arkada Traces'i açsın.
  it('clears the page-owned filter params on /traces before merging', () => {
    expect(mergeOpenHref(
      '/traces?service=x&search=POST+%2Fv1%2Fa&range=custom%3A1-2',
      '/traces',
      '?range=12h&rootOnly=true&hasError=true&filters=%5B%5D&cols=a%2Cb&chat=c1&page=3',
    )).toBe('/traces?range=custom%3A1-2&cols=a%2Cb&chat=c1&service=x&search=POST+%2Fv1%2Fa');
  });
  it('keeps the old merge semantics on pages without an owned-param list', () => {
    expect(mergeOpenHref('/trace?id=abc', '/trace', '?id=zzz&range=30m')).toBe('/trace?id=abc&range=30m');
  });
  it('rejects non-app hrefs', () => {
    expect(mergeOpenHref('https://evil', '/trace', '')).toBeNull();
    expect(mergeOpenHref('//evil', '/trace', '')).toBeNull();
  });
});
