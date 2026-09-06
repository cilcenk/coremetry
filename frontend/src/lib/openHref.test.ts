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
  it('rejects non-app hrefs', () => {
    expect(mergeOpenHref('https://evil', '/trace', '')).toBeNull();
    expect(mergeOpenHref('//evil', '/trace', '')).toBeNull();
  });
});
