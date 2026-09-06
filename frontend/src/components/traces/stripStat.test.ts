// v0.10.513 — şerit istatistiği: p95 varsayılan, URL'den seçilebilir.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { STRIP_STATS, STRIP_STAT_DEFAULT, parseStripStat, stripStatLabel, stripStatHeaderLabel } from './stripStat';

describe('stripStat', () => {
  it('varsayılan p95; tanınmayan/boş değer varsayılana düşer', () => {
    expect(STRIP_STAT_DEFAULT).toBe('p95');
    expect(parseStripStat(null)).toBe('p95');
    expect(parseStripStat('')).toBe('p95');
    expect(parseStripStat('avg')).toBe('p95');
    expect(parseStripStat('p50')).toBe('p50');
    expect(parseStripStat('p99')).toBe('p99');
    expect(STRIP_STATS).toEqual(['p50', 'p95', 'p99']);
  });
  it('etiketler: p50 "median", başlık "P95 MAX"', () => {
    expect(stripStatLabel('p50')).toBe('median');
    expect(stripStatLabel('p95')).toBe('p95');
    expect(stripStatHeaderLabel('p50')).toBe('MEDIAN MAX');
    expect(stripStatHeaderLabel('p99')).toBe('P99 MAX');
  });
  it('kaynak pinleri: Traces şeridi ?rt= okur/yazar (varsayılan yazılmaz), agg seçime bağlı, expand düğmesi yok', () => {
    const src = readFileSync(resolve(__dirname, '../../pages/Traces.tsx'), 'utf8');
    expect(src).toContain("parseStripStat(searchParams.get('rt'))");
    expect(src).toContain("['rt',       stripStat !== STRIP_STAT_DEFAULT ? stripStat : '']");
    expect(src).toContain("{ name: 'rt', agg: stripStat, field: 'duration_ms' }");
    expect(src).toContain('className="segmented sg-sm"');
    expect(src).not.toMatch(/chartTall|toggleChartTall|⌄ expand|⌃ shrink/);
    const storage = readFileSync(resolve(__dirname, '../../lib/storage.ts'), 'utf8');
    expect(storage).not.toContain('tracesChartTall');
  });
});
