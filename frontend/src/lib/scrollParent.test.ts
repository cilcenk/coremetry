// @vitest-environment jsdom
// scrollParent.test.ts — v0.10.324: en yakın overflow:auto/scroll ata bulunur;
// body/html'e dayanınca null (pencere). Ofset kabın scrollTop'unu içerir.
import { describe, it, expect } from 'vitest';
import { findScrollParent, offsetWithinScrollParent } from './scrollParent';

describe('findScrollParent', () => {
  it('overflow auto/scroll ata → o eleman; yoksa null', () => {
    const outer = document.createElement('div'); outer.style.overflowY = 'auto';
    const mid = document.createElement('div');
    const list = document.createElement('div');
    mid.appendChild(list); outer.appendChild(mid); document.body.appendChild(outer);
    expect(findScrollParent(list)).toBe(outer);
    mid.style.overflow = 'scroll';
    expect(findScrollParent(list)).toBe(mid);
    const lone = document.createElement('div'); document.body.appendChild(lone);
    expect(findScrollParent(lone)).toBeNull();
    expect(findScrollParent(null)).toBeNull();
  });
  it('offsetWithinScrollParent scrollTop ile toplar, negatif olmaz', () => {
    const sc = document.createElement('div'); const list = document.createElement('div');
    sc.appendChild(list); document.body.appendChild(sc);
    Object.defineProperty(sc, 'scrollTop', { value: 120, configurable: true });
    sc.getBoundingClientRect = () => ({ top: 100 } as DOMRect);
    list.getBoundingClientRect = () => ({ top: 130 } as DOMRect);
    expect(offsetWithinScrollParent(list, sc)).toBe(150);
    list.getBoundingClientRect = () => ({ top: -500 } as DOMRect);
    expect(offsetWithinScrollParent(list, sc)).toBe(0);
  });
});
