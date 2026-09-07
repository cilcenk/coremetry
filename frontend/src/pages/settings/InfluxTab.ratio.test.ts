// InfluxTab.ratio.test.ts — v0.10.532 kaynak pini: oran türü tel'e yalnız
// kind==='ratio' iken gider ve o zaman flux BOŞ gider (sunucu "oranda flux"
// diye reddeder); şablon düğmesi iki girdi olmadan devre dışı; durum kartı
// atlanan oran kovasını söyler.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const src = readFileSync(resolve(__dirname, 'InfluxTab.tsx'), 'utf8');

describe('InfluxTab oran türü', () => {
  it('tel dönüşümü + tür anahtarı + şablon kapısı + durum satırı', () => {
    expect(src).toContain("flux: q.kind === 'ratio' ? '' : q.flux");
    expect(src).toContain("ratio: q.kind === 'ratio' ? ratioToWire(q.ratio) : undefined");
    expect(src).toContain("kind: q.ratio ? 'ratio' : 'flux', ratio: ratioToForm(q.ratio)");
    expect(src).toContain('aria-label="Sorgu türü"');
    expect(src).toContain("o.kind === 'flux' && o.name.trim()");
    expect(src).toContain('|| !r.queries.some(q => q.name === TFAIL_TEMPLATE.name)');
    expect(src).toContain('|| !r.queries.some(q => q.name === GG_TOTAL_TEMPLATE.name)');
    expect(src).toContain('queryFromWire(TFAIL_RATIO_TEMPLATE)] }])');
    expect(src).toContain('oran kovası atlandı');
    expect(src).toContain('!p.error && !p.wideWindow && p.hint');
  });
});
