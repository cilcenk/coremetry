// Traces.explainLink.test.ts — v0.10.328 kaynak pini: boş-durum linki yalnız
// admin'e, SON liste isteğinin parametreleriyle, yeni sekmede.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const src = readFileSync(resolve(__dirname, 'Traces.tsx'), 'utf8');

describe('Traces teşhis linki', () => {
  it('admin kapısı + son istek parametreleri + TracesEmpty prop', () => {
    expect(src).toContain("authUser?.role === 'admin' ? tracesExplainUrl(lastListParamsRef.current) : null");
    expect(src).toContain('lastListParamsRef.current = listParams;');
    expect(src).toContain('api.traces(listParams, ctl.signal)');
    expect(src).toContain('explainHref={explainHref ?? undefined}');
    expect(src).toMatch(/<a href=\{explainHref\} target="_blank" rel="noreferrer"/);
  });
});

// v0.10.329 — boş liste öz-teşhisi satırı: emptyDiag yanıttan TracesEmpty'ye.
describe('Traces boş liste öz-teşhisi', () => {
  it('matchingSpans prop yanıttan gelir ve iki hâli yazılır', () => {
    expect(src).toContain('matchingSpans={data?.emptyDiag?.matchingSpans}');
    expect(src).toContain('spans in this window match the filter, yet the trace list came back empty');
    // v0.10.341 — çipler arama varken trace düzeyi; metin artık aynı-span iddiası taşımaz.
    expect(src).toContain('nothing in this window matches search + filters (trace-level');
  });
});

// v0.10.530 — "TTL'i aştı" ipucu: karar saf modülde, ham sayım yanıttan
// prop'la gelir; dört dal metinde ayrı yazılır (yüklem dalı Aggregate
// düğmesi TAŞIMAZ — tavsiye aramayı değiştirmektir).
describe('Traces boş-durum nedeni', () => {
  it('serviceSpans prop + tracesEmptyReason + dört dal', () => {
    expect(src).toContain('serviceSpans={data?.emptyDiag?.serviceSpans}');
    expect(src).toContain("import { tracesEmptyReason } from './traces/emptyReason';");
    expect(src).toContain('tracesEmptyReason({ narrowed: !!narrowedFromNs, service, search, mvSpans, serviceSpans })');
    expect(src).toContain("reason === 'predicate'");
    expect(src).toContain('none of them match the search');
    expect(src).toContain("reason === 'aged'");
    expect(src).toContain('the raw spans table holds <b>none</b> for it here');
    expect(src).toContain("reason === 'unmeasured'");
    expect(src).toContain('could not measure whether raw spans');
    expect(src).not.toContain('const aged = service && search');
  });
});
