// v0.10.523 — tek arama terimi: liste/şerit/sayım/RED aynı şeyi sorar.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { effectiveTraceSearch } from './traceSearchTerm';

describe('effectiveTraceSearch', () => {
  it('arama kutusu önce; kimlik kutusundaki 32-hex olmayan değer yedek; 32-hex id terim değil', () => {
    expect(effectiveTraceSearch({ search: ' POST /x ', traceId: 'abc' })).toBe('POST /x');
    expect(effectiveTraceSearch({ search: '', traceId: ' 0301010778 ' })).toBe('0301010778');
    expect(effectiveTraceSearch({ search: '', traceId: '0123456789abcdef0123456789abcdef' })).toBeUndefined();
    expect(effectiveTraceSearch({ search: '', traceId: '' })).toBeUndefined();
    expect(effectiveTraceSearch({})).toBeUndefined();
  });
  it('kaynak pini: Traces.tsx dört yüzeyde de effectiveTraceSearch kullanır, ham filter.search hiçbir isteğe gitmez', () => {
    const src = readFileSync(resolve(__dirname, '../pages/Traces.tsx'), 'utf8');
    expect((src.match(/search: effectiveTraceSearch\(filter\)/g) ?? []).length).toBeGreaterThanOrEqual(4);
    expect(src).not.toMatch(/search: filter\.search \|\| /);
    expect(src).toContain("stripScope([...chartFilters, ...groupLeaves(grouped ? advGroup : null)], effectiveTraceSearch(filter) ?? '')");
    // şerit effect'i kimlik kutusunu da izler
    expect(src).toMatch(/\[view, listRangeNs, filter\.service, filter\.search, filter\.traceId, filter\.rootOnly/);
  });
});
