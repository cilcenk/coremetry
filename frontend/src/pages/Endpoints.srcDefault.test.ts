import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

// v0.10.454 (operatör 2026-09-06) — /endpoints listesi VARSAYILAN SPAN;
// ?src=metric zorlar; kendiliğinden span'a düşme (v0.10.361) kalktı.
// Detay sayfası belirli endpoint için ?src=metric sunar; not dürüst.
describe('endpoints source defaults (v0.10.454)', () => {
  const list = readFileSync(resolve(__dirname, 'Endpoints.tsx'), 'utf8');
  const detail = readFileSync(resolve(__dirname, 'EndpointDetail.tsx'), 'utf8');
  it('list page defaults to span and only explicit ?src=metric forces metric', () => {
    expect(list).toContain("const src: 'span' | 'metric' = entry === 'rpc' ? 'span'\n    : explicitSrc === 'metric' ? 'metric'\n    : 'span';");
    expect(list).not.toContain('autoSpan');
    expect(list).toContain("const metricNote = src === 'metric' ? (rowsQ.data?.note ?? null) : null;");
  });
  it('detail page offers a per-endpoint metric source and labels its scope', () => {
    expect(detail).toContain("params.get('src') === 'metric' && !entry ? 'metric' : 'span'");
    expect(detail).toContain("...(detailSrc === 'metric' ? { src: 'metric' as const } : {})");
    expect(detail).toContain('<option value="metric">Kaynak: metrik</option>');
    expect(detail).toContain('grafikler ve alt bölümler span türevli');
    expect(detail).toContain("next.set('src', 'metric'); else next.delete('src');");
  });
});
