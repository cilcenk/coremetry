// redBands.test.ts — v0.10.316: band matematiği (kelepçe, eksik er → 0,
// zaman korunur) + kaynak pini: Overview ve Pod band satırlarını buradan alır.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { okErrorPoints } from './redBands';

describe('okErrorPoints', () => {
  it('rate × (1 − er) / rate × er; eksik er 0; negatif kelepçe; zaman korunur', () => {
    const { ok, err } = okErrorPoints(
      [{ time: 1, value: 100 }, { time: 2, value: 50 }, { time: 3, value: -4 }],
      [{ time: 1, value: 10 }, { time: 2, value: 200 }],
    );
    expect(ok).toEqual([{ time: 1, value: 90 }, { time: 2, value: 0 }, { time: 3, value: 0 }]);
    expect(err).toEqual([{ time: 1, value: 10 }, { time: 2, value: 100 }, { time: 3, value: 0 }]);
  });
  it('kaynak pini: Overview ve Pod kendi map satırını taşımaz', () => {
    const root = resolve(__dirname, '../../');
    for (const f of ['pages/service/Overview.tsx', 'pages/Pod.tsx']) {
      const src = readFileSync(resolve(root, f), 'utf8');
      expect(src, f).toContain('okErrorPoints(');
      expect(src, f).not.toMatch(/p\.value \* \(1 - \(erPts\[i\]/);
    }
  });
});
