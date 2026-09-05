// v0.10.418 — log arama denetimi B7: uygulanan arama cümlesi görünür ve
// kopyalanır (compileSearch 12 kullanım, 0 render idi). Kaynak pini.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

describe('effective search clause chip (B7)', () => {
  const src = readFileSync(resolve(__dirname, './Logs.tsx'), 'utf8');
  it('compiledSearch render + CopyButton', () => {
    expect(src).toContain('{compiledSearch && (');
    expect(src).toContain('<CopyButton value={compiledSearch}');
    expect(src).toContain('<code title={compiledSearch}>{compiledSearch}</code>');
  });
  it('pill barının dışında (serbest metinle de görünür)', () => {
    // v0.10.420 — "Clear all"dan sonra olmak yetmez (blok içinde de
    // sonra olur); pill barı bloğunun KAPANIŞINDAN sonra olmalı.
    const chip = src.indexOf('<CopyButton value={compiledSearch}');
    const barClose = src.indexOf('Clear all\n            </Button>\n          </div>\n        )}');
    expect(barClose).toBeGreaterThan(0);
    expect(chip).toBeGreaterThan(barClose);
  });
});
