// dataTableBarrel.test.ts — v0.10.317 (DataTable audit §12 dilim 7): fiziksel
// taşınma sonrası ESKİ yol kalmaz (shim yok). Kapı: src ağacında
// '@/components/DataTable' / '@/components/ui/VirtualTable' / göreli
// './DataTable' (ui/DataTable dışında) importu → kırmızı; dosyalar yeni yerde.
import { describe, it, expect } from 'vitest';
import { readdirSync, readFileSync, statSync, existsSync } from 'node:fs';
import { join, resolve } from 'node:path';

const SRC = resolve(__dirname, '../../..');
function walk(dir: string, out: string[] = []): string[] {
  for (const f of readdirSync(dir)) {
    const p = join(dir, f);
    if (statSync(p).isDirectory()) walk(p, out);
    else if (/\.(ts|tsx)$/.test(f)) out.push(p);
  }
  return out;
}

describe('DataTable barrel — tek yol', () => {
  it('dosyalar ui/DataTable altında; eski yollar yok', () => {
    expect(existsSync(resolve(__dirname, 'DataTable.tsx'))).toBe(true);
    expect(existsSync(resolve(__dirname, 'VirtualTable.tsx'))).toBe(true);
    expect(existsSync(resolve(SRC, 'components/DataTable.tsx'))).toBe(false);
    expect(existsSync(resolve(SRC, 'components/ui/VirtualTable.tsx'))).toBe(false);
  });
  it('hiçbir dosya eski yoldan içe aktarmaz', () => {
    const bad: string[] = [];
    for (const p of walk(SRC)) {
      // Kapı kendi metnini ısırmasın: bu dosya iğneleri literal taşır.
      if (p.endsWith('dataTableBarrel.test.ts')) continue;
      const s = readFileSync(p, 'utf8');
      const rel = p.slice(SRC.length + 1);
      if (s.includes("from '@/components/DataTable'") || s.includes("from '@/components/ui/VirtualTable'")) bad.push(rel);
      if (!rel.startsWith('components/ui/DataTable/') && /from '\.\.?\/(\.\.\/)*(components\/)?(DataTable|VirtualTable)'/.test(s)) bad.push(rel);
    }
    expect(bad).toEqual([]);
  });
});
