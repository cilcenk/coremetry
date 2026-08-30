// excPodsLayout.test.ts — v0.10.173 (operatör, prod ekran görüntüsü):
// exception detayında «Pods · nodes» EN ALTTA (stack/sample gridinden sonra)
// ve tablo taşmasız (fixed yerleşim + colgroup; pod/node hücreleri title'lı
// ellipsis — fixed+nowrap+küçük genişlik sessiz kırpma tuzağı).
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const read = (rel: string) => readFileSync(resolve(__dirname, rel), 'utf8');

describe('exception detayı Pods · nodes (v0.10.173)', () => {
  it('panel Sample traces kartından SONRA, PageShell kapanışından önce', () => {
    const src = read('./ProblemDetail.tsx');
    const pods = src.indexOf('<ExceptionPodsPanel');
    const samples = src.indexOf('<h3>Sample traces</h3>');
    const stack = src.indexOf('<h3>Stack trace</h3>');
    expect(pods).toBeGreaterThan(samples);
    expect(pods).toBeGreaterThan(stack);
    expect(src.indexOf('</PageShell>', pods)).toBeGreaterThan(pods);
  });
  it('tablo sabit yerleşimli, colgroup\'lu; pod/node/tarih hücreleri ellipsis sınıfı + title taşır; namespace/cluster ayrı kolon değil', () => {
    const src = read('./ExceptionPodsPanel.tsx');
    expect(src).toContain('<table className="exc-pods-t">');
    expect(src).toContain('<colgroup>');
    expect((src.match(/className="mono exc-pods-cell" title=/g) ?? []).length).toBe(3);
    expect(src).not.toContain('<th>namespace</th>');
    expect(src).toContain('exc-pods-sub');
    const css = read('../../styles/globals.css');
    expect(css).toMatch(/\.exc-pods-t \{ table-layout: fixed;/);
    expect(css).toMatch(/\.exc-pods-cell \{[^}]*text-overflow: ellipsis/);
  });
});
