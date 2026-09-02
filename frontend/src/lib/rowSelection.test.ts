// rowSelection.test.ts — v0.10.246 DataTable dilim 1: id-anahtarlı seçim, çapa yeniden sıralamada korunur.
import { describe, it, expect } from 'vitest';
import { EMPTY_SELECTION, toggleRow, rangeSelect, selectAll, pruneSelection } from './rowSelection';

describe('rowSelection', () => {
  it('toggle multi/single', () => {
    let s = toggleRow(EMPTY_SELECTION, 'a');
    s = toggleRow(s, 'b');
    expect([...s.ids]).toEqual(['a', 'b']);
    expect(s.anchor).toBe('b');
    s = toggleRow(s, 'a');
    expect([...s.ids]).toEqual(['b']);
    const single = toggleRow(toggleRow(EMPTY_SELECTION, 'a', 'single'), 'b', 'single');
    expect([...single.ids]).toEqual(['b']);
    expect(toggleRow(single, 'b', 'single')).toBe(EMPTY_SELECTION);
  });
  it('Shift-aralığı çapadan ölçülür ve yeniden sıralamada çapa satırını takip eder', () => {
    const s = toggleRow(EMPTY_SELECTION, 'c');
    const r1 = rangeSelect(s, ['a', 'b', 'c', 'd', 'e'], 'e');
    expect([...r1.ids].sort()).toEqual(['c', 'd', 'e']);
    // yeniden sıralandı: çapa 'c' artık başta → aralık c..a
    const r2 = rangeSelect(s, ['c', 'b', 'a', 'd', 'e'], 'a');
    expect([...r2.ids].sort()).toEqual(['a', 'b', 'c']);
    expect(r2.anchor).toBe('c');
    // çapa yoksa tekil ekleme
    const r3 = rangeSelect(EMPTY_SELECTION, ['a', 'b'], 'b');
    expect([...r3.ids]).toEqual(['b']);
    expect(rangeSelect(s, ['a'], 'zzz')).toBe(s);
  });
  it('selectAll / prune', () => {
    const all = selectAll(['x', 'y']);
    expect(all.ids.size).toBe(2);
    expect(all.anchor).toBe('x');
    const p = pruneSelection(all, ['y', 'z']);
    expect([...p.ids]).toEqual(['y']);
    expect(p.anchor).toBeNull();
    expect(pruneSelection(all, ['x', 'y', 'q'])).toBe(all);
  });
});
