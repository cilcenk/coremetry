import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { msSyncKey, CHART_SYNC_NAMESPACE } from './syncNamespace';

describe('msSyncKey (v0.10.289)', () => {
  it('tabana motor ad alanını ekler; boş taban senkronsuz', () => {
    expect(msSyncKey('podjmx:api-1')).toBe('podjmx:api-1-ms');
    expect(msSyncKey('  x ')).toBe('x-ms');
    expect(msSyncKey('')).toBeUndefined();
    expect(msSyncKey(undefined)).toBeUndefined();
    expect(msSyncKey(null)).toBeUndefined();
    expect(CHART_SYNC_NAMESPACE).toBe('-ms');
  });
});

// Kaynak kapısı: `-ms` ad alanı src/ içinde YALNIZ syncNamespace.ts'te
// yazılır. Elle yazılmış bir ek, helper'ın bir gün değişmesiyle (hat B
// emekliliği) sessizce ayrışır — hata yok, imleç sadece geçmez.
function walk(dir: string, out: string[]) {
  for (const n of readdirSync(dir)) {
    const p = join(dir, n);
    if (statSync(p).isDirectory()) walk(p, out);
    else if (/\.(ts|tsx)$/.test(n) && !/\.test\.tsx?$/.test(n)) out.push(p);
  }
}
describe('-ms ad alanı tek yerde', () => {
  it('src/ altında syncNamespace.ts dışında `-ms` sync literal\'i yok', () => {
    const root = resolve(__dirname, '../..');
    const files: string[] = [];
    walk(root, files);
    const offenders: string[] = [];
    for (const f of files) {
      if (f.endsWith('syncNamespace.ts')) continue;
      const src = readFileSync(f, 'utf8').replace(/\/\/.*$/gm, '').replace(/\/\*[\s\S]*?\*\//g, '');
      // syncKey bağlamında -ms: `...-ms` şablon sonu, "…-ms" dize, `${x}-ms`
      if (/syncKey\s*=\s*(\{[^}]*)?["'`][^"'`]*-ms["'`]/.test(src) || /\$\{[^}]+\}-ms`/.test(src) || /["'`][a-z-]*-ms["'`]\s*;/.test(src)) {
        offenders.push(f.slice(root.length + 1));
      }
    }
    expect(offenders, 'elle yazılmış -ms ad alanı').toEqual([]);
  });
});
