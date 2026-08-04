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

  // v0.9.640 — operatör-bildirimli regresyon: YATAY negatif margin blok
  // elemanın genişliğini 40px artırıyor, kolon sayısı fazla bir tabloda
  // #content'te yatay kaydırma doğuruyor ve içerik barın yanından
  // sızıyordu. Yatay taşırma zaten gereksizdi: alttan kayan içerik de
  // barla aynı content-box genişliğinde.
  it('YATAY taşırma YOK — genişliği artıran negatif margin yasak', () => {
    const s = block('.controls.is-sticky');
    expect(s).not.toMatch(/margin(-left|-right)?:\s*-/);
    expect(s).not.toMatch(/width:/);
  });

  it('scrollport üst kenarına yapışıyor', () => {
    expect(block('.controls.is-sticky')).toMatch(/top:\s*0/);
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

// v0.9.644 — yapışkan tablo BAŞLIĞI (denetim bulgusu #10, etki 5/5).
//
// Engel: `.table-wrap`'ın overflow-x:auto'su onu kaydırma konteyneri
// yapıyor, thead'in sticky'si o konteynere yapışıyor ama konteyner
// dikey kaydırmadığı için etkisiz. `.is-fit` konteyneri kaldırıyor.
describe('yapışkan tablo başlığı', () => {
  it('is-fit kaydırma konteynerini kaldırıyor', () => {
    expect(block('.table-wrap.is-fit')).toMatch(/overflow:\s*visible/);
  });

  it('başlık yapışkan ve opak', () => {
    const b = block('.table-wrap.is-fit thead th');
    expect(b).toMatch(/position:\s*sticky/);
    expect(b).toMatch(/background:/);
  });

  // İKİ ÖZELLİK BİRLİKTE ÇALIŞMALI: ikisi de top:0 olsaydı üst üste
  // binerlerdi. Başlık barın ALTINA yapışıyor.
  it('başlık, yapışkan filtre barının ALTINA yapışıyor', () => {
    expect(block('.table-wrap.is-fit thead th')).toContain('top: var(--controls-h, 0px)');
  });

  // Varsayılan DEĞİŞMEMELİ: geniş tabloda is-fit, yatay kaydırmayı
  // #content'e taşır ve v0.9.640'ta düzeltilen bar sızıntısını geri
  // getirir. Yanlış sınıflandırmanın bedeli asimetrik.
  it('varsayılan .table-wrap hâlâ kendi kaydırma konteyneri', () => {
    const b = block('.table-wrap');
    expect(b).toMatch(/overflow-x:\s*auto/);
  });
});

describe('PageControls yüksekliği yayınlıyor', () => {
  const src = readFileSync(resolve(__dirname, './PageControls.tsx'), 'utf8');

  it('--controls-h yazılıyor', () => {
    expect(src).toContain("setProperty('--controls-h'");
  });

  // Sabit sayı kırılgan olurdu: bar sarınca yüksekliği değişiyor.
  it('yükseklik ÖLÇÜLÜYOR, sabit değil', () => {
    expect(src).toContain('ResizeObserver');
    expect(src).toContain('offsetHeight');
  });

  // Bar sticky değilse değişken yazılmamalı — yoksa başlık olmayan bir
  // barın altına yapışmaya çalışır.
  it('yalnız sticky iken yayınlıyor ve sökülürken temizliyor', () => {
    expect(src).toContain('if (!el || !sticky) return;');
    expect(src).toContain("removeProperty('--controls-h')");
  });
});
