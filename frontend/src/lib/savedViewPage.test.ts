import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { resolve, join } from 'node:path';
import { savedViewPage } from './actions';

// v0.9.1048 (Faz 0.7) — ⌘K "Save current view" page anahtarını artık
// pathname'den TÜRETMEZ, haritadan alır. Eski türetme SavedViewsBar'ı
// olmayan 31 rotada görünmez kayıt yazıyordu; /databases/slow-queries
// için "databases/slow-queries" yazarken bar "slowqueries" okuyordu —
// "kaydedildi" diyordu, kayıt hiçbir yerde görünmüyordu.
//
// İkinci test kaynak taramasıdır: her <SavedViewsBar page="X"> mount'u
// haritada bir DEĞER olarak bulunmalı. Yeni bar ekleyen haritayı da
// güncellemek zorunda kalır — harita ile mount listesi sessizce ayrışamaz.

describe('savedViewPage (v0.9.1048)', () => {
  it('bilinen rotalar bar anahtarına gider; slow-queries tuzağı dahil', () => {
    expect(savedViewPage('/traces')).toBe('traces');
    expect(savedViewPage('/databases/slow-queries')).toBe('slowqueries');
    expect(savedViewPage('/databases')).toBe('databases');
    expect(savedViewPage('/problems')).toBe('problems');
    expect(savedViewPage('/logs/')).toBe('logs'); // sondaki / toleransı
  });

  it("bar'ı olmayan rota null → kayıt YAZILMAZ", () => {
    expect(savedViewPage('/services')).toBeNull();
    expect(savedViewPage('/service')).toBeNull();
    expect(savedViewPage('/clusters')).toBeNull();
    expect(savedViewPage('/')).toBeNull();
  });

  it('her SavedViewsBar mount anahtarı haritada bir değer', () => {
    const SRC = resolve(__dirname, '..');
    const mounts = new Set<string>();
    const walk = (dir: string) => {
      for (const f of readdirSync(dir)) {
        const p = join(dir, f);
        if (statSync(p).isDirectory()) { walk(p); continue; }
        if (!f.endsWith('.tsx')) continue;
        for (const m of readFileSync(p, 'utf8').matchAll(/<SavedViewsBar\s+page="([^"]+)"/g)) {
          mounts.add(m[1]);
        }
      }
    };
    walk(SRC);
    expect(mounts.size).toBeGreaterThanOrEqual(10); // tarama kapsam kaybetmesin
    const mapped = new Set(
      ['/traces', '/logs', '/explore', '/inbox', '/endpoints', '/databases',
        '/databases/slow-queries', '/messaging', '/problems', '/anomalies']
        .map(p => savedViewPage(p)));
    for (const key of mounts) {
      expect(mapped.has(key), `bar page="${key}" haritada yok — ⌘K bu sayfada yetim kayıt yazar`).toBe(true);
    }
  });
});
