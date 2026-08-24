// heatmapPivot — UX denetimi D4 / Ö24 regresyon testleri (v0.9.947).
//
// Orijinal belirti: pano heatmap paneli salt-okunurdu — aynı görselleştirme
// Service ve Explore'da hem hücre tıkı hem kutu seçimi taşıyordu.
//
// Bu dosyanın çivilediği asıl şey DÜZELTMENİN SINIRI: jestler koşulsuz
// bağlanamaz. Pano paneli bir METRİK HİSTOGRAMI çiziyor (span süresi
// değil); süre olmayan bir histogramda "bu bandın trace'leri" boş modal
// değil YANLIŞ modal olurdu.

import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { heatmapPivotable, heatmapTracesHref } from './heatmapPivot';

describe('heatmapPivotable — birim kapısı', () => {
  const cases: [string | undefined, boolean][] = [
    ['ms', true],
    ['s', true],
    ['seconds', true],
    ['milliseconds', true],
    ['MS', true],          // büyük/küçük harf duyarsız
    ['  ms  ', true],      // kırpılır
    ['bytes', false],
    ['By', false],
    ['percent', false],
    ['reqps', false],
    ['', false],
    [undefined, false],    // birimini söylemeyen histogramı SÜRE SAYMA
  ];
  for (const [unit, want] of cases) {
    it(`${JSON.stringify(unit)} → ${want}`, () => {
      expect(heatmapPivotable(unit)).toBe(want);
    });
  }

  it('kapı histogramHeatmap’in ms çevirisiyle hizalı', () => {
    // histogramHeatmap.ts yalnız 's'/'seconds' için ×1000 yapıyor; pivot
    // kapısı en azından O yazımları tanımak ZORUNDA, yoksa saniye birimli
    // bir gecikme histogramı ms eksenine çevrilir ama jestsiz kalırdı.
    const src = readFileSync(join(__dirname, 'histogramHeatmap.ts'), 'utf8');
    const m = src.match(/unit === '(\w+)' \|\| unit === '(\w+)'/);
    expect(m, 'histogramHeatmap toMs dalı değişti — kapıyı da güncelle').toBeTruthy();
    expect(heatmapPivotable(m![1])).toBe(true);
    expect(heatmapPivotable(m![2])).toBe(true);
  });
});

describe('heatmapTracesHref', () => {
  const box = {
    timeFromNs: 1_700_000_000_000_000_000,
    timeToNs: 1_700_000_060_000_000_000,
    lowDurMs: 12.4, highDurMs: 25.1, count: 9,
  };

  // v0.9.1356 — üretici aileye taşındıktan sonra iddialar BAYT düzeyinden
  // ÇÖZÜLMÜŞ değere geçti. URLSearchParams `:`i `%3A`, boşluğu `+` diye
  // heceliyor; ikisi de aynı okuyucudan (searchParams.get) aynen geri
  // çıkıyor, yani anlam değişmedi — değişen tek şey heceleme, ve bayta
  // bakan bir iddia bu göçü sahte bir regresyon gibi gösterirdi.
  // Kardeş üretici slowTracesHref de aynı hecelemeyi çiviliyor
  // (slowTracesHref.test.ts: `range=custom%3A…`).
  const Q = (href: string) => new URLSearchParams(href.slice(href.indexOf('?') + 1));

  it('pencereyi KUTUDAN alır, sayfa aralığından değil', () => {
    expect(Q(heatmapTracesHref(box, 'checkout')).get('range'))
      .toBe('custom:1700000000000-1700000060000');
  });

  it('süre bandı tam sayıya YAYILARAK kapsar (floor/ceil)', () => {
    // Daraltmak, operatörün sürüklediği kutudaki span'leri listeden
    // düşürürdü — "9 örnek" der, 7 satır gösterirdi.
    const href = heatmapTracesHref(box, 'checkout');
    expect(href).toContain('minMs=12');
    expect(href).toContain('maxMs=26');
  });

  it('servis çipi yalnız servis VARSA yazılır', () => {
    expect(Q(heatmapTracesHref(box, 'checkout')).get('service')).toBe('checkout');
    // Boş servis bir DEĞER değil, yokluk: param hiç yazılmamalı. (Boş bir
    // `service=` /traces'te "adı boş olan servis" filtresine dönüşürdü.)
    expect(Q(heatmapTracesHref(box)).has('service')).toBe(false);
    expect(Q(heatmapTracesHref(box, '')).has('service')).toBe(false);
  });

  it('servis adı URL-kodlanır ve aynen geri çözülür', () => {
    const href = heatmapTracesHref(box, 'a/b c');
    expect(href).not.toContain('service=a/b c'); // ham `/` veya boşluk yok
    expect(Q(href).get('service')).toBe('a/b c');
  });

  it('negatif alt sınır 0’a kelepçelenir', () => {
    // ⚠ minMs=0 DÜŞMEMELİ. Üretici doğruluk (truthiness) ile değil
    // `!== undefined` ile yazıyor; doğruluk kontrolü bu satırı sessizce
    // atlar ve operatörün seçtiği bandı ALT UÇTAN sınırsız genişletirdi.
    expect(Q(heatmapTracesHref({ ...box, lowDurMs: -3 })).get('minMs')).toBe('0');
  });

  // v0.9.1356 — göçün asıl kazancı: elle kurulan dize KABUL kuralını hiç
  // taşımıyordu, `custom:` token'ını koşulsuz basıyordu. Aile üreticisi
  // decodeRange'in reddedeceği bir pencerede paramı ATLIYOR (v0.9.1355).
  it('bozuk kutu penceresinde range paramı HİÇ yazılmaz', () => {
    for (const w of [
      { timeFromNs: 6e9, timeToNs: 5e9 },   // ters
      { timeFromNs: 5e9, timeToNs: 5e9 },   // sıfır genişlik
      { timeFromNs: -1e9, timeToNs: 5e9 },  // epoch öncesi
    ]) {
      const q = Q(heatmapTracesHref({ ...box, ...w }, 'checkout'));
      expect(q.has('range'), JSON.stringify(w)).toBe(false);
      // Pivotun geri kalanı AYAKTA kalır — pencere reddi linki öldürmez.
      expect(q.get('service')).toBe('checkout');
      expect(q.get('minMs')).toBe('12');
    }
  });
});

describe('PanelRenderer bağlantısı (v0.9.947)', () => {
  const src = readFileSync(join(__dirname, 'PanelRenderer.tsx'), 'utf8');

  it('jestler KAPIDAN geçer — koşulsuz bağlanmaz', () => {
    expect(src).toMatch(/onCellClick=\{pivotable \? setCellExemplar : undefined\}/);
    expect(src).toMatch(/onBoxSelect=\{pivotable \? setBoxSel : undefined\}/);
  });

  it('kapı panelin BİRİMİNDEN kurulur', () => {
    expect(src).toMatch(/heatmapPivotable\(cfg\.unit\)/);
  });

  it('modal panelin kapsamını taşır', () => {
    expect(src).toMatch(/cfg\.service \? \[\{ k: 'service\.name'/);
  });

  it('pivot linki react-router Link — tam sayfa reload YOK (İ2 sınıfı)', () => {
    expect(src).toMatch(/<Link to=\{heatmapTracesHref\(boxSel, cfg\.service\)\}/);
  });
});
