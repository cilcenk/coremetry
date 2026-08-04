import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

// v0.9.639 — yapışkan filtre barı (denetim bulgusu #5, etki 5/5:
// "filtreler kaydırınca ekrandan çıkıyor").
//
// Bu testler görsel doğrulama YERİNE GEÇMEZ — CSS'in tarayıcıda nasıl
// göründüğünü jsdom söyleyemez. Çiviledikleri şey, düzeltmenin
// dayandığı YAPISAL varsayımlar: bunlar bozulursa sticky sessizce
// çalışmaz ve kimse fark etmez.

const css = readFileSync(resolve(__dirname, '../../styles/globals.css'), 'utf8')
  .replace(/\/\*[\s\S]*?\*\//g, ''); // yorumlar önce sıyrılır: açıklama kuralı alıntılıyor

function block(sel: string): string {
  const i = css.indexOf(sel + ' {');
  if (i < 0) return '';
  return css.slice(i, css.indexOf('}', i));
}

describe('sticky filtre barının dayandığı kabuk', () => {
  // Sticky'nin yapışacağı bir kaydırma portu OLMAK ZORUNDA. #content
  // overflow:auto olmaktan çıkarsa bar sessizce sıradan bir div olur.
  it('#content kaydırma portu olmayı sürdürüyor', () => {
    const b = block('#content');
    expect(b).toMatch(/overflow:\s*auto/);
    expect(b).toMatch(/flex:\s*1/);
  });

  // Taşırma (negatif margin) #content'in padding'ine göre hesaplandı.
  // Padding değişirse bar kenarlardan sızar.
  it('#content padding’i 20px — taşırma buna göre', () => {
    expect(block('#content')).toMatch(/padding:\s*20px/);
    const s = block('.controls.is-sticky');
    expect(s).toContain('margin: -20px -20px 14px');
    expect(s).toContain('top: -20px');
  });

  it('sticky sınıfı gerçekten sticky ve opak', () => {
    const s = block('.controls.is-sticky');
    expect(s).toMatch(/position:\s*sticky/);
    expect(s).toMatch(/z-index/);
    // Opak zemin şart: altından kayan içerik barın içinden görünürdü.
    expect(s).toMatch(/background:\s*var\(--bg0\)/);
  });

  // İÇ ÇERÇEVE YASAK (operatör kuralı: "sayfa başına tek scroll ekseni").
  // Bar kendi kaydırma konteynerini açarsa iç içe scroll doğar.
  it('bar yeni bir kaydırma konteyneri AÇMIYOR', () => {
    expect(block('.controls.is-sticky')).not.toMatch(/overflow/);
  });
});

describe('opt-in olma sözleşmesi', () => {
  const src = readFileSync(resolve(__dirname, './PageControls.tsx'), 'utf8');

  // .controls'u 30 dosya kullanıyor ve hepsi liste sayfası değil.
  // Varsayılan sticky olursa doğrulanmamış bir davranış 30 sayfaya yayılır.
  it('sticky varsayılan olarak KAPALI', () => {
    expect(src).toMatch(/sticky\s*=\s*false/);
  });

  it('kapalıyken is-sticky sınıfı basılmıyor', () => {
    expect(src).toContain("sticky ? 'is-sticky' : ''");
  });
});
