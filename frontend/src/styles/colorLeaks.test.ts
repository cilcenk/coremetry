// colorLeaks — Dalga 4 / mK6 kapısı (v0.9.906)
//
// Ne çiviliyor: `globals.css`in KURAL GÖVDELERİNDE hex/rgba renk
// literali kalmadığı. Tema blokları (`:root`, `[data-theme=…]`) tek
// meşru literal yeridir; dışarıda yazılan her literal ÜÇ temaya birden
// dayatılır ve pratikte "dark temanın rengi light sayfada" demektir.
//
// Neden ayrı bir kapı: tsc bir CSS dosyasına hiç bakmaz, eslint de
// öyle; `undefinedCssRefs` yalnız TANIMSIZ `var(--x)` arar — TANIMLI
// bir token yerine yazılmış bir literal onun için görünmez. `make
// audit`in CSS kuralı yok. Yani bu bozukluk sınıfı dört kapıdan da
// sessizce geçiyordu: ekranda bir şey "bozulmuyor", sadece yanlış
// renkte. Kanonik örnek `.s-fatal`ın dark-tema kırmızısıydı (#ff6b6b):
// beyaz light yüzeyinde ~2.8:1 ölçüyordu, yani üründeki EN yüksek
// önem seviyesi en okunmaz olanıydı.
//
// Muafiyet anahtarı GEREKÇEdir, satır numarası değil (v0.9.887 dersi:
// satıra bağlı muafiyet dosyaya bir import eklenince kayar ve test
// alakasız bir yerde kırmızıya döner). Bir muafiyet, gerekçesi
// ortadan kalktığında ÇIKARILMALI.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const CSS = readFileSync(resolve(__dirname, 'globals.css'), 'utf8');

// Yorumları BOŞALT, silme — satır numaraları rapora giriyor.
function stripComments(src: string): string {
  return src.replace(/\/\*[\s\S]*?\*\//g, m => m.replace(/[^\n]/g, ' '));
}

// Tema bildirim blokları literal'in meşru evi. `:root { … }` ve
// `[data-theme="x"] { … }` gövdelerini (iç içe kapsamlı olanlar dahil,
// örn. `[data-theme="redhat"] #sidebar`) ayıklıyoruz.
// Her satır, İÇİNDE bulunduğu kuralın seçicisiyle birlikte dönüyor:
// muafiyet gerekçeleri seçiciye bağlı, oysa sızıntı çoğu zaman
// seçiciden birkaç satır AŞAĞIDA (`box-shadow:` kendi satırında).
// Satır-yerel eşleşme burada yanlış cevabı verirdi.
function outsideThemeBlocks(src: string): { line: number; text: string; selector: string }[] {
  const out: { line: number; text: string; selector: string }[] = [];
  let depth = 0;
  let inTheme = false;
  let selector = '';
  let pending = '';
  src.split('\n').forEach((raw, idx) => {
    if (!inTheme && /^\s*(:root|\[data-theme)/.test(raw) && raw.includes('{')) {
      inTheme = true;
      depth = 0;
    }
    if (inTheme) {
      depth += (raw.match(/\{/g) ?? []).length - (raw.match(/\}/g) ?? []).length;
      if (depth <= 0) inTheme = false;
      return;
    }
    if (raw.includes('{')) {
      selector = (pending + ' ' + raw.slice(0, raw.indexOf('{'))).trim();
      pending = '';
    }
    out.push({ line: idx + 1, text: raw, selector });
    if (raw.includes('}')) selector = '';
    if (!raw.includes('{') && !raw.includes('}') && raw.trim() && !raw.includes(':')) {
      pending = raw.trim(); // çok satırlı seçici listesi
    }
  });
  return out;
}

// Kural gövdesinde renk literali imzası. Parçalardan kuruluyor ki bu
// dosyanın KENDİ düzyazısı bir kural sanılmasın (depoda yedi kez ısıran
// tuzak) — ama burada taranan globals.css olduğu için bu yalnız
// tutarlılık; asıl koruma stripComments.
const HEX = '#' + '[0-9a-fA-F]{3,8}' + '\\b';
const RGBA = 'rgba?' + '\\([0-9\\s,.]+\\)';
const LEAK = new RegExp(`(${HEX}|${RGBA})`);

// GEREKÇEYE göre anahtarlanmış muafiyetler. Anahtar = seçici + neden.
const ALLOWED: { selector: string; why: string }[] = [
  {
    selector: '.wf-bar-label',
    why:
      'Etiket bir SPAN ÇUBUĞUNUN üstünde duruyor — zemin sayfa yüzeyi ' +
      'değil, doygun bir waterfall rengi. Beyaz her üç temada da doğru; ' +
      '--text token ailesi burada yanlış olurdu (light temada çubuğun ' +
      'üstüne koyu gri yazardı).',
  },
  {
    selector: 'th.sticky-right, td.sticky-right',
    why:
      'Yönlü örtme gölgesi (-6px 0 8px -8px). --shadow-* ailesinin ' +
      'tamamı aşağı-doğru yayılıyor; yatay bir kademe yok ve bu tek ' +
      'kullanım için token açmak ölü token üretir.',
  },
];

describe('globals.css renk literalleri', () => {
  it('kural gövdelerinde hex/rgba literali kalmadı', () => {
    const lines = outsideThemeBlocks(stripComments(CSS));
    const leaks = lines
      .filter(l => LEAK.test(l.text))
      .filter(l => !ALLOWED.some(a => l.selector.includes(a.selector.split(',')[0].trim())));
    expect(
      leaks.map(l => `globals.css:${l.line} ${l.text.trim().slice(0, 100)}`),
    ).toEqual([]);
  });

  it('muafiyetlerin hepsi hâlâ CANLI (bayat girdi = yanlış sebeple yeşil)', () => {
    for (const a of ALLOWED) {
      expect(CSS.includes(a.selector), `${a.selector} kayboldu — muafiyeti ÇIKAR`).toBe(true);
    }
  });
});

describe('mK6 token tanımları', () => {
  // Rol token'ları üç temada da ÇÖZÜLEBİLİR olmalı. --on-accent ve
  // --backdrop bilinçli olarak yalnız :root'ta: değeri üç temada aynı,
  // ve devralınan değeri yeniden yazan bir override ileride sessizce
  // ayrışır. Burada aranan "her temada tanımlı" değil, ":root'ta
  // tanımlı VE tema-bağımlı olanların her temada karşılığı var".
  const rootBlock = CSS.slice(CSS.indexOf(':root'), CSS.indexOf('[data-theme="light"]'));

  it('rol token\'ları :root\'ta tanımlı', () => {
    for (const t of ['--on-accent', '--backdrop', '--fatal', '--shadow-modal']) {
      expect(rootBlock.includes(`${t}:`), `${t} :root'ta yok`).toBe(true);
    }
  });

  it('tema-bağımlı olanlar her üç temada override edilmiş', () => {
    const light = CSS.slice(CSS.indexOf('[data-theme="light"]'), CSS.indexOf('[data-theme="redhat"]'));
    const redhatStart = CSS.indexOf('[data-theme="redhat"]');
    const redhat = CSS.slice(redhatStart, CSS.indexOf('[data-theme="redhat"] body'));
    for (const t of ['--fatal', '--shadow-modal']) {
      expect(light.includes(`${t}:`), `${t} light temada override edilmemiş`).toBe(true);
      expect(redhat.includes(`${t}:`), `${t} redhat temasında override edilmemiş`).toBe(true);
    }
  });

  it('--fatal, --err\'den AYRI bir değer (yoksa seviye ayrımı kaybolur)', () => {
    // .s-fatal'ın tek işi --err'in bir tık üstünü göstermek. Token
    // eklerken en kolay hata onu var(--err)'e bağlamaktır; o durumda
    // "fatal" ile "error" ekranda ayırt edilemez hâle gelir.
    expect(/--fatal:\s*var\(--err\)/.test(CSS)).toBe(false);
  });
});
