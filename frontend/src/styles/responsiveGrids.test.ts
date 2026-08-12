// responsiveGrids — dar ekran denetimi D8 kapısı (v0.9.989)
//
// Ne çiviliyor: N EŞİT kolonluk bir ızgara (`'1fr 1fr'`,
// `'repeat(4, 1fr)'`) satır-içi stilde bildirilemez — paylaşılan
// `.grid-2` … `.grid-5` sınıflarını kullanmak zorunda.
//
// Neden: satır-içi stil bir `@media` kuralını HER ZAMAN yener. Depoda
// 32 yerde eşit-kesirli ızgara satır içindeydi ve bu yüzden dar ekranda
// DARALTILAMIYORDU: telefonda dört KPI karosu ~85px'e sıkışıyor, üç
// alanlık form satırı okunamaz hale geliyordu. Sınıfa taşındıkları anda
// `.ov-kpis` ailesinin yerleşik basamakları (1024 → 2 kolon, 640 → 1
// kolon) onlara da uygulanıyor.
//
// KURAL KENDİNİ BOŞALTIYOR, izin listesi YOK. Kapsam BİLEREK yalnız
// EŞİT kesirler:
//   • `'220px 1fr'` / `'auto 1fr'` bir SATIR düzeni (etiket + içerik),
//     `.grid-N` onu bozar;
//   • `'2fr 1fr'` / `'1.4fr 1fr'` kasıtlı bir ORAN, eşitlemek tasarımı
//     değiştirir.
// İkisi de "mekanik ızgara borcu" değil, ayrı birer düzen kararı —
// kapının onları bulgu sayması, gerçek bulguyu gürültüye gömerdi.
//
// Kapı MUTASYONLA doğrulandı: `pages/Slos.tsx`e `gridTemplateColumns:
// '1fr 1fr'` geri konduğunda kırmızıya döndüğü görüldükten sonra
// gemiye alındı.
import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { join, resolve } from 'node:path';

const SRC = resolve(__dirname, '..');
const CSS = readFileSync(join(__dirname, 'globals.css'), 'utf8');

function tsxFiles(dir: string, out: string[] = []): string[] {
  for (const e of readdirSync(dir)) {
    if (e === 'node_modules') continue;
    const p = join(dir, e);
    if (statSync(p).isDirectory()) tsxFiles(p, out);
    else if (/\.tsx$/.test(e) && !/\.test\.tsx$/.test(e)) out.push(p);
  }
  return out;
}

// equalFractionCols — değer N EŞİT kesirden mi oluşuyor (N ≥ 2)?
// `repeat(N, 1fr)` ve `1fr 1fr …` iki yazımı da aynı şey.
export function equalFractionCols(value: string): number | null {
  const v = value.trim();
  const rep = v.match(/^repeat\(\s*(\d+)\s*,\s*1fr\s*\)$/);
  if (rep) {
    const n = Number(rep[1]);
    return n >= 2 ? n : null;
  }
  const parts = v.split(/\s+/);
  if (parts.length >= 2 && parts.every(p => p === '1fr')) return parts.length;
  return null;
}

function lineOf(src: string, idx: number): number {
  return src.slice(0, idx).split('\n').length;
}

function findings(): string[] {
  const bad: string[] = [];
  // İmza çalışma anında kuruluyor ki bu dosyanın kendi düzyazısı
  // eşleşmesin (depoda yedi kez ısıran tuzak).
  const PROP = 'grid' + 'TemplateColumns:';
  for (const p of tsxFiles(SRC)) {
    const src = readFileSync(p, 'utf8');
    let i = 0;
    while ((i = src.indexOf(PROP, i)) !== -1) {
      // Bildirimin değer kısmı: satır sonuna ya da bir sonraki
      // özelliğe kadar. Üçlü operatör (`cond ? 'a' : 'b'`) de burada
      // kalır — İKİ dalı da yoklamak için tüm literaller taranıyor.
      const tail = src.slice(i + PROP.length, i + PROP.length + 200).split('\n')[0];
      for (const m of tail.matchAll(/'([^']+)'/g)) {
        if (equalFractionCols(m[1]) !== null) {
          bad.push(`${p.slice(SRC.length + 1)}:${lineOf(src, i)} ${m[1]}`);
        }
      }
      i += PROP.length;
    }
  }
  return bad;
}

describe('D8 — eşit-kesirli ızgaralar paylaşılan sınıfta', () => {
  it('satır-içi eşit-kesirli gridTemplateColumns kalmadı', () => {
    expect(
      findings(),
      'satır-içi eşit-kesirli ızgara — .grid-2/.grid-3/.grid-4/.grid-5 kullan (satır içi stil @media\'yi yener)',
    ).toEqual([]);
  });

  it('paylaşılan sınıflar tanımlı ve minmax(0,1fr) kullanıyor', () => {
    for (const n of [2, 3, 4, 5]) {
      const re = new RegExp(`\\.grid-${n}\\s*\\{[^}]*grid-template-columns:\\s*repeat\\(${n},\\s*minmax\\(0,\\s*1fr\\)\\)`);
      expect(CSS, `.grid-${n} tanımı kayboldu ya da minmax(0,1fr) değil`).toMatch(re);
    }
  });

  it('iki daraltma basamağı da mevcut (1024 → 2 kolon, 640 → 1 kolon)', () => {
    const b1024 = /@media \(max-width: 1024px\) \{([\s\S]*?)\n\}/.exec(CSS);
    const b640 = /@media \(max-width: 640px\) \{([\s\S]*)/.exec(CSS);
    expect(b1024, '1024 katmanı kayboldu').toBeTruthy();
    expect(b640, '640 katmanı kayboldu').toBeTruthy();
    expect(b1024![1]).toMatch(/\.grid-3, \.grid-4, \.grid-5\s*\{[^}]*repeat\(2, minmax\(0, 1fr\)\)/);
    expect(b640![1]).toMatch(/\.grid-2, \.grid-3, \.grid-4, \.grid-5\s*\{[^}]*minmax\(0, 1fr\)/);
  });

  it('sınıflar gerçekten KULLANILIYOR (ölü şema değil)', () => {
    const used = new Set<string>();
    for (const p of tsxFiles(SRC)) {
      const src = readFileSync(p, 'utf8');
      for (const m of src.matchAll(/['"]grid-([2-5])['"]/g)) used.add(m[1]);
    }
    expect([...used].sort(), '.grid-N sınıflarının bir kısmı hiç kullanılmıyor').toEqual(['2', '3', '4', '5']);
  });

  it('equalFractionCols yalnız EŞİT kesirleri sayıyor', () => {
    const CASES: [string, number | null][] = [
      ['1fr 1fr', 2],
      ['1fr 1fr 1fr', 3],
      ['repeat(4, 1fr)', 4],
      ['repeat(5, 1fr)', 5],
      ['repeat(1, 1fr)', null],   // tek kolon — daraltacak bir şey yok
      ['1fr', null],
      ['2fr 1fr', null],          // kasıtlı oran
      ['1.4fr 1fr', null],        // kasıtlı oran
      ['auto 1fr', null],         // etiket + içerik satırı
      ['220px 1fr', null],        // sabit + içerik satırı
      ['180px repeat(4, 1fr)', null],
      ['repeat(auto-fit, minmax(200px, 1fr))', null],
      ['1fr 90px 60px', null],
    ];
    CASES.forEach(([v, want]) => expect(equalFractionCols(v), v).toBe(want));
  });
});
