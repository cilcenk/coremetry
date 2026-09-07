import { describe, it, expect } from 'vitest';
import { appendChatBlock, chartBlocks, mergeBlockLinks } from './chatBlocks';
import type { ChatTypedBlock } from './types';

// v0.10.541 — blok biriktirme: id tekil (son kazanır), seq sıralı; chart süzgeci
// şekli doğrular; link blokları href tekil olarak çiplere katılır.
const mk = (id: string, seq: number, type: ChatTypedBlock['type'], payload: unknown): ChatTypedBlock => ({ id, seq, type, final: true, payload });

describe('chatBlocks', () => {
  it('append: sıra seq, aynı id yerine geçer', () => {
    let bs = appendChatBlock(undefined, mk('b2', 2, 'link', { href: '/x' }));
    bs = appendChatBlock(bs, mk('b1', 1, 'chart', { service: 'api', agg: 'p95' }));
    bs = appendChatBlock(bs, mk('b2', 2, 'link', { href: '/y', label: 'Y' }));
    expect(bs.map(b => b.id)).toEqual(['b1', 'b2']);
    expect((bs[1].payload as { href: string }).href).toBe('/y');
  });
  it('chartBlocks yalnız geçerli şekli döner', () => {
    const bs = [mk('b1', 1, 'chart', { service: 'api', agg: 'p95', fromNs: 1, toNs: 2 }), mk('b2', 2, 'chart', { nope: 1 }), mk('b3', 3, 'link', { href: '/x' })];
    expect(chartBlocks(bs)).toEqual([{ service: 'api', agg: 'p95', fromNs: 1, toNs: 2 }]);
    expect(chartBlocks(undefined)).toEqual([]);
  });
  it('mergeBlockLinks: href tekil, etiket yoksa href', () => {
    const bs = [mk('b1', 1, 'link', { href: '/traces?x', label: 'Traces' }), mk('b2', 2, 'link', { href: '/logs' }), mk('b3', 3, 'link', { href: '/traces?x', label: 'dup' })];
    expect(mergeBlockLinks([{ label: 'A', href: '/a' }], bs)).toEqual([
      { label: 'A', href: '/a' }, { label: 'Traces', href: '/traces?x' }, { label: '/logs', href: '/logs' },
    ]);
  });
});
