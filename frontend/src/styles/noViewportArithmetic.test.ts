// noViewportArithmetic — sayfa yüksekliğini viewport ARİTMETİĞİYLE
// kuran kalıbın kapısı (v0.9.1000, denetim DALGA 9 kapanışı).
//
// Ne çiviliyor: `height: calc(100vh - Npx)` (ve `min-height` ikizi)
// frontend/src'de YALNIZ gerekçeli allowlist'te bulunabilir.
//
// Neden bu kapı ŞART — bu depoda dört kez ısırdı ve dördünde de hiçbir
// otomatik kontrol görmedi:
//   · `AdminSql.tsx` -80px (v0.9.981/D3.2'de kalktı)
//   · `#td-outer` -185px (dar ekranda v0.9.988/D5.6'da serbest bırakıldı)
//   · `TraceCompare.tsx` -220px (v0.9.1000'de tamamen silindi)
//   · `.metric-list` -250px — `max-height`, yani BU kapının kapsamı
//     DIŞINDA (aşağıda "neden max-height serbest"e bak)
// Ortak hata: N, o günkü topbar + kontrol barı + sekme şeridi toplamına
// ELLE kalibre ediliyor. Kalibrasyon üç yerden birden bayatlıyor —
// `[data-density]` dolgusu dört değer alıyor (6/10/20/22px), ≤640px
// bloğu 12px'e düşürüyor, sayfaya bir satır eklendiğinde de sayı
// olduğu yerde kalıyor. Sonuç sessiz: kutu ya taşıyor ya boşluk
// bırakıyor, tsc/eslint/jsdom hiçbiri bakmıyor.
//
// DOĞRU KALIP (üç örnekte de aynı): kabuk zaten `flex: 1; overflow: auto`
// ve yüksekliği KESİN. Sayfa içinde `min-height: 100%` bir flex kolonu
// kurulur, dolan bölüm `flex: 1; min-height: 0` der. Yükseklik gerçek
// yerleşimden gelir, yoğunluktan ve eşikten bağımsız doğrudur.
//
// NEDEN `max-height` SERBEST: `max-height: calc(100vh - 32px)` bir sayfa
// yerleşimi değil, bir ÜST SINIR — modal/açılır panelin viewport'u
// aşmasını engelliyor (`AdminStatusPage`, `ChannelModal`,
// `LdapUserPicker`, `.metric-list`, `AdminCatalog` tablo kapağı). Yanlış
// kalibre edilirse en fazla panel biraz kısa olur; içerik kaybolmaz,
// çünkü altında `overflow: auto` var. Farklı bir hata sınıfı, farklı
// bir kapı gerektirir — bu kapıya karıştırılmaz.
import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { resolve, join, sep } from 'node:path';

const SRC = resolve(__dirname, '..');
const CSS = readFileSync(join(SRC, 'styles/globals.css'), 'utf8');

// TS/TSX yorum soyucusu — DİZE İÇERİĞİ KORUNUR.
//
// İki tuzak birden var ve ikisi de bu depoda ölçüldü:
//   (a) naif `/\/\*[\s\S]*?\*\//` bir `//` satır yorumunun içindeki `/*`
//       dizisini blok başlangıcı sanıp dosyanın kalanını yutuyor
//       (zLayers, v0.9.980 — kapı 69 sürüm boyunca KÖR koştu);
//   (b) dize farkındalığı olmayan bir soyucu `'https://…'` gördüğünde
//       satırın kalanını yorum sanıyor. Bu kapı için (b) ölümcül:
//       aranan desen SATIR İÇİ STİL, yani bir dizenin İÇİNDE
//       (`height: 'calc(100vh - 220px)'`). Dizeleri atan bir soyucu
//       aradığı şeyi silmiş olurdu.
function stripTsComments(src: string): string {
  let out = '';
  let mode: 'code' | 'line' | 'block' | '"' | "'" | '`' = 'code';
  for (let i = 0; i < src.length; i++) {
    const c = src[i];
    const n = src[i + 1] ?? '';
    if (mode === 'code') {
      if (c === '/' && n === '/') { mode = 'line'; out += '  '; i++; continue; }
      if (c === '/' && n === '*') { mode = 'block'; out += '  '; i++; continue; }
      if (c === '"' || c === "'" || c === '`') { mode = c; out += c; continue; }
      out += c;
    } else if (mode === 'line') {
      if (c === '\n') { mode = 'code'; out += '\n'; } else out += ' ';
    } else if (mode === 'block') {
      if (c === '*' && n === '/') { mode = 'code'; out += '  '; i++; continue; }
      out += c === '\n' ? '\n' : ' ';
    } else {
      if (c === '\\') { out += '  '; i++; continue; }
      if (c === mode) mode = 'code';
      out += c;
    }
  }
  return out;
}

const stripCssComments = (src: string) =>
  src.replace(/\/\*[\s\S]*?\*\//g, m => m.replace(/[^\n]/g, ' '));

// `(?<![-\w])` `max-height` / `maxHeight` / `lineHeight`i eliyor:
// önlerinde `-` ya da kelime karakteri var. Kalanı `height` ve
// `min-height`/`minHeight` — yani kabın BOYUNU dayatan beyanlar.
const BANNED = /(?<![-\w])(?:minHeight|min-height|height)\s*:\s*['"]?\s*calc\(\s*100d?vh/g;

function walk(dir: string, out: string[] = []): string[] {
  for (const e of readdirSync(dir)) {
    const p = join(dir, e);
    if (statSync(p).isDirectory()) walk(p, out);
    else if (/\.(tsx?|css)$/.test(p)) out.push(p);
  }
  return out;
}

// GEREKÇELİ ALLOWLIST — dosya → izinli hit sayısı.
//
//   styles/globals.css × 1 → `#td-outer` (`:1729`, -185px).
//   Neden duruyor: kabı YALNIZ `pages/PublicTrace.tsx` render ediyor ve
//   o rota kendi kabuğunu kuruyor — `#content` yok, dolayısıyla
//   "PageShell göçü" diye bir iş yok; doğru iş ayrı bir public kabuk
//   (`PublicShell`). Aritmetiğin en zararlı hâli olan dar ekran zaten
//   serbest bırakıldı (`#td-outer { height: auto }`, D5.6) ve onu
//   `gridCollapse.test.ts:75` çiviliyor.
//
// Liste YALNIZ KÜÇÜLÜR (pageShellAdoption raçetinin aynısı).
const ALLOW: Record<string, number> = {
  'styles/globals.css': 1,
};

function hits(): Record<string, number> {
  const out: Record<string, number> = {};
  for (const abs of walk(SRC)) {
    const rel = abs.slice(SRC.length + 1).split(sep).join('/');
    if (/\.test\.tsx?$/.test(rel)) continue;   // kapıların KENDİ metinleri
    const raw = readFileSync(abs, 'utf8');
    const src = rel.endsWith('.css') ? stripCssComments(raw) : stripTsComments(raw);
    const n = (src.match(BANNED) ?? []).length;
    if (n > 0) out[rel] = n;
  }
  return out;
}

describe('DALGA 9 — viewport aritmetiği yalnız gerekçeli allowlist\'te', () => {
  const live = hits();

  // Anti-erozyon: tarama gerçekten dolaşıyor mu. Bir gün `walk` bozulsa
  // ya da uzantı filtresi kaysa kapı BOŞ küme üzerinde yeşil kalırdı.
  it('tarama gerçekten dosya görüyor', () => {
    expect(walk(SRC).length, 'tarama boş — kapı hiçbir şey ölçmüyor').toBeGreaterThan(200);
  });

  it('allowlist DIŞINDA viewport-yükseklik aritmetiği yok', () => {
    const strays = Object.keys(live)
      .filter(p => ALLOW[p] === undefined)
      .map(p => `${p} (${live[p]}×)`);
    expect(
      strays,
      'kabın boyu viewport aritmetiğiyle kuruluyor — kabuk zaten flex:1+overflow:auto; '
      + 'sayfa içinde `min-height: 100%` flex kolonu + `flex:1; min-height:0` kullan '
      + '(.tc-fill deseni, globals.css)',
    ).toEqual([]);
  });

  it('izinli dosyalar sayılarını ARTIRMAMIŞ', () => {
    const grew = Object.entries(ALLOW)
      .filter(([p, max]) => (live[p] ?? 0) > max)
      .map(([p, max]) => `${p}: ${live[p]} > izinli ${max}`);
    expect(grew, 'izinli dosyaya YENİ bir viewport hesabı eklenmiş').toEqual([]);
  });

  // Raçetin ikinci dişlisi: gerekçe ortadan kalktığında girdi ÖLÜR.
  it('ölü allowlist girdisi yok', () => {
    const dead = Object.keys(ALLOW).filter(p => (live[p] ?? 0) === 0);
    expect(dead, 'aritmetik kalkmış — girdiyi allowlist\'ten SİL').toEqual([]);
  });

  it('#td-outer\'ın gerekçesi hâlâ ayakta (tek render eden: PublicTrace)', () => {
    const renderers = walk(SRC)
      .filter(p => p.endsWith('.tsx') && !/\.test\.tsx$/.test(p))
      .filter(p => /id="td-outer"/.test(stripTsComments(readFileSync(p, 'utf8'))))
      .map(p => p.slice(SRC.length + 1).split(sep).join('/'));
    expect(
      renderers,
      'kabı başka bir sayfa da render ediyor — allowlist gerekçesi ("yalnız public rota") çürüdü',
    ).toEqual(['pages/PublicTrace.tsx']);
  });
});

describe('DALGA 9 — TraceCompare gerçek yerleşimden doluyor', () => {
  const page = stripTsComments(
    readFileSync(join(SRC, 'pages/TraceCompare.tsx'), 'utf8'));

  it('sayfada calc kalmamış ve kap PageShell', () => {
    expect(page, '-220px geri gelmiş').not.toMatch(/calc\(\s*100d?vh/);
    expect(page, 'kap elle yazılmış — <PageShell> kullan').not.toContain('id="content"');
    expect(page).toContain('<PageShell>');
  });

  // Zincirin ÜÇ halkası da şart: biri düşerse kutu ya çöker ya taşar ve
  // hiçbir tip hatası doğmaz.
  it('dolgu zinciri bağlı: .tc-fill → .tc-split → .tc-wf', () => {
    expect(page).toContain('className="tc-fill"');
    expect(page).toContain('className="grid-2 tc-split"');
    expect(page).toContain('className="tc-wf"');
  });

  it('CSS karşılıkları var (ölü sınıf değil)', () => {
    const css = stripCssComments(CSS);
    expect(css).toMatch(/\.tc-fill \{[^}]*min-height: 100%/);
    expect(css).toMatch(/\.tc-split \{[^}]*flex: 1[^}]*min-height: 0/);
    expect(css).toMatch(/\.tc-wf \{[^}]*flex: 1[^}]*min-height: 0[^}]*overflow: auto/);
  });

  // Telefonda iki şelale ALT ALTA geliyor; kabuk viewport'a kilitli
  // kalsaydı her birine üç-dört satır düşerdi (D5.6'nın #td-outer için
  // verdiği kararın aynısı).
  it('≤640px\'te yükseklik içeriğe bırakılıyor', () => {
    const m = /@media \(max-width: 640px\) \{([\s\S]*)/.exec(CSS);
    expect(m, '640px telefon katmanı kayboldu').toBeTruthy();
    const layer = stripCssComments(m![1]);
    expect(layer).toMatch(/\.tc-fill \{ min-height: 0; \}/);
    expect(layer).toMatch(/\.tc-split \{ flex: none;/);
  });
});
