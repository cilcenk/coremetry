// seriesFocus (v0.9.793) — odak stilinin saf çekirdeği.
//
// Korunan sözleşmeler: (a) odak yoksa çıktı NÖTR (unfocus ayrı bir kod yolu
// değil — "geri al" mantığı yazmıyoruz, aynı fonksiyon nötr durumu üretiyor),
// (b) odaklanan seri tam alpha + taban genişlik + 0.5, (c) tanınmayan etiket
// odaksızlıkla AYNI (Explore'da GroupTable başka bir panelin serisinde
// gezinirken bu panel toptan solmamalı), (d) taban genişlik ÇAĞIRANDAN gelir
// (bars 1 / line 1.5) — burada sabit yok.

import { describe, it, expect } from 'vitest';
import {
  resolveFocusIdx, focusSeriesStyle, FOCUS_DIM_ALPHA, FOCUS_WIDTH_BOOST,
} from './seriesFocus';

const NAMES = ['checkout', 'payments', 'search'];

describe('resolveFocusIdx', () => {
  it('etiket bulunur → 0-tabanlı veri indeksi', () => {
    expect(resolveFocusIdx(NAMES, 'checkout')).toBe(0);
    expect(resolveFocusIdx(NAMES, 'search')).toBe(2);
  });

  it('null/undefined → -1 (odak yok)', () => {
    expect(resolveFocusIdx(NAMES, null)).toBe(-1);
    expect(resolveFocusIdx(NAMES, undefined)).toBe(-1);
  });

  it('bu panelde OLMAYAN etiket → -1, odaksızlıkla aynı', () => {
    // Explore: GroupTable satırı başka panelin serisi olabilir. -1 dönmezse
    // panelin TÜM serileri solar ve grafik sebepsizce sönerdi.
    expect(resolveFocusIdx(NAMES, 'billing')).toBe(-1);
  });

  it('boş seri kümesinde her etiket -1', () => {
    expect(resolveFocusIdx([], 'checkout')).toBe(-1);
  });
});

describe('focusSeriesStyle', () => {
  const base = [1.5, 1.5, 1.5];

  it('odak yok → hepsi tam alpha + taban genişlik (nötr durum)', () => {
    expect(focusSeriesStyle(base, -1)).toEqual([
      { alpha: 1, width: 1.5 },
      { alpha: 1, width: 1.5 },
      { alpha: 1, width: 1.5 },
    ]);
  });

  it('odak var → odaklanan kalın + tam, ötekiler DIM ama taban genişlikte', () => {
    expect(focusSeriesStyle(base, 1)).toEqual([
      { alpha: FOCUS_DIM_ALPHA, width: 1.5 },
      { alpha: 1, width: 1.5 + FOCUS_WIDTH_BOOST },
      { alpha: FOCUS_DIM_ALPHA, width: 1.5 },
    ]);
  });

  it('indeks 0 (falsy) geçerli odak hedefi', () => {
    expect(focusSeriesStyle(base, 0)[0]).toEqual({ alpha: 1, width: 2 });
    expect(focusSeriesStyle(base, 0)[1].alpha).toBe(FOCUS_DIM_ALPHA);
  });

  it('taban ÇAĞIRANDAN: bars (1) ve line (1.5) karışık panelde ayrı ayrı', () => {
    expect(focusSeriesStyle([1, 1.5], 0)).toEqual([
      { alpha: 1, width: 1.5 },
      { alpha: FOCUS_DIM_ALPHA, width: 1.5 },
    ]);
    expect(focusSeriesStyle([1, 1.5], 1)).toEqual([
      { alpha: FOCUS_DIM_ALPHA, width: 1 },
      { alpha: 1, width: 2 },
    ]);
  });

  it('taban eksik/geçersizse 1.5 varsayılır (line tabanı)', () => {
    expect(focusSeriesStyle([undefined, NaN, Infinity], -1))
      .toEqual([{ alpha: 1, width: 1.5 }, { alpha: 1, width: 1.5 }, { alpha: 1, width: 1.5 }]);
  });

  it('kapsam dışı odak indeksi kimseyi öne çıkarmaz ama HERKESİ soldurmaz mı?', () => {
    // Dürüst sınır: resolveFocusIdx zaten -1 üretir, yani bu durum çağrı
    // yolunda oluşmaz. Yine de davranış belgeli olsun: 5 numaralı seri yok,
    // o yüzden hepsi dim olur. Kapı bu davranışı SABİTLER — bir gün biri
    // resolveFocusIdx'i atlayıp ham indeks geçerse sürpriz yaşamasın.
    expect(focusSeriesStyle(base, 5).every(s => s.alpha === FOCUS_DIM_ALPHA)).toBe(true);
  });

  it('boş panel → boş dizi (döngü kenar durumu)', () => {
    expect(focusSeriesStyle([], 0)).toEqual([]);
  });

  it('sabitler spec değerlerinde', () => {
    expect(FOCUS_DIM_ALPHA).toBe(0.25);
    expect(FOCUS_WIDTH_BOOST).toBe(0.5);
  });
});
