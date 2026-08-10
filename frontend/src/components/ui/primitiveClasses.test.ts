import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync } from 'node:fs';
import { resolve, join } from 'node:path';

// primitiveClasses.test.ts — v0.9.884 (tutarlılık denetimi Dalga 3, T1+T2).
//
// KAPATTIĞI KAPI BOŞLUĞU — Dalga 3'ün TEK büyük riski (plan R1):
// bir primitif `variant`/`size` haritasına yeni bir satır eklenir, karşılığı
// CSS kuralı AYNI COMMIT'te gelmezse buton element-seviyesi `button {}`
// kuralına düşer ve **dolu mavi primary** olur. Yıkıcı bir "Sil" butonu
// birdenbire sayfanın en davetkâr öğesi hâline gelir. `tsc` sessiz,
// `eslint` sessiz, `make audit`'te buton kuralı yok.
//
// NEDEN `undefinedCssRefs` BUNU GÖREMİYOR: o kapı `className="literal"`
// tarar. Primitiflerin bastığı sınıf literal DEĞİL — `variantClass[variant]`
// ile ÇALIŞMA ANINDA hesaplanır. Yani haritanın içi statik taramanın kör
// noktası; bu kapının tek varlık sebebi orası.
//
// TASARIM — kural KAYNAKTAN türetilir, burada LİSTELENMEZ. Haritalar
// `components/ui/*.tsx`ten okunur; yeni bir primitif eklendiğinde kapı onu
// kendiliğinden tarar. Elle yazılmış bir sınıf listesi olsaydı bu test kendi
// düzyazısını doğrulardı — bu depoda altı kez ısıran tuzak.

const UI  = __dirname;
const SRC = resolve(__dirname, '../..');

const stripComments = (s: string) =>
  s.replace(/\/\*[\s\S]*?\*\//g, '').split('\n').map(l => l.replace(/\/\/.*$/, '')).join('\n');

// ── CSS'te TANIMLI sınıflar ───────────────────────────────────────────────
function cssFiles(dir: string, out: string[] = []): string[] {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, e.name);
    if (e.isDirectory()) cssFiles(p, out);
    else if (p.endsWith('.css')) out.push(p);
  }
  return out;
}
const CSS = cssFiles(SRC).map(p => stripComments(readFileSync(p, 'utf8'))).join('\n');
const definedClasses = new Set<string>(
  [...CSS.matchAll(/\.(-?[A-Za-z_][\w-]*)/g)].map(m => m[1]),
);

// ── Primitiflerin BASTIĞI sınıflar ────────────────────────────────────────
// `.test.` içeren dosyalar dışarıda: fixture'lar uydurma sınıf basar.
const TEST_MARK = '.' + 'test' + '.';
const primitives = readdirSync(UI)
  .filter(f => f.endsWith('.tsx') && !f.includes(TEST_MARK))
  .sort();

/**
 * Bir primitifin ÇALIŞMA ANINDA basabildiği bütün sınıflar. İki kaynak:
 *   1. `const xClass: Record<Foo, string> = { a: 'cls', … }` haritaları,
 *   2. `const classes = [ 'taban', xClass[…], koşul ? 'x' : '' ]` dizisi —
 *      taban sınıf (`btn-icon`) yalnız burada geçer, haritada geçmez.
 */
function emittedClasses(src: string): string[] {
  const out: string[] = [];
  const body = stripComments(src);
  for (const blk of body.matchAll(/Record<[^>]*,\s*string>\s*=\s*\{([\s\S]*?)\n\}/g))
    for (const m of blk[1].matchAll(/:\s*'([^']*)'/g))
      if (m[1]) out.push(m[1]);
  for (const blk of body.matchAll(/const classes\s*=\s*\[([\s\S]*?)\n\s*\]/g))
    for (const m of blk[1].matchAll(/'([^']*)'/g))
      if (m[1]) out.push(m[1]);
  return out;
}

describe('primitiveClasses — atomun bastığı her sınıfın CSS karşılığı var', () => {
  it('en az bir primitif taranıyor (kapı boşa dönmüyor)', () => {
    // Dosya adı deseni kayarsa (ör. ui/ klasörü bölünürse) bu kapı sessizce
    // 0 dosya tarayıp yeşil kalırdı. Boş taramayı hata sayıyoruz.
    expect(primitives.length).toBeGreaterThan(5);
  });

  it('variant/size haritalarındaki hiçbir sınıf CSS\'te karşılıksız değil', () => {
    const offenders: string[] = [];
    for (const f of primitives) {
      const src = readFileSync(join(UI, f), 'utf8');
      for (const cls of emittedClasses(src)) {
        if (/\s/.test(cls)) {
          offenders.push(`ui/${f}: '${cls}' çok-token — birleştirme/ayrıştırma bozulur`);
        } else if (!definedClasses.has(cls)) {
          offenders.push(`ui/${f}: .${cls} CSS'te YOK → element-seviyesi button{} kuralına düşer (dolu mavi primary)`);
        }
      }
    }
    expect(offenders, `Karşılıksız primitif sınıfı:\n${offenders.join('\n')}`).toEqual([]);
  });

  it('Button\'un iki yeni rungu (xs, ghost-danger) gerçekten boyanıyor', () => {
    // Nokta iddia: haritada VAR olmaları yetmez, kuralın `button.` ön ekiyle
    // yazılmış olması gerekir — `.xs` diye serbest bir sınıf, element-seviyesi
    // `button` kuralının özgüllüğünü (0,1,1 vs 0,0,1) yenemezdi.
    const css = stripComments(readFileSync(resolve(SRC, 'styles/globals.css'), 'utf8'));
    expect(css).toMatch(/button\.xs\s*\{/);
    expect(css).toMatch(/button\.ghost-danger\s*\{/);
  });

  it('.spinner.sm bileşik kuralı var — loading butonu büyütmesin (R7)', () => {
    // Bu, `undefinedCssRefs`in yapısal olarak KAÇIRDIĞI durum: Button
    // `className="spinner sm"` basıyor, iki token da ayrı ayrı tanımlı
    // (`.spinner` ve `button.sm`), dolayısıyla o kapı yeşil. Ama BİLEŞİK
    // kural yoksa spinner 14px kalır, `sm` butonun satır kutusu 11px'tir ve
    // buton istek sırasında ZIPLAR. Bileşik seçiciyi ayrıca yokluyoruz.
    const css = stripComments(readFileSync(resolve(SRC, 'styles/globals.css'), 'utf8'));
    expect(css).toMatch(/\.spinner\.sm\s*\{/);
    const btn = stripComments(readFileSync(join(UI, 'Button.tsx'), 'utf8'));
    expect(btn).toMatch(/className="spinner sm"/);
  });
});
