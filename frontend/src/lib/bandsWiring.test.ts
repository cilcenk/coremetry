// bandsWiring.test.ts — v0.10.171 (inceleme #8): saf codec yeşilken kablolama
// koparsa CI görmez ([[feedback-tested-but-unreachable]]). Kaynak kapısı:
//   - Overview: anomali bölgeleri bandsOn'a bağlı, deploy ▼ anahtarın DIŞINDA,
//     toggle canlı location.search ile yazar, aria-pressed taşır;
//   - Pod: aynı üç sözleşme.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const read = (rel: string) => readFileSync(resolve(__dirname, rel), 'utf8').replace(/^\s*\/\/.*$/gm, '');

describe('anomali bantları ?bands= kablolaması (v0.10.170/171)', () => {
  it('Service Overview', () => {
    const src = read('../pages/service/Overview.tsx');
    expect(src).toMatch(/const a = bandsOn \? anomalyRegions\(/);
    expect(src).toMatch(/\[\.\.\.\(deployRegions \?\? \[\]\), \.\.\.a\]/);
    expect(src).toMatch(/writeBandsParam\(prev, !bandsOn, window\.location\.search\)/);
    expect(src).toMatch(/<LinkButton[^>]*aria-pressed=\{bandsOn\}/);
  });
  it('/pod', () => {
    const src = read('../pages/Pod.tsx');
    expect(src).toMatch(/if \(!bandsOn \|\| podWindowEvents\.length === 0\) return undefined;/);
    expect(src).toMatch(/writeBandsParam\(prev, !bandsOn, window\.location\.search\)/);
    expect(src).toMatch(/<LinkButton[^>]*aria-pressed=\{bandsOn\}/);
  });
});
