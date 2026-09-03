// Pod.compare.test.ts — v0.10.315 (chart-layer Dilim 2.3b) kaynak pini:
// üç RED paneli ghostItems alır, band kurucusu canlı+ghost için TEK
// (throughputBandsFrom), Compare to: aynı hook/bileşenle.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const src = readFileSync(resolve(__dirname, 'Pod.tsx'), 'utf8');
const sc = readFileSync(resolve(__dirname, '../components/ServiceCharts.tsx'), 'utf8');

describe('Pod önceki-dönem hayaleti', () => {
  it('üç panelde ghostItems, tek band kurucusu, ghost sorguları yalnız compare açıkken', () => {
    for (const g of ['ghostItems={latencyGhost}', 'ghostItems={throughputGhost}', 'ghostItems={failureGhost}']) {
      expect(src.split(g).length - 1).toBe(1);
    }
    expect(src.split('throughputBandsFrom(').length - 1).toBeGreaterThanOrEqual(3); // tanım + canlı + ghost
    expect(src.split('enabled: ghostEnabled').length - 1).toBe(2);
    expect(src).toContain('const ghostEnabled = redEnabled && compareOff > 0');
  });
  it('Compare to: ServiceCharts ile aynı hook ve bileşen', () => {
    expect(src).toContain('useCompareParam()');
    expect(src).toContain('<CompareToggle value={compare} onChange={setCompare} />');
    expect(sc).toContain('useCompareParam({ seedKey: STORAGE_KEYS.svcChartsCompare })');
    expect(sc).toContain('<CompareToggle value={compare} onChange={setCompareAndPersist} />');
    expect(sc).not.toContain("Compare to:</span>");
  });
});
