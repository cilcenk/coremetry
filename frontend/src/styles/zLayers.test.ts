// zLayers — Dalga 4 / MK2 kapısı (v0.9.910)
//
// Ne çiviliyor: `--z-*` merdiveninin TANIMLI, ARTAN ve anlamlı
// ilişkileri koruyor olduğu + `globals.css`te çıplak sayı kalmadığı.
//
// Neden bu kapı ŞART: z-index regresyonlarını dört kapının hiçbiri
// göremez. tsc bir sayıya bakmaz, eslint satır-içi stile bakmaz, jsdom
// yığın sırasını HESAPLAMAZ (`getComputedStyle` z-index'i döndürür ama
// "hangisi üstte" sorusunu cevaplamaz — bunun için gerçek bir compositor
// gerekir), `make audit` CSS'e bakmaz. Ekranda bir panel diğerinin
// altında kaybolur ve hiçbir test kırmızıya dönmez.
//
// Bu yüzden kapı DEĞERLERİ değil, İLİŞKİLERİ sınıyor: "dropdown
// popover'ın altında" gibi ifadeler, kaç sayı olduklarından bağımsız
// olarak doğru kalmak zorunda. Bir rung'un sayısı değişebilir; sırası
// değişirse bu bir davranış kararıdır ve testi güncellemek gerekir —
// tam da istenen sürtünme.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const CSS = readFileSync(resolve(__dirname, 'globals.css'), 'utf8');

// `-` regex'te kelime sınırı DEĞİL: `\b--z-drawer\b` `--z-drawer-panel`in
// içinde de eşleşir ve YANLIŞ değeri döndürür (v0.9.894 dersi).
function z(name: string): number {
  const m = new RegExp(`--z-${name}(?![\\w-])\\s*:\\s*(\\d+)`).exec(CSS);
  if (!m) throw new Error(`--z-${name} tanımlı değil`);
  return Number(m[1]);
}

const LADDER = [
  'sticky-cell', 'sticky-head', 'sticky-foot', 'sticky-bar',
  'handle', 'tooltip', 'app-splash', 'nav', 'dropdown', 'popover',
  'drawer', 'drawer-panel', 'fab', 'modal', 'modal-nested', 'toast', 'debug',
];

describe('MK2 — z merdiveni', () => {
  it('17 rung tanımlı ve KESİN artan', () => {
    const vals = LADDER.map(z);
    expect(vals).toEqual([...vals].sort((a, b) => a - b));
    expect(new Set(vals).size, 'iki rung aynı değerde — beraberlik DOM sırasına kalır').toBe(vals.length);
  });

  it('dört yapışkan kademe ayrı ve doğru sırada', () => {
    // Hepsi tek --z-sticky'ye çökerse yapışkan başlık yapışkan filtre
    // barının ÜSTÜNE çizilir (v0.9.697'nin z-index sürümü).
    expect(z('sticky-cell')).toBeLessThan(z('sticky-head'));
    expect(z('sticky-head')).toBeLessThan(z('sticky-foot'));
    expect(z('sticky-foot')).toBeLessThan(z('sticky-bar'));
    expect(z('sticky-bar')).toBeLessThan(z('handle'));
  });

  it('üst katman zinciri: dropdown < popover < drawer < fab < modal < toast < debug', () => {
    const chain = ['dropdown', 'popover', 'drawer', 'fab', 'modal', 'toast', 'debug'].map(z);
    expect(chain).toEqual([...chain].sort((a, b) => a - b));
  });

  it('drawer paneli perdesinin TAM üstünde (+1)', () => {
    // Perde ile panel arasına başka bir şeyin girmesi mümkün olmamalı.
    expect(z('drawer-panel')).toBe(z('drawer') + 1);
  });

  it('iç içe modal dıştakinin üstünde', () => {
    // ZoomChannelPicker, ChannelModal'ın İÇİNDE açılıyor.
    expect(z('modal-nested')).toBeGreaterThan(z('modal'));
  });

  it('tooltip nav\'ın ALTINDA, yapışkanların üstünde', () => {
    // Tooltip bir sayfa içeriği süslemesi; sidebar/topbar onu örtmeli.
    expect(z('tooltip')).toBeGreaterThan(z('sticky-bar'));
    expect(z('tooltip')).toBeLessThan(z('nav'));
  });

  it('globals.css\'te çıplak z-index sayısı kalmadı (mikro bant hariç)', () => {
    // 0/1 gibi bileşen-İÇİ mikro katmanlar kapsam dışı: onlar bir
    // uygulama katmanı değil, tek bir kutunun kendi içindeki sıra.
    const stripped = CSS.replace(/\/\*[\s\S]*?\*\//g, m => m.replace(/[^\n]/g, ' '));
    const bad: string[] = [];
    stripped.split('\n').forEach((l, i) => {
      const m = /(?<!-)z-index:\s*(\d+)/.exec(l);
      if (m && Number(m[1]) > 6) bad.push(`globals.css:${i + 1} ${l.trim().slice(0, 90)}`);
    });
    expect(bad).toEqual([]);
  });
});
