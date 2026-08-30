// seriesCompact.wiring.test.ts — v0.10.186: çözücü api.dashboardData'ya bağlı
// (saf çekirdek yeşilken kablosuz kalmasın — feedback-tested-but-unreachable).
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

describe('sütunsal nokta kodlaması kablolaması', () => {
  it('api.dashboardData → decodeBundle; sunucu enc/cols alanları tipte', () => {
    const src = readFileSync(resolve(__dirname, 'api.ts'), 'utf8');
    const i = src.indexOf('withMetricSource(`/api/dashboards/data`)');
    expect(i).toBeGreaterThan(0);
    expect(src.slice(i, i + 900)).toMatch(/\.then\(decodeBundle\)/);
    expect(src).toMatch(/enc\?: string; cols\?: CompactSeries\[\]/);
    expect(src.slice(i, i + 900)).toMatch(/enc: SERIES_ENC/); // opt-in gövdede, sabit tek yerde (189: col2; eski sekme düz şekil alır)
  });
});
