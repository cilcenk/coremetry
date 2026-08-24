import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { resolve, join } from 'node:path';

// anchorVariantGate — v0.9.1372.
//
// Buton varyantları `globals.css`te İKİ yazımla yaşıyor: `button.sec` ve
// `a.sec`. Sebebi anatomik — bir <a> `button` taban kuralını miras almaz,
// dolayısıyla anchor ikizinin radius/padding/font'u KENDİ yazması gerekir.
//
// Bu ikizlik sessizce bozulabilen türden: eksik yarı hata vermez, sınıf
// adı geçerlidir, React uyarmaz, tsc göremez, tarayıcı da bilinmeyen bir
// sınıfı yok sayar. Görünen tek şey, düğme olması gereken şeyin düz metin
// gibi durmasıdır — ve bunu ancak o ekrana bakan operatör fark eder.
//
// İki kez oldu:
//   • v0.9.1210 — `a.sec` yoktu; EndpointDetail'in "Traces → / Explore →"
//     pivotları düz link gibi görünüyordu (operatör: "çok belli olmuyor").
//   • v0.9.1372 — aynı tuzağın eşiğine gelindi: operatör pivotların mavi
//     olmasını istedi, `.accent` ise YALNIZ `button.accent` olarak
//     tanımlıydı. `<Link className="accent">` yazmak hiçbir şey
//     yapmayacaktı ve gerekçe ("accent zaten var") doğru görünecekti.
//
// KAPI: bir anchor'a verilen her varyant sınıfı için `a.<sınıf>` kuralı
// globals.css'te BULUNMALI. Kapı ikinci yarıyı hatırlatır; ne yapacağını
// değil, var olup olmadığını denetler.
const SRC = resolve(__dirname, '..');
const CSS = readFileSync(resolve(SRC, 'styles', 'globals.css'), 'utf8');

// Yalnız BUTON varyantları — düzen/yardımcı sınıfları (`mono`, `badge`,
// `tab`, sayfaya özel adlar) bu sözleşmenin dışında; onların anchor
// ikizine ihtiyacı yok. Liste `button.<x>` kurallarından TÜRETİLİYOR,
// elle yazılmıyor: yeni bir varyant eklendiğinde kapı onu kendiliğinden
// kapsar.
const buttonVariants = new Set(
  [...CSS.matchAll(/^button\.([a-z][\w-]*)\s*[,{]/gm)].map(m => m[1]),
);

function walk(dir: string, out: string[] = []): string[] {
  for (const e of readdirSync(dir)) {
    const p = join(dir, e);
    if (statSync(p).isDirectory()) walk(p, out);
    else if (/\.tsx$/.test(p) && !/\.test\.tsx$/.test(p)) out.push(p);
  }
  return out;
}

/** Bir kaynak metninde <Link>/<a> üzerine konmuş varyant sınıfları. */
export function anchorVariantClasses(src: string, variants: Set<string>): string[] {
  const found = new Set<string>();
  for (const m of src.matchAll(/<(?:Link|a)\b[^>]*?className="([^"{]*)"/g)) {
    for (const cls of m[1].split(/\s+/)) if (variants.has(cls)) found.add(cls);
  }
  return [...found];
}

describe('anchorVariantClasses — yüklem (sentetik)', () => {
  const V = new Set(['sec', 'accent', 'danger']);
  const cases: Array<[string, string, string[]]> = [
    ['Link üzerinde varyant', '<Link className="accent" to="/x">y</Link>', ['accent']],
    ['a üzerinde varyant', '<a className="sec" href="/x">y</a>', ['sec']],
    ['button KAPSAM DIŞI', '<button className="accent">y</button>', []],
    ['varyant olmayan sınıf yok sayılır', '<Link className="mono">y</Link>', []],
    ['karışık sınıf listesi', '<Link className="mono accent">y</Link>', ['accent']],
    ['başka öznitelik araya girer', '<Link to="/x" style={{a:1}} className="sec">y</Link>', ['sec']],
  ];
  for (const [name, src, want] of cases) {
    it(name, () => expect(anchorVariantClasses(src, V).sort()).toEqual(want.sort()));
  }
});

describe('anchor varyant ikizi kapısı (v0.9.1372)', () => {
  it('globals.css gerçekten button varyantı tanımlıyor — boş küme tuzağı', () => {
    // Regex bir gün kayarsa küme boşalır ve kapı HER ŞEYİ geçirir;
    // yeşil kalarak. Bilinen iki varyant burada çivili.
    expect(buttonVariants.size).toBeGreaterThanOrEqual(3);
    expect(buttonVariants.has('sec')).toBe(true);
    expect(buttonVariants.has('accent')).toBe(true);
  });

  it('anchor üzerinde kullanılan her varyantın a.<sınıf> ikizi VAR', () => {
    const used = new Set<string>();
    for (const abs of walk(SRC)) {
      for (const c of anchorVariantClasses(readFileSync(abs, 'utf8'), buttonVariants)) used.add(c);
    }
    expect(used.size).toBeGreaterThan(0); // ağaç gerçekten tarandı
    const missing = [...used].filter(c => !new RegExp(`^a\\.${c}\\s*[,{]`, 'm').test(CSS));
    expect(missing).toEqual([]);
  });

  it('olmayan bir varyant için ikiz YOK — negatif kontrol', () => {
    // Kapının yüklemi gerçekten CSS'e bakıyor mu? Uydurma bir sınıf
    // adının ikizi bulunmamalı; bulunuyorsa eşleşme fazla gevşektir.
    expect(/^a\.zzz-yok\s*[,{]/m.test(CSS)).toBe(false);
  });
});
