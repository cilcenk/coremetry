import { describe, it, expect } from 'vitest';
import { isV2Viz, toCoreViz } from './panelViz';
import type { PanelVizType } from '@/lib/types';

// v0.9.796 — mark → motor kararının saf kapısı.
//
// İki ayrı yalan riski var, ikisi de sessiz:
//   1. Yanlış YÖNLENDİRME — stacked-bar v2'ye giderse panel çubuk yerine
//      ALAN çizer (CorePanel'in 'stacked'ı yığılmış alandır). Operatör
//      "bar" seçmiş, ekranda alan görür; hata mesajı yok.
//   2. Yanlış AD — CorePanel markı 'bars', dashboard'ınki 'bar'. Tek
//      harflik sapma CorePanel'in viz union'ında eşleşmez ve panel
//      varsayılan 'line'a düşer: yine sessiz, yine yanlış grafik.
describe('panelViz — hangi mark hangi motorda', () => {
  const ALL: PanelVizType[] = ['line', 'bar', 'stacked-bar', 'area', 'stacked-area'];

  it('v2 motoru line + bar + area taşır', () => {
    expect(ALL.filter(isV2Viz)).toEqual(['line', 'bar', 'area']);
  });

  // DÜRÜST SINIR: yığılmışlar eski SVG motorunda. Bu testin kızarması
  // "yığılmışları da taşıdım" demek — o karar mockup onayı ister
  // (yığılmış çubuğun v2'de karşılığı hâlâ yok).
  it('yığılmış markların ikisi de v2 DIŞINDA', () => {
    expect(isV2Viz('stacked-bar')).toBe(false);
    expect(isV2Viz('stacked-area')).toBe(false);
  });

  it('bar → bars (CorePanel adı), ötekiler aynen', () => {
    expect(toCoreViz('bar')).toBe('bars');
    expect(toCoreViz('area')).toBe('area');
    expect(toCoreViz('line')).toBe('line');
  });

  // Eşleme TOPLU: v2'ye giden her mark bir CorePanel markına düşer ve
  // hiçbiri sessizce undefined dönmez (union genişlerse burası kızarır).
  it('v2 markların HEPSİ eşlenir — boş dönen yok', () => {
    const mapped = ALL.filter(isV2Viz).map(toCoreViz);
    expect(mapped).toEqual(['line', 'bars', 'area']);
    expect(mapped.some(m => !m)).toBe(false);
  });
});
