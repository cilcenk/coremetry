import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

// v0.10.440 (log arama denetimi B3) — sayfa-yerel ↻ + tazelik etiketi:
// sayaç memo bağımlılığında (pencere gerçekten hareket eder), tık cursor'ı
// sıfırlar (v0.7.81), canlı kuyrukta çizilmez, özel aralıkta yalnız
// refetch, otomatik aralık YOK, etiket pencere sonundan (fetch anı değil).
describe('Logs ↻ refresh (B3)', () => {
  const src = readFileSync(resolve(__dirname, 'Logs.tsx'), 'utf8');
  it('nowTick drives the window memo', () => {
    expect(src).toContain('[useTimeRange, range, nowTick]');
    expect(src).toContain('const [nowTick, setNowTick] = useState(0);');
  });
  it('the handler resets paging, refetches on custom, ticks on presets, hidden while live', () => {
    const i = src.indexOf('aria-label="Yenile"');
    expect(i).toBeGreaterThan(0);
    const block = src.slice(i - 200, i + 600);
    expect(block).toContain('{!live && (');
    expect(block).toContain('resetPaging();');
    expect(block).toContain("range.preset === 'custom'");
    expect(block).toContain('staticQ.refetch()');
    expect(block).toContain('setNowTick(t => t + 1)');
    expect(block).toContain('fmtClock(to / 1e6)');
  });
  it('Search always re-queries (v0.10.447): apply ticks on presets, refetches on custom', () => {
    const i = src.indexOf('const apply = () => {');
    expect(i).toBeGreaterThan(0);
    const block = src.slice(i, i + 400);
    expect(block).toContain('setNowTick(t => t + 1)');
    expect(block).toContain("range.preset === 'custom'");
    expect(block).toContain('staticQ.refetch()');
  });
  it('never adds an automatic interval around the tick', () => {
    expect(src).not.toMatch(/setInterval\([^)]*setNowTick/);
  });
});
