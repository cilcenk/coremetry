// Overview.compare.test.ts — v0.10.316 kaynak pini: hayalet yalnız Failure
// rate · trace panelinde; iki ghost sorgusu yalnız compare açıkken; toggle
// Details (ServiceCharts) ile aynı hook + tohum anahtarı.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const src = readFileSync(resolve(__dirname, 'Overview.tsx'), 'utf8');

describe('Overview önceki-dönem hayaleti', () => {
  it('tek panelde ghostItems (failure), metrik paneller hayaletsiz', () => {
    expect(src.split('ghostItems={').length - 1).toBe(1);
    const i = src.indexOf('ghostItems={failureGhost}');
    expect(i).toBeGreaterThan(src.indexOf('storageKey="ov-throughput-failure-v2"'));
  });
  it('ghost sorguları yalnız compare açıkken; Details ile aynı tohum', () => {
    expect(src.split('enabled: ghostEnabled').length - 1).toBe(2);
    expect(src).toContain('useCompareParam({ seedKey: STORAGE_KEYS.svcChartsCompare })');
    expect(src).toContain('<CompareToggle value={compare} onChange={setCompare} />');
  });
});
