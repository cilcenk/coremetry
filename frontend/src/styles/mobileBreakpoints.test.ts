// mobileBreakpoints — dar ekran denetimi D2 kapısı (v0.9.980)
//
// Ne çiviliyor: `globals.css`teki her GENİŞLİK medya sorgusunun değeri
// {640, 1024, 1280} kümesinde. Yeni bir ara eşik = başarısız.
//
// Neden bir SAYI kümesi kapıya değer: eşikler bu depoda birbirinden
// habersiz büyüdü — 380 / 480 / 720 / 760 / 767 / 1100, altı tane, altı
// ayrı kararla. Sonuç bir "responsive tasarım" değil, altı ayrı dar
// ekran anlayışıydı ve en az bir yerde ÇAKIŞIYORDU: `Sidebar.tsx`
// off-canvas moda 768'de geçerken CSS hamburger payını 767'de
// veriyordu, yani tam 768px'te menü hamburgerle açılıyor ama sayfa
// başlığı hamburgerin ALTINDA kalıyordu. Kimse fark etmedi çünkü bir
// eşik uyuşmazlığını hiçbir tip/lint/audit kapısı göremez.
//
// `prefers-reduced-motion` blokları KAPSAM DIŞI: onlar bir genişlik
// kararı değil, erişilebilirlik tercihi.
//
// Not: CSS custom property `@media` sorgusunda kullanılamaz
// (`@media (max-width: var(--bp-sm))` geçersizdir), bu yüzden sayılar
// elle yazılmak ZORUNDA. Tek kaynak disiplinini o yüzden test taşıyor.
import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync } from 'node:fs';
import { resolve, join } from 'node:path';

const CSS = readFileSync(resolve(__dirname, 'globals.css'), 'utf8');
const SRC = resolve(__dirname, '..');

const ALLOWED = new Set([640, 1024, 1280]);

function widthQueries(src: string): { line: number; feature: string; px: number }[] {
  const out: { line: number; feature: string; px: number }[] = [];
  src.split('\n').forEach((l, i) => {
    const re = /@media[^{]*?\((max-width|min-width):\s*(\d+(?:\.\d+)?)px\)/g;
    let m: RegExpExecArray | null;
    while ((m = re.exec(l)) !== null) {
      out.push({ line: i + 1, feature: m[1], px: Number(m[2]) });
    }
  });
  return out;
}

describe('D2 — üç adlı eşik', () => {
  it('globals.css\'teki her genişlik eşiği {640, 1024, 1280} kümesinde', () => {
    const bad = widthQueries(CSS)
      .filter(q => !ALLOWED.has(q.px))
      .map(q => `globals.css:${q.line} ${q.feature}: ${q.px}px`);
    expect(bad, 'yeni bir ara eşik — üç adlı eşiğe katla').toEqual([]);
  });

  it('her iki eşik de gerçekten kullanımda (ölü şema değil)', () => {
    const used = new Set(widthQueries(CSS).map(q => q.px));
    expect(used.has(640), '640 telefon katmanı kayboldu').toBe(true);
    expect(used.has(1024), '1024 tablet katmanı kayboldu').toBe(true);
  });

  // JS tarafı ile CSS tarafı AYNI sayıyı kullanmak zorunda. Bu ikisi
  // ayrıştığında ortaya çıkan hata sınıfı sessizdir: bir bantta iki
  // düzen aynı anda geçerli olur.
  it('Sidebar\'ın JS eşikleri CSS katmanıyla aynı', () => {
    const sb = readFileSync(join(SRC, 'components/Sidebar.tsx'), 'utf8');
    const mobile = /const MOBILE_BP = (\d+)/.exec(sb);
    const tablet = /const TABLET_BP = (\d+)/.exec(sb);
    expect(mobile, 'MOBILE_BP kayboldu').toBeTruthy();
    expect(tablet, 'TABLET_BP kayboldu').toBeTruthy();
    expect(Number(mobile![1])).toBe(640);
    expect(Number(tablet![1])).toBe(1024);
  });

  // v0.9.988 (D6.1) — ikinci JS tüketicisi. `useIsNarrow` bir KOLONUN
  // dar ekranda düşüp düşmeyeceğine karar veriyor; eşiği CSS'ten
  // ayrışırsa 640-660px bandında `<colgroup>` bir kolon eksik, CSS ise
  // hâlâ geniş düzen sanıyor olurdu — hizası kaymış bir tablo, hiçbir
  // tip/lint kapısının göremeyeceği bir sınıf.
  it('useNarrow\'ın JS eşiği CSS telefon katmanıyla aynı', () => {
    const un = readFileSync(join(SRC, 'lib/useNarrow.ts'), 'utf8');
    const m = /export const NARROW_MAX_PX = (\d+)/.exec(un);
    expect(m, 'NARROW_MAX_PX kayboldu').toBeTruthy();
    expect(Number(m![1])).toBe(640);
  });

  // D2.1 — kapının ASIL sebebi. Bu kural silinirse 31 dosya / ~50 tablo
  // telefonda SESSİZCE sayfayı yatay kaydırmaya geri döner ve yapışkan
  // filtre barı içerikle yana kayar (v0.9.640 sızıntısı).
  it('dar ekranda is-fit tablolar kaydırma ağını geri alıyor', () => {
    const block = /@media \(max-width: 1024px\) \{([\s\S]*?)\n\}/.exec(CSS);
    expect(block, '1024 katmanı kayboldu').toBeTruthy();
    const body = block![1];
    expect(body).toMatch(/\.table-wrap\.is-fit\s*\{[^}]*overflow-x:\s*auto/);
    // `position: relative` + `top: auto` ikisi birden ŞART: sticky'yi
    // kaldırıp `top`u bırakmak başlığı satırların üstüne kaydırır
    // (v0.9.697 olayının birebir tekrarı).
    expect(body).toMatch(/\.table-wrap\.is-fit thead th\s*\{[^}]*position:\s*relative/);
    expect(body).toMatch(/\.table-wrap\.is-fit thead th\s*\{[^}]*top:\s*auto/);
  });

  it('iOS zoom düzeltmesi yoğunluk modlarını da kapsıyor', () => {
    const block = /@media \(max-width: 640px\) \{([\s\S]*)/.exec(CSS)![1];
    const rule = /input, select, textarea,([\s\S]*?)\{([^}]*)\}/.exec(block);
    expect(rule, 'iOS zoom kuralı kayboldu').toBeTruthy();
    expect(rule![2]).toMatch(/font-size:\s*16px/);
    // `[data-density="compact"] .controls input` (0,2,1) medya bloğunun
    // içindeki `input` (0,0,1) kuralını ÖZGÜLLÜKLE yener — kapsam
    // alınmazsa düzeltme yoğunluk modu açık her operatörde etkisiz.
    expect(rule![1]).toContain('[data-density] .controls input');
    expect(rule![1]).toContain('[data-density] .controls select');
  });

  // Katmanın dosyanın SONUNDA olması bir stil tercihi değil, kuralın
  // çalışma ŞARTI (kaynak sırası = eşit özgüllükte kazanan).
  it('dar ekran katmanı yoğunluk bloğundan SONRA geliyor', () => {
    // YORUMLARI SOY. İlk yazımda soymuyordu ve test YANLIŞ cevap
    // veriyordu: katmanın kendi açıklaması `[data-density="compact"]
    // .controls input` dizesini örnek olarak içeriyor, dolayısıyla
    // "yoğunluk bloğu" katmanın İÇİNDE bulunuyordu. Bu depoda dört kez
    // ısıran "kendi yorumunu eşleyen test" tuzağının beşincisi —
    // mutasyonla yakalandı (katmanı dosyanın başına taşıdım, test yeşil
    // kaldı).
    const clean = CSS.replace(/\/\*[\s\S]*?\*\//g, m => m.replace(/[^\n]/g, ' '));
    const density = clean.indexOf('[data-density="compact"] .controls input');
    const layer = clean.indexOf('@media (max-width: 640px)');
    expect(density, 'yoğunluk kuralı kayboldu').toBeGreaterThan(-1);
    expect(layer, 'telefon katmanı kayboldu').toBeGreaterThan(-1);
    expect(layer, 'katman yoğunluk bloğunun ÜSTÜNE taşınmış — iOS zoom düzeltmesi ölür')
      .toBeGreaterThan(density);
  });
});
