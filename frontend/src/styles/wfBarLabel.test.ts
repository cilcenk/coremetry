import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

// v0.10.356 — Operator-reported: şelalede öz-süre gölgesi (.wf-bar-self,
// position:absolute) statik .wf-bar-label'ın üstüne boyanınca süre yazısı
// sönük kalıyordu. Etiket gölgenin üstünde (relative + z-index) ve beyaz.
describe('.wf-bar-label öz-süre gölgesinin üstünde ve beyaz (v0.10.356)', () => {
  const css = readFileSync(resolve(__dirname, 'globals.css'), 'utf8');
  const rule = css.match(/^\.wf-bar-label \{([^}]*)\}/m);
  it('kural var ve katmanı gölgeden üstte', () => {
    expect(rule).not.toBeNull();
    expect(rule![1]).toMatch(/position:\s*relative/);
    expect(rule![1]).toMatch(/z-index:\s*[1-9]/);
    expect(rule![1]).toMatch(/color:\s*var\(--on-accent\)/);
  });
  it('gölge etikete z-index ile geçmiyor', () => {
    const self = css.match(/^\.wf-bar-self \{([^}]*)\}/m);
    expect(self).not.toBeNull();
    expect(self![1]).not.toMatch(/z-index/);
  });
});
