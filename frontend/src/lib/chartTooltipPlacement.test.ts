import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { placeTooltip } from './chartTooltip';

// v0.9.631 — operatör-bildirimli: "neden imlecin sol üstünde çıkıyor,
// sağ altta çıksa daha iyi, Grafana gibi".
//
// Kök neden İKİ YERLEŞİM SİSTEMİNİN KAVGASIYDI: placeTooltip sağ-altı
// tercih eden bir sol-üst köşe hesaplayıp panele clamp'liyor, sonra
// `.ov-tt { transform: translate(-50%, -110%) }` kutuyu yarım genişlik
// SOLA + %110 yükseklik YUKARI çekiyordu. Hesap ne verirse versin sonuç
// sol-üsttü — ve dönüşüm clamp'ten SONRA uygulandığı için clamp hiçbir
// şey garanti etmiyordu, kutu panelden de taşıyordu.
//
// Transform, placeTooltip'ten önceki dönemin kalıntısıydı.

// stripCssComments — /* … */ bloklarını atar.
//
// ZORUNLU: düzeltmenin AÇIKLAMASI globals.css'te duruyor ve içinde
// `transform: translate(-50%, -110%)` geçiyor. Yorumları sıyırmayan bir
// tarama kendi açıklamasıyla eşleşir ve hep kırmızı kalır — bu oturumda
// üç kez ters yönü yaşandı (kendi yorumuyla eşleşip hep YEŞİL kalan test).
function stripCssComments(css: string): string {
  return css.replace(/\/\*[\s\S]*?\*\//g, '');
}

describe('stripCssComments', () => {
  it('yorum içindeki kuralı görmezden gelir, koddakini bırakır', () => {
    const out = stripCssComments('/* transform: translate(-50%,-110%) */\n.a { color: red; }');
    expect(out).not.toContain('translate');
    expect(out).toContain('color: red');
  });
});

describe('.ov-tt yerleşimi', () => {
  const css = stripCssComments(
    readFileSync(resolve(__dirname, '../styles/globals.css'), 'utf8'),
  );

  // `.ov-tt` bloğunu al (alt seçicileri `.ov-tt .ov-tt-r` DEĞİL).
  const block = css.match(/(^|\})\s*\.ov-tt\s*\{([^}]*)\}/)?.[2] ?? '';

  it('blok bulunabiliyor (regex bozulmadıysa)', () => {
    expect(block).toContain('position: absolute');
  });

  it('konumu kaydıran bir transform TAŞIMAZ — placeTooltip son köşeyi zaten hesaplıyor', () => {
    expect(block).not.toMatch(/transform\s*:/);
  });
});

describe('placeTooltip', () => {
  // Bol yer varken Grafana davranışı: imlecin SAĞ-ALTI.
  it('yer varken sağ-alta koyar', () => {
    const p = placeTooltip(100, 100, 200, 120, 800, 400, 0, 0, 800, 400);
    expect(p.x).toBeGreaterThan(100);
    expect(p.y).toBeGreaterThan(100);
  });

  // Sağda yer yoksa sola çevirir ama tuval DIŞINA taşmaz.
  it('sağ kenarda çevirir, taşmaz', () => {
    const p = placeTooltip(760, 100, 200, 120, 800, 400, 0, 0, 800, 400);
    expect(p.x + 200).toBeLessThanOrEqual(800);
    expect(p.x).toBeGreaterThanOrEqual(0);
  });

  // Altta yer yoksa yukarı çevirir ama tuval DIŞINA taşmaz.
  it('alt kenarda çevirir, taşmaz', () => {
    const p = placeTooltip(100, 380, 200, 120, 800, 400, 0, 0, 800, 400);
    expect(p.y + 120).toBeLessThanOrEqual(400);
    expect(p.y).toBeGreaterThanOrEqual(0);
  });

  // Operatörün gerçek şekli: KISA grafik (150px) + GENİŞ tooltip (uzun
  // seri etiketleri). Hiçbir eksende yer yok — yine de kutu tuval içinde
  // KALMALI. Eski transform bu vakada kutuyu panelin dışına atıyordu.
  it('dar panelde bile tuval içinde kalır', () => {
    const [tw, th] = [360, 130];
    for (const cx of [0, 50, 200, 400, 590]) {
      for (const cy of [0, 40, 75, 110, 149]) {
        const p = placeTooltip(cx, cy, tw, th, 600, 150, 0, 0, 600, 150);
        expect(p.x).toBeGreaterThanOrEqual(0);
        expect(p.y).toBeGreaterThanOrEqual(0);
        expect(p.x + tw).toBeLessThanOrEqual(600);
        expect(p.y + th).toBeLessThanOrEqual(150);
      }
    }
  });
});
