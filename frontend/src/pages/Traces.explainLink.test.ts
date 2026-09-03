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
