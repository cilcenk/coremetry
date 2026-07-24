import { describe, it, expect } from 'vitest';
import { clampRegion, fitLabel, thresholdVisible } from './overlays';

// overlays.test.ts (Grafana-parite M3) — paylaşımlı threshold/bölge çizim
// çekirdeğinin SAF yardımcılarını sabitler. Çizim fonksiyonlarının kendisi
// canvas ister (node ortamı — vitest.config.ts bilinçli jsdom'suz); piksel
// hesap/kırpma/etiket-sığdırma kararları burada tam kaplı.

describe('thresholdVisible — eşik canlı y-ölçeğinin içinde mi', () => {
  const cases: [string, number, number, number, boolean][] = [
    ['içeride', 50, 0, 100, true],
    ['alt kenarda (dahil)', 0, 0, 100, true],
    ['üst kenarda (dahil)', 100, 0, 100, true],
    ['altında', -1, 0, 100, false],
    ['üstünde', 101, 0, 100, false],
    ['negatif ölçekte içeride', -5, -10, 0, true],
  ];
  for (const [name, v, mn, mx, want] of cases) {
    it(name, () => expect(thresholdVisible(v, mn, mx)).toBe(want));
  }
});

describe('clampRegion — bölge ↔ canlı x-penceresi kesişimi', () => {
  it('tamamen içerideki bölge aynen döner', () => {
    expect(clampRegion(20, 30, 0, 100)).toEqual({ from: 20, to: 30 });
  });
  it('sola taşan bölge pencere başına kırpılır (zoom-in senaryosu)', () => {
    expect(clampRegion(-50, 30, 0, 100)).toEqual({ from: 0, to: 30 });
  });
  it('sağa taşan bölge pencere sonuna kırpılır (açık problem → şimdi)', () => {
    expect(clampRegion(80, 500, 0, 100)).toEqual({ from: 80, to: 100 });
  });
  it('pencereyi tamamen kapsayan bölge pencereye iner', () => {
    expect(clampRegion(-10, 900, 0, 100)).toEqual({ from: 0, to: 100 });
  });
  it('tamamen solda kalan bölge çizilmez', () => {
    expect(clampRegion(-30, -10, 0, 100)).toBeNull();
  });
  it('tamamen sağda kalan bölge çizilmez', () => {
    expect(clampRegion(200, 300, 0, 100)).toBeNull();
  });
  it('pencere kenarına sıfır-genişlik değen bölge çizilmez (to === xMin)', () => {
    expect(clampRegion(-10, 0, 0, 100)).toBeNull();
  });
  it('ters bölge (to <= from) çizilmez', () => {
    expect(clampRegion(30, 30, 0, 100)).toBeNull();
    expect(clampRegion(40, 30, 0, 100)).toBeNull();
  });
  it('NaN/Infinity uçlar çizilmez (bozuk veri savunması)', () => {
    expect(clampRegion(NaN, 30, 0, 100)).toBeNull();
    expect(clampRegion(10, Infinity, 0, 100)).toBeNull();
  });
});

describe('fitLabel — etiket sığdırma (sığdır / kısalt / sustur)', () => {
  // Deterministik ölçüm: 7px/karakter (monospace taklidi).
  const mono = (s: string) => s.length * 7;

  it('sığan etiket aynen döner', () => {
    expect(fitLabel('P1', 100, mono)).toBe('P1');
  });
  it('tam sınırda sığan etiket aynen döner (<= kuralı)', () => {
    expect(fitLabel('ABCD', 28, mono)).toBe('ABCD'); // 4*7 = 28
  });
  it('sığmayan etiket … ile kısalır', () => {
    // 'CRITICAL' = 56px; 35px'e 'CRIT…' (5*7=35) sığar.
    expect(fitLabel('CRITICAL', 35, mono)).toBe('CRIT…');
  });
  it('kısaltırken kuyruk boşluğu atılır (trimEnd)', () => {
    // 'AB CD' → 3 karakterlik kesim 'AB ' + … yerine 'AB…'.
    expect(fitLabel('AB CD', 21, mono)).toBe('AB…');
  });
  it('tek karakter + … bile sığmıyorsa boş döner (hiç çizme)', () => {
    expect(fitLabel('CRITICAL', 10, mono)).toBe('');
  });
  it('availPx <= 0 / boş etiket boş döner', () => {
    expect(fitLabel('P1', 0, mono)).toBe('');
    expect(fitLabel('P1', -5, mono)).toBe('');
    expect(fitLabel('', 100, mono)).toBe('');
  });
});
