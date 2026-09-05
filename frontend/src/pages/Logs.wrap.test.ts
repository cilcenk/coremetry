// v0.10.417 — log arama denetimi B5: satır sarma anahtarı. Koşullu
// className undefinedCssRefs kapısından kaçar → CSS kuralı burada pinli.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const read = (p: string) => readFileSync(resolve(__dirname, p), 'utf8');

describe('log line wrap toggle (B5)', () => {
  it('Logs.tsx: kalıcı state + araç çubuğu + prop', () => {
    const src = read('./Logs.tsx');
    expect(src).toContain("getRaw('logs.wrap') === '1'");
    expect(src).toContain('aria-pressed={wrapLines}');
    expect(src).toContain('wrap={wrapLines}');
  });
  it('LogTable: mesaj hücresi sınıfı ve title yalnız kapalıyken', () => {
    const src = read('../components/LogTable.tsx');
    expect(src).toContain("className={wrap ? 'lt-wrap' : undefined}");
    expect(src).toContain('title={wrap ? undefined : l.body}');
  });
  it('globals.css: kural tanımlı, .td-full ve telefon kuralı dokunulmamış', () => {
    const css = read('../styles/globals.css');
    expect(css).toContain('.logtbl-dense > tbody > tr > td.lt-wrap {');
    expect(css).toMatch(/tbody td\.td-full \{[^}]*word-break: break-all/);
    // v0.10.420 — telefon kuralı gerçekten pinli (başlık öyle diyordu, gövde bakmıyordu).
    expect(css).toMatch(/\.logtbl-dense > tbody > tr > td \{ white-space: normal/);
  });
});
