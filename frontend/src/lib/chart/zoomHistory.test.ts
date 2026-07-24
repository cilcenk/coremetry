import { describe, it, expect } from 'vitest';
import { pushZoom, popZoom } from './zoomHistory';

// zoomHistory (Grafana-parite M1) — çift-tık "bir adım geri" yığınının saf
// çekirdeği. Service/Dashboard TimeRange iter, Explore {from,to}|null iter.

describe('pushZoom / popZoom', () => {
  it('LIFO: push a,b → pop b, sonra a, sonra boş (view null)', () => {
    let stack = pushZoom<string>([], 'a');
    stack = pushZoom(stack, 'b');
    let r = popZoom(stack);
    expect(r.view).toBe('b');
    r = popZoom(r.stack);
    expect(r.view).toBe('a');
    r = popZoom(r.stack);
    expect(r.view).toBe(null);
    expect(r.stack).toEqual([]);
  });

  it('immutable: push/pop girdiyi mutate etmez', () => {
    const orig = ['a'];
    const pushed = pushZoom(orig, 'b');
    expect(orig).toEqual(['a']);
    expect(pushed).toEqual(['a', 'b']);
    const { stack } = popZoom(pushed);
    expect(pushed).toEqual(['a', 'b']);
    expect(stack).toEqual(['a']);
  });

  it('boş yığında pop → {stack:[], view:null} (çağıran: preset default ya da no-op)', () => {
    expect(popZoom([])).toEqual({ stack: [], view: null });
  });

  it('derinlik sınırı 32: 40 push → 32 kalır, en ESKİLER düşer', () => {
    let stack: number[] = [];
    for (let i = 0; i < 40; i++) stack = pushZoom(stack, i);
    expect(stack).toHaveLength(32);
    expect(stack[0]).toBe(8);           // 0..7 düştü
    expect(stack[stack.length - 1]).toBe(39);
  });

  it('Explore şekli: null görünüm (ilk zoom öncesi) itilebilir ve geri döner', () => {
    type W = { from: number; to: number } | null;
    let stack = pushZoom<W>([], null);                 // ilk zoom: öncesi "zoom yok"
    stack = pushZoom<W>(stack, { from: 1, to: 2 });    // ikinci zoom
    let r = popZoom(stack);
    expect(r.view).toEqual({ from: 1, to: 2 });
    r = popZoom(r.stack);
    expect(r.view).toBe(null);                          // → tam görünüme dön
  });

  it('TimeRange şekli: obje referansı aynen korunur (kopyalanmaz)', () => {
    const range = { preset: 'custom', fromMs: 1, toMs: 2 };
    const stack = pushZoom<typeof range>([], range);
    expect(popZoom(stack).view).toBe(range);
  });
});
