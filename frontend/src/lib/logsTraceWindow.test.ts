import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { logsUseTimeRange } from './logsTraceWindow';

// v0.10.690 — Logs sayfası traceId + mutlak aralıkta pencereyi gönderir
// (derin bağlantı), göreli aralıkta göndermez (sunucu türetir).
describe('logsUseTimeRange (v0.10.690)', () => {
  it('traceId yok → aralık her zaman', () => {
    expect(logsUseTimeRange('', { preset: '30m' })).toBe(true);
  });
  it('traceId + custom mutlak → aralık gönderilir', () => {
    expect(logsUseTimeRange('abc', { preset: 'custom', fromMs: 1, toMs: 2 })).toBe(true);
  });
  it('traceId + göreli ya da sınırsız custom → gönderilmez', () => {
    expect(logsUseTimeRange('abc', { preset: '30m' })).toBe(false);
    expect(logsUseTimeRange('abc', { preset: 'custom' })).toBe(false);
  });
});

describe('BAĞLANMA (Logs.tsx)', () => {
  const src = readFileSync(resolve(__dirname, '../pages/Logs.tsx'), 'utf8');
  it('useTimeRange kuralı yardımcıdan; eski "aralık her zaman yok sayılır" yazımı yok', () => {
    expect(src).toContain('const useTimeRange = logsUseTimeRange(filter.traceId, range);');
    expect(src).not.toContain('const useTimeRange = !filter.traceId;');
    expect(src).not.toContain('searches across full retention');
  });
});
