import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { resolve, join } from 'node:path';

// linkColorGate — v0.9.1379.
//
// `globals.css` zaten diyor ki: `a { color: var(--accent2) }`. Yani
// uygulamanın TEK link rengi var ve hiçbir link onu yeniden yazmak
// zorunda değil. Buna ragmen 15 link `color: 'var(--accent)'` ile
// KENDİ mavisini seçiyordu — ayrı bir token, ayrı bir renk.
//
// Sapmanın görülmemesinin sebebi ölçüldü: redhat temasında --accent ve
// --accent2 AYNI değer (#0066cc). Operatörün görsel yönü redhat, yani
// o temada bakan hiç kimse farkı göremezdi. Fark yalnız dark ve light
// temalarda vardı ve oralarda sapan renk HER ZAMAN daha zayıftı:
//
//   tema    zemin   --accent   --accent2
//   dark    bg1        5.17       6.85
//   dark    bg2        4.55       6.03   ← 4.5 AA tabanının kılpayı üstü
//   light   bg1        5.19       7.59
//   light   bg2        4.88       7.13
//   redhat  her ikisi de eşit
//
// Yani bu sadece tutarlılık değildi: 15 link uygulamanın en zayıf
// kontrastlı linkleriydi ve biri AA tabanına 0.05 uzaklıktaydı.
//
// KAPI: bir <Link>/<a> `var(--accent)` YAZMAZ — o, varsayılandan SAPAN
// ikinci mavi.
//
// Yüklem bir tur daraltıldı. İlk yazımı "link hiç renk yazmasın" diyordu
// ve ağaçta iki meşru kullanımı ihlal saydı:
//
//   • `var(--err)` / `var(--warn)` — ANLAMSAL renkler. "worst error →"
//     linki kırmızıdır çünkü hataya gider; onu maviye zorlamak bilgiyi
//     silerdi. Kapı anlamsal tokenlara karışmaz.
//   • `var(--accent2)` — varsayılanın TEKRARI. Gürültü, kusur değil:
//     29 site böyle yazıyor ve hepsini temizlemek sıfır görsel etkili
//     ayrı bir süpürme. İhlal saymak o süpürmeyi bugün zorlardı; kapı
//     kusuru korur, temizliği değil.
//
// KAPSAM DIŞI, bilinçli: `var(--accent)` link OLMAYAN yerlerde yaşamaya
// devam ediyor (grafik seri renkleri, rozet etiketleri, ◆ glifleri,
// tıklanabilir <span>'ler). Onlar metin-link değil; `a` kuralı onları
// hiç kapsamıyor ve koyu accent oralarda bilinçli bir seçim olabilir.
// Kapı bu yüzden hem ELEMENT TÜRÜNE hem TOKEN ADINA bakıyor.
const SRC = resolve(__dirname, '..');

function walk(dir: string, out: string[] = []): string[] {
  for (const e of readdirSync(dir)) {
    const p = join(dir, e);
    if (statSync(p).isDirectory()) walk(p, out);
    else if (/\.tsx$/.test(p) && !/\.test\.tsx$/.test(p)) out.push(p);
  }
  return out;
}

/**
 * linkColorOverrides — <Link>/<a> AÇILIŞ etiketinin İÇİNDE sapan maviyi
 * (`var(--accent)`) yazan siteler.
 *
 * Saf tutuluyor ki sentetik girdiyle sınanabilsin: kapıyı yalnız canlı
 * ağaçta koşturmak, ağaç yeşilken BOZUK bir kapıyı çalışandan ayırt
 * edilemez kılar.
 */
export function linkColorOverrides(src: string): string[] {
  const out: string[] = [];
  // Açılış etiketini `>` görene kadar topla; çok satırlı JSX yaygın.
  const re = /<(Link|a)\b([^>]*)>/g;
  let m: RegExpExecArray | null;
  while ((m = re.exec(src))) {
    const attrs = m[2];
    // `--accent2` DEĞİL (varsayılanın tekrarı), `--err`/`--warn` DEĞİL
    // (anlamsal). Sonundaki `)` şart: onsuz `--accent2` de eşleşir.
    if (/\bcolor:\s*'var\(--accent\)'/.test(attrs)) out.push(`<${m[1]}> var(--accent)`);
  }
  return out;
}

describe('linkColorOverrides — yüklem (sentetik)', () => {
  const cases: Array<[string, string, boolean]> = [
    ['renk yazan Link ihlal', `<Link to="/x" style={{ color: 'var(--accent)' }}>y</Link>`, false],
    ['sapan mavi yazan a ihlal', `<a href="/x" style={{ color: 'var(--accent)' }}>y</a>`, false],
    // Ayırt edici vakalar — kapı bir tur bunları YANLIŞ bayrakladı.
    ['accent2 (varsayılanın tekrarı) temiz', `<Link to="/x" style={{ color: 'var(--accent2)' }}>y</Link>`, true],
    ['anlamsal --err temiz', `<Link to="/x" style={{ color: 'var(--err)' }}>worst error →</Link>`, true],
    ['anlamsal --warn temiz', `<Link to="/x" style={{ color: 'var(--warn)' }}>N+1 →</Link>`, true],
    ['renk yazmayan Link temiz', `<Link to="/x" style={{ marginLeft: 5 }}>y</Link>`, true],
    ['cok satirli acilis etiketi yakalanir',
      `<Link to="/x"\n  style={{ fontSize: 11, color: 'var(--accent)' }}>y</Link>`, false],
    ['span KAPSAM DISI', `<span style={{ color: 'var(--accent)' }}>y</span>`, true],
    ['grafik seri rengi KAPSAM DISI', `{ label: 'used', color: 'var(--accent)' }`, true],
    ['sade Link temiz', `<Link to="/x">y</Link>`, true],
  ];
  for (const [name, src, clean] of cases) {
    it(name, () => expect(linkColorOverrides(src).length === 0).toBe(clean));
  }
});

describe('link rengi kapısı (v0.9.1379)', () => {
  it('hiçbir <Link>/<a> kendi rengini yazmıyor', () => {
    const offenders: string[] = [];
    for (const abs of walk(SRC)) {
      for (const hit of linkColorOverrides(readFileSync(abs, 'utf8'))) {
        offenders.push(`${abs.slice(SRC.length + 1)}: ${hit}`);
      }
    }
    expect(offenders).toEqual([]);
  });

  it('varsayılan link rengi GERÇEKTEN tanımlı — kapının dayanağı', () => {
    // Kapı "varsayılan zaten doğru" diyor. O kural bir gün düşerse
    // kapı linkleri RENKSİZ bırakır ve yine yeşil kalır.
    const css = readFileSync(resolve(SRC, 'styles', 'globals.css'), 'utf8');
    expect(css).toMatch(/^a \{ color: var\(--accent2\)/m);
  });

  it('ağaç gerçekten tarandı — boş küme tuzağı', () => {
    const withLinks = walk(SRC).filter(p => /<Link\b/.test(readFileSync(p, 'utf8'))).length;
    expect(withLinks).toBeGreaterThan(30);
  });
});
