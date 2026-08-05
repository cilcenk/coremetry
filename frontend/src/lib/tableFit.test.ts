import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { FIT_CONTENT_BUDGET, fixedBudget } from './tableFit';

// v0.9.662 — `is-fit` bir İDDİA, stil tercihi değil.
//
// `.table-wrap.is-fit` iç kaydırma konteynerini kaldırıyor (yapışkan başlık
// ancak öyle mümkün). Bedeli: sığmayan tablo artık kendi kutusunda
// kaydırılmıyor, taşmasını SAYFAYA sızdırıyor — operatör bunu "tablolar
// kaymış" diye bildirdi (v0.9.660).
//
// Bu yüzden is-fit verilen her tablonun bütçesi ÖLÇÜLMELİ. Kolonlar zamanla
// eklenir (Users'ta v0.8.403 + v0.8.450 + custom role, her biri masum) ve
// bir gün sessizce ekranı aşar. Kapı olmadan bunu kimse görmez.

const PAGES = ['../pages/Clusters.tsx', '../pages/AdminClickhouse.tsx'];

const read = (p: string) => readFileSync(resolve(__dirname, p), 'utf8');

// stripLineComments — yorumları atar. Bu dosyanın aradığı kalıplar
// ("table-wrap", "width:") sayfa yorumlarında da geçiyor; yorumlu tarama
// kendi açıklamasını kod sanar (bu kod tabanında dört kez ısırdı).
function stripLineComments(src: string): string {
  return src.split('\n').map(l => {
    const i = l.indexOf('//');
    return i < 0 ? l : l.slice(0, i);
  }).join('\n');
}

type ColSet = { name: string; cols: { width?: number; flex?: boolean }[] };

// parseColSets — `const X_COLS … = [ … ];` bloklarındaki kolonları okur.
// Genişlik ve flex dışındaki her şey burada ilgisiz.
function parseColSets(src: string): ColSet[] {
  const out: ColSet[] = [];
  for (const m of src.matchAll(/const (\w*COLS\w*)\s*(?::[^=]+)?=\s*\[([\s\S]*?)\n\];/g)) {
    const cols = [...m[2].matchAll(/\{\s*id:\s*'[^']+'[\s\S]*?\}/g)].map(c => ({
      width: /width:\s*(\d+)/.exec(c[0]) ? Number(/width:\s*(\d+)/.exec(c[0])![1]) : undefined,
      flex: /flex:\s*true/.test(c[0]) || undefined,
    }));
    out.push({ name: m[1], cols });
  }
  return out;
}

describe('is-fit tabloları bütçeye sığıyor', () => {
  for (const page of PAGES) {
    const src = stripLineComments(read(page));
    const sets = parseColSets(src);

    // Ayrıştırıcı sessizce boş dönerse test hiçbir şey ölçmeden GEÇER —
    // testin en tehlikeli hâli. Önce ayrıştırıcıyı doğrula.
    it(`${page}: kolon kümeleri ayrıştırılabiliyor`, () => {
      expect(sets.length).toBeGreaterThan(0);
      for (const s of sets) expect(s.cols.length).toBeGreaterThan(0);
    });

    for (const s of sets) {
      it(`${page} ${s.name}: sabit bütçe ${FIT_CONTENT_BUDGET}px'e sığıyor`, () => {
        expect(fixedBudget(s.cols)).toBeLessThanOrEqual(FIT_CONTENT_BUDGET);
      });

      // flex kolon, sabit toplam bütçeyi doldurduğunda 0'a çöker ve kolon
      // ekrandan TAMAMEN kaybolur (v0.9.660 Team kolonu). Emici varsa ona
      // gerçek bir pay kalmalı.
      if (s.cols.some(c => c.flex)) {
        it(`${page} ${s.name}: esneyen kolona pay kalıyor`, () => {
          const nFlex = s.cols.filter(c => c.flex).length;
          const spare = FIT_CONTENT_BUDGET - fixedBudget(s.cols);
          expect(spare / nFlex).toBeGreaterThanOrEqual(100);
        });
      }
    }
  }
});

describe('is-fit yalnız ölçülmüş tablolarda', () => {
  // Ölçülmüş tablo = colgroup'lu tablo (bildirilen kolon genişlikleri).
  // Colgroup'suz auto-layout tablolar bütçelenmiyor: min-content
  // genişlikleri içerikten geliyor ve ölçülemiyor, o yüzden iç kaydırma
  // güvenlik ağı onlarda KALIYOR.
  for (const page of PAGES) {
    const lines = read(page).split('\n');
    it(`${page}: colgroup'lu her wrap is-fit`, () => {
      const missing: number[] = [];
      lines.forEach((l, i) => {
        if (!l.includes('className="table-wrap"')) return;
        const hasColgroup = lines.slice(i, i + 8).some(x => x.includes('Colgroup'));
        if (hasColgroup) missing.push(i + 1);
      });
      expect(missing).toEqual([]);
    });
  }
});

describe('fixedBudget', () => {
  it('flex kolonları saymıyor', () => {
    expect(fixedBudget([{ width: 100 }, { width: 50, flex: true }])).toBe(100);
  });
  it('genişliksiz kolonu 0 sayıyor', () => {
    expect(fixedBudget([{ width: 100 }, {}])).toBe(100);
  });
  it('boş küme 0', () => {
    expect(fixedBudget([])).toBe(0);
  });
});

describe('kurtuluş yolu', () => {
  const dt = readFileSync(resolve(__dirname, '../components/DataTable.tsx'), 'utf8');
  // is-fit iç kaydırmayı kaldırdığı için, sürüklenmiş bir genişlik tabloyu
  // taşırırsa sayfa kayar. Geri dönüş yolu tutamağın KENDİSİNDE olmalı —
  // hatanın yapıldığı yerde.
  it('resize tutamağı çift tıkla sıfırlıyor', () => {
    const i = dt.indexOf('export function ColResizeHandle');
    const body = dt.slice(i, i + 700);
    expect(body).toContain('onDoubleClick');
    expect(body).toContain('resetLayout()');
  });
  it('tutamağın title\'ı bunu söylüyor', () => {
    expect(dt).toContain('double-click to reset all column widths');
  });
});
