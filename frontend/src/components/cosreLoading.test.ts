// v0.10.152 (operatör) — CoSRE Explain yüklenirken gövde ortasında büyük,
// OTel işaretli yükleniyor durumu (küçük köşe spinner'ı değil). Kaynak taraması.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

describe('CoSRE explain loading state (v0.10.152)', () => {
  it('uses LoaderMark (lg) centered while waiting for the first token', () => {
    const src = readFileSync(join(__dirname, 'CopilotExplain.tsx'), 'utf8').replace(/^\s*\/\/.*$/gm, '');
    expect(src).toMatch(/auto && busy && text === null && \(/);
    expect(src).toMatch(/<LoaderMark size="lg"/);
    expect(src).not.toMatch(/<Spinner label=\{includeCode/);
  });
  it('PageLoader reuses LoaderMark (single mark, no drift)', () => {
    const src = readFileSync(join(__dirname, 'Spinner.tsx'), 'utf8').replace(/^\s*\/\/.*$/gm, '');
    expect((src.match(/opentelemetry\.svg/g) ?? []).length).toBe(1);
    expect(src).toMatch(/<LoaderMark label=\{label \?\? 'Loading'\} \/>/);
  });
});
