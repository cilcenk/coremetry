// bgZeroIsPageOnly — arka plan denetimi D1 kapısı (v0.9.978)
//
// Ne çiviliyor: `--bg0` SAYFA ZEMİNİ tokenidir; onu bir yüzeye
// (kart, popover, pill, kod bloğu) zemin diye vermek yasaktır.
//
// Neden kural bu: yükseltme YÖNÜ temadan temaya değişiyor.
//   dark    --bg0 #1c2128  <  --bg1 #22272e   (yüzey daha AÇIK)
//   light   --bg0 #ffffff  >  --bg1 #f6f8fa   (yüzey daha KOYU — ters!)
//   redhat  --bg0 #f0f0f0  <  --bg1 #ffffff   (yüzey daha AÇIK)
// Yani `--bg0`la boyanmış bir yüzey dark'ta "geri çekilmiş", light'ta
// "öne çıkmış" görünür. Aynı satır üç temada üç farklı anlam taşıyor.
// Denetimin KN-3'ü tam olarak budur ve `colorLeaks` bunu göremez:
// `--bg0` TANIMLI ve meşru bir token, yalnız YANLIŞ YERDE.
//
// İzin listesi SAYFA ZEMİNİNE yapışan yüzeylerden oluşuyor — yapışkan
// filtre barı, sayfa altı pager'ı ve sekme şeridi kaydıkça altlarından
// içerik geçer; opak olmak zorundalar ve doğru opak değer sayfanın
// KENDİ rengidir. Bunlar `--bg1` olsaydı sayfada kayan beyaz şeritler
// oluşurdu (D1'in "DOKUNULMAYACAKLAR" listesi).
//
// Muafiyet anahtarı GEREKÇEdir, satır numarası değil.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const CSS = readFileSync(resolve(__dirname, 'globals.css'), 'utf8');

function stripComments(src: string): string {
  return src.replace(/\/\*[\s\S]*?\*\//g, m => m.replace(/[^\n]/g, ' '));
}

// Seçici → gerekçe. Bir muafiyet, gerekçesi ortadan kalktığında
// ÇIKARILMALI.
const ALLOW: Record<string, string> = {
  'body': 'sayfa zemininin TANIMI — tek meşru kaynak',
  '.controls.is-sticky': 'yapışkan filtre barı; altından içerik kayar, sayfa rengi olmalı',
  '.pager.is-sticky-bottom': 'sayfa altı yapışkan pager; aynı gerekçe',
  '.tab-strip.svc-tabs': 'yapışkan sekme şeridi; aynı gerekçe',
  '.topo-node.ext': 'zemin DEĞİL ayırt edici: taban .topo-node --bg1, .ext ondan geri çekiliyor. --bg1 iç/dış ayrımını siler, --bg2 .topo tuvaliyle çakışır. Düzeltmesi tasarım çağrısı — OPERATÖR KARARI bekliyor (v0.9.978)',
};

// `background`/`background-color` bildirimi İÇİNDE `--bg0` geçen her
// kuralı topla. `border: 1px solid var(--bg0)` (`.wf-ev`) bilinçli
// olarak KAPSAM DIŞI: orada `--bg0` bir kıl çizgi rengi, yüzey değil.
function bgZeroRules(): { selector: string; decl: string }[] {
  const out: { selector: string; decl: string }[] = [];
  const clean = stripComments(CSS);
  const re = /([^{}]+)\{([^{}]*)\}/g;
  let m: RegExpExecArray | null;
  while ((m = re.exec(clean)) !== null) {
    const selector = m[1].trim().replace(/\s+/g, ' ');
    for (const d of m[2].split(';')) {
      if (!/(^|\s)background(-color)?\s*:/.test(d)) continue;
      if (d.includes('var(--bg0)')) out.push({ selector, decl: d.trim() });
    }
  }
  return out;
}

describe('D1 — --bg0 yalnız sayfa zemini', () => {
  it('izin listesi dışında --bg0 ile boyanan yüzey yok', () => {
    const offenders = bgZeroRules().filter(r =>
      !r.selector.split(',').every(s => ALLOW[s.trim()] !== undefined));
    expect(
      offenders.map(o => `${o.selector} { ${o.decl} }`),
      'bir yüzey sayfa zemini tokeniyle boyanmış — üç temada üç farklı anlam',
    ).toEqual([]);
  });

  it('izin listesindeki her girdi hâlâ CSS\'te var (ölü muafiyet yok)', () => {
    const live = new Set(bgZeroRules().flatMap(r => r.selector.split(',').map(s => s.trim())));
    const dead = Object.keys(ALLOW).filter(s => !live.has(s));
    expect(dead, 'gerekçesi kalmamış muafiyet — listeden çıkar').toEqual([]);
  });

  // Merdiven yönünün temaya göre döndüğünün KANITI; kuralın neden var
  // olduğunu tek bir assert'te tutuyor. Tema blokları değişirse bu
  // test kırmızıya döner ve kuralın gerekçesi yeniden düşünülür.
  it('yükseltme yönü light temada TERS — kuralın gerekçesi', () => {
    const val = (theme: string, token: string) => {
      const block = theme === 'dark'
        ? /:root\s*\{([\s\S]*?)\}/.exec(CSS)![1]
        : new RegExp(`\\[data-theme="${theme}"\\]\\s*\\{([\\s\\S]*?)\\}`).exec(CSS)![1];
      return new RegExp(`${token}\\s*:\\s*([^;]+)`).exec(block)![1].trim();
    };
    expect(val('dark', '--bg0')).not.toBe(val('dark', '--bg1'));
    expect(val('light', '--bg0')).toBe('#ffffff');
    expect(val('light', '--bg1')).toBe('#f6f8fa');   // bg1 < bg0 → ters merdiven
    expect(val('redhat', '--bg0')).toBe('#f0f0f0');
    expect(val('redhat', '--bg1')).toBe('#ffffff');
  });
});
