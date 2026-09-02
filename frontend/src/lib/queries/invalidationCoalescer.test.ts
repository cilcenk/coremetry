// invalidationCoalescer.test.ts — v0.10.260: pencere içinde tekrar eden
// anahtar BİR kez invalidate edilir; farklı anahtarlar korunur; flush
// bekleyenleri boşaltır; pencere kapanınca yeni pencere açılır.
import { describe, it, expect } from 'vitest';
import { createCoalescer } from './invalidationCoalescer';

describe('createCoalescer', () => {
  it('aynı pencerede tekrar eden anahtarlar tek invalidate', () => {
    const calls: unknown[][] = [];
    let fire: (() => void) | null = null;
    const c = createCoalescer(k => calls.push([...k]), 250, fn => { fire = fn; return 0; });
    c.add(['problems']);
    c.add(['problems']);
    c.add(['inbox', 'count']);
    c.add(['problems']);
    expect(c.pending()).toBe(2);
    expect(calls.length).toBe(0);
    fire!();
    expect(calls).toEqual([['problems'], ['inbox', 'count']]);
    expect(c.pending()).toBe(0);
    // pencere kapandı → yeni ekleme yeni pencere açar
    c.add(['problems']);
    expect(c.pending()).toBe(1);
    c.flush();
    expect(calls.length).toBe(3);
  });
  it('flush boşken hiçbir şey yapmaz', () => {
    const calls: unknown[] = [];
    const c = createCoalescer(k => calls.push(k), 250, () => 0);
    c.flush();
    expect(calls.length).toBe(0);
  });
});
