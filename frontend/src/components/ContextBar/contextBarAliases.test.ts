// contextBarAliases.test.ts — v0.10.250 kaynak-tarama kapısı (audit §12 test 3):
// hiçbir sayfa/bileşen from= / to= / ns= takma adlarını URL'ye YAZMAZ —
// bunlar yalnız girişte okunur (ikinci pencere kanalı = iki yazım kayması
// sınıfı; ns= /clusters çekmecesinin, from= /pod drillFrom'un anahtarı).
// İstisna listesi bilinçli ve dar: /clusters çekmecesi (ns sahibi) ve
// /pod (from = drillFrom kaynağı) — kendi anlamlarıyla yazıyorlar.
import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { join } from 'node:path';

const ROOT = join(__dirname, '..', '..');
const ALLOW = new Set(['pages/Clusters.tsx', 'pages/Pod.tsx']);

function walk(dir: string, out: string[] = []): string[] {
  for (const e of readdirSync(dir)) {
    const p = join(dir, e);
    if (statSync(p).isDirectory()) walk(p, out);
    else if (/\.(ts|tsx)$/.test(e) && !/\.test\.tsx?$/.test(e)) out.push(p);
  }
  return out;
}

describe('URL takma adları yalnız girişte', () => {
  it("hiçbir kaynak set('from'|'to'|'ns', …) yazmaz", () => {
    const bad: string[] = [];
    for (const f of walk(join(ROOT, 'pages')).concat(walk(join(ROOT, 'components')), walk(join(ROOT, 'hooks')), walk(join(ROOT, 'lib')))) {
      const rel = f.slice(ROOT.length + 1);
      if (ALLOW.has(rel)) continue;
      const src = readFileSync(f, 'utf8');
      // Yalnız SAYFA URL'si yazıcıları: setSearchParams güncelleyicileri (next/sp/
      // searchParams) ve rebuildPreserving giriş listeleri. API sorgu dizeleri
      // (api.ts qs/q/p.set) ve /pod drillFrom (podDetailPath q.set) kapsam dışı.
      if (/\b(next|sp|searchParams|usp)\.set\(\s*['"](from|to|ns)['"]\s*,/.test(src)
          || (/rebuildPreserving/.test(src) && /\[\s*['"](from|to|ns)['"]\s*,\s*[^\]]/.test(src))) {
        bad.push(rel);
      }
    }
    expect(bad).toEqual([]);
  });
});
