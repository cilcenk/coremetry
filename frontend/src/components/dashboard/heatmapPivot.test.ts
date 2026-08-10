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

  it('pencereyi KUTUDAN alır, sayfa aralığından değil', () => {
    const href = heatmapTracesHref(box, 'checkout');
    expect(href).toContain('range=custom:1700000000000-1700000060000');
  });

  it('süre bandı tam sayıya YAYILARAK kapsar (floor/ceil)', () => {
    // Daraltmak, operatörün sürüklediği kutudaki span'leri listeden
    // düşürürdü — "9 örnek" der, 7 satır gösterirdi.
    const href = heatmapTracesHref(box, 'checkout');
    expect(href).toContain('minMs=12');
    expect(href).toContain('maxMs=26');
  });

  it('servis çipi yalnız servis VARSA yazılır', () => {
    expect(heatmapTracesHref(box, 'checkout')).toContain('service=checkout');
    const noSvc = heatmapTracesHref(box);
    expect(noSvc).not.toContain('service=');
    expect(noSvc.startsWith('/traces?minMs=')).toBe(true);
  });

  it('servis adı URL-kodlanır', () => {
    expect(heatmapTracesHref(box, 'a/b c')).toContain('service=a%2Fb%20c');
  });

  it('negatif alt sınır 0’a kelepçelenir', () => {
    expect(heatmapTracesHref({ ...box, lowDurMs: -3 })).toContain('minMs=0');
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
