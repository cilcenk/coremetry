import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

// v0.9.697 — YAPIŞKAN BAŞLIK, SATIR İÇİ `position` TARAFINDAN ÖLDÜRÜLMESİN.
//
// Operatör-bildirimi: "Kolonların ismi ilk satırın üzerine denk geliyor."
//
// PROD ÖLÇÜMÜ (v0.9.663, konsol, scrollTop=0):
//   controlsH "87px" · bar {top105, bottom192, h87} · wrap.top 206 · th.top 294
// 294 − 207 = 87 = --controls-h. scrollTop 0 iken sticky bir öğe AŞAĞI
// itilemez; relative bir öğe ise `top` kadar tam olarak itilir. Teşhis
// buradan çıktı — üç yanlış hipotezi ölçüm eledi, kod okuması değil.
//
// KÖK NEDEN: DataTableHead her th'ye SATIR İÇİ `position: 'relative'`
// yazıyordu. Satır içi stil, `.table-wrap.is-fit thead th`in
// `position: sticky`sini eziyor — ama aynı kuralın `top: var(--controls-h)`i
// satır içi DEĞİL, yani uygulanmaya devam ediyor. Sonuç: relative + top:87px
// = başlık 87px aşağı kayar ve yerini akışta BOŞ BIRAKIR; satırlar yukarı
// çıkar, başlık onların üstüne çizilir.
//
// NEDEN SEÇİCİ GÖRÜNDÜ: yapışkan filtre barı olmayan sayfalarda
// --controls-h hiç tanımlanmıyor, fallback `0px`, kayma sıfır. Kusur
// yalnız bar+is-fit birlikte olan sayfalarda ve bar ne kadar yüksekse
// o kadar görünür.

const SRC = resolve(__dirname, '..');

function readCss() {
  // Blok yorumları ŞART: bu kuralların gerekçeleri CSS'te yorum olarak
  // duruyor ve "position: sticky" dizgisi oralarda da geçiyor. Sıyırmazsam
  // test kendi açıklamasını kural sanar (bu kod tabanında altı kez ısırdı).
  return readFileSync(resolve(SRC, 'styles/globals.css'), 'utf8')
    .replace(/\/\*[\s\S]*?\*\//g, '');
}

// Bir kuralın gövdesini çıkarır.
//
// SEÇİCİ KURAL SINIRINA ÇİVİLİ: düz `indexOf('thead th {')` araması
// `.table-wrap.is-fit thead th {` içinde de eşleşiyor ve YANLIŞ kuralın
// gövdesini döndürüyor — ilk yazımda tam bunu yaptım, test taban kuralı
// sorup yapışkan kuralın gövdesini aldı. Alt-dize tuzağının CSS sürümü.
function ruleBody(css: string, selector: string): string {
  const esc = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const m = new RegExp(`(?:^|[};])\\s*${esc}\\s*\\{`, 'm').exec(css);
  if (!m) throw new Error(`kural bulunamadı: ${selector}`);
  const start = m.index + m[0].length;
  const end = css.indexOf('}', start);
  return css.slice(start, end);
}

// Kaba özgüllük: [id, class, element]. Bu kuralların hiçbirinde id yok.
function specificity(selector: string): [number, number, number] {
  const ids = (selector.match(/#[\w-]+/g) ?? []).length;
  const classes = (selector.match(/\.[\w-]+/g) ?? []).length;
  const elements = (selector.replace(/[.#][\w-]+/g, '').match(/\b[a-z]+\b/g) ?? []).length;
  return [ids, classes, elements];
}

function beats(a: string, b: string): boolean {
  const [ai, ac, ae] = specificity(a);
  const [bi, bc, be] = specificity(b);
  if (ai !== bi) return ai > bi;
  if (ac !== bc) return ac > bc;
  return ae > be;
}

describe('DataTableHead th konumu', () => {
  const src = readFileSync(resolve(SRC, 'components/DataTable.tsx'), 'utf8')
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .split('\n').map(l => l.replace(/\/\/.*$/, '')).join('\n');

  // ASIL KAPI. Satır içi `position` geri gelirse yapışkan başlık yine ölür
  // ve kusur sessizce döner — ekranda bir şey "bozulmuyor", sadece başlık
  // satırların üstüne biniyor.
  it('th stiline SATIR İÇİ position yazmıyor', () => {
    expect(src).not.toMatch(/position:\s*(['"])relative\1/);
    expect(src).not.toMatch(/position:\s*c\.stickyRight/);
  });
});

describe('globals.css konum katmanları', () => {
  const css = readCss();

  it('taban `thead th` relative — resize tutamağının çapası', () => {
    expect(ruleBody(css, 'thead th')).toMatch(/position:\s*relative/);
  });

  it('`.table-wrap.is-fit thead th` sticky', () => {
    expect(ruleBody(css, '.table-wrap.is-fit thead th')).toMatch(/position:\s*sticky/);
  });

  // BU TESTİN VAR OLMA SEBEBİ: hata bir özgüllük/katman hatasıydı.
  // Taban kural yapışkan kuralı ezerse başlık yine kaymaz-ama-yapışmaz olur.
  it('is-fit kuralı taban kuralı YENİYOR', () => {
    expect(beats('.table-wrap.is-fit thead th', 'thead th')).toBe(true);
  });

  it('sticky-right kuralı taban kuralı YENİYOR', () => {
    expect(beats('th.sticky-right', 'thead th')).toBe(true);
  });

  // Tutamak absolute; çapası th. relative de sticky de "konumlanmış"
  // sayıldığı için her iki varyantta da çapa duruyor — düzeltmenin
  // tutamağı kırmadığının çivisi.
  it('resize tutamağı hâlâ absolute', () => {
    expect(ruleBody(css, 'thead th .col-resize-handle')).toMatch(/position:\s*absolute/);
  });
});

describe('özgüllük yardımcısı kendi kendini doğruluyor', () => {
  it('sınıf sayısı eleman sayısını yener', () => {
    expect(beats('.a.b thead th', 'thead th')).toBe(true);
    expect(beats('thead th', '.a.b thead th')).toBe(false);
  });
});
