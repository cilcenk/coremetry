import { describe, it, expect } from 'vitest';
import { medianStep, isBridgeableGap, filterGapsPx, nearestFilledIdx } from './gapPolicy';

// uPlot Aşama 2 madde 3+4 — gap köprüleme + cursor dolu-örnek snap'i.
// Kontrat: tek kaçmış scrape köprülenir, gerçek kesinti kırık kalır;
// hover en yakın dolu örneğe sınırlı pencerede snap'ler.

describe('medianStep', () => {
  it('düzenli adım → adımın kendisi', () => {
    expect(medianStep([0, 60, 120, 180])).toBe(60);
  });
  it('birkaç gap medyanı bozmaz', () => {
    // 60'lık adım, bir 300'lük delik: medyan hâlâ 60
    expect(medianStep([0, 60, 120, 420, 480, 540])).toBe(60);
  });
  it('yetersiz veri → 0', () => {
    expect(medianStep([])).toBe(0);
    expect(medianStep([5])).toBe(0);
  });
});

describe('isBridgeableGap', () => {
  const step = 60;
  it('tek kaçmış bucket (gap = 2×step) → köprüle', () => {
    expect(isBridgeableGap(120, step)).toBe(true);
  });
  it('iki kaçmış bucket (gap = 3×step) → kır (gerçek kesinti)', () => {
    expect(isBridgeableGap(180, step)).toBe(false);
  });
  it('sınır: gap = 2.5×step tam eşik → kır', () => {
    expect(isBridgeableGap(150, step)).toBe(false);
  });
  it('bitişik noktalar (gap = step) → köprüle (zaten boşluk yok)', () => {
    expect(isBridgeableGap(60, step)).toBe(true);
  });
  it('step bilinmiyor (0) → köprüleme, mevcut davranış', () => {
    expect(isBridgeableGap(120, 0)).toBe(false);
  });
});

describe('filterGapsPx', () => {
  // pxPerSec = 2 → 60 sn = 120 px
  const pxPerSec = 2;
  const step = 60;
  it('kısa gap (2×step) listeden düşer (köprülenir), uzun kalır', () => {
    const gaps: [number, number][] = [
      [100, 340],  // 240px = 120sn = 2×step → köprüle
      [500, 920],  // 420px = 210sn = 3.5×step → kalır
    ];
    expect(filterGapsPx(gaps, pxPerSec, step)).toEqual([[500, 920]]);
  });
  it('step/pxPerSec bilinmiyor → dokunma (tüm gap listesi aynen)', () => {
    const gaps: [number, number][] = [[1, 2]];
    expect(filterGapsPx(gaps, 0, step)).toEqual(gaps);
    expect(filterGapsPx(gaps, pxPerSec, 0)).toEqual(gaps);
  });
});

describe('nearestFilledIdx', () => {
  const vals = [10, null, null, 40, null, 60];
  it('dolu index olduğu gibi döner', () => {
    expect(nearestFilledIdx(vals, 0)).toBe(0);
    expect(nearestFilledIdx(vals, 3)).toBe(3);
  });
  it('null index en yakın doluya snap (pencere içinde)', () => {
    expect(nearestFilledIdx(vals, 4)).toBe(3);   // sol 1 mesafe
    expect(nearestFilledIdx(vals, 2)).toBe(3);   // sağ 1 mesafe
  });
  it('eşit uzaklıkta soldaki kazanır', () => {
    // idx 1: sol 0 (mesafe 1), sağ 3 (mesafe 2) → 0... idx 2: sol 0 (2), sağ 3 (1) → 3
    expect(nearestFilledIdx([10, null, null, 40], 1)).toBe(0);
    expect(nearestFilledIdx([10, null, 30], 1)).toBe(0); // eşit → sol
  });
  it('pencere dışı → orijinal index (sınırsız arama YOK)', () => {
    expect(nearestFilledIdx([1, null, null, null, null, null, 7], 3, 2)).toBe(3);
  });
  it('sınır dışı index güvenli', () => {
    expect(nearestFilledIdx(vals, -1)).toBe(-1);
    expect(nearestFilledIdx(vals, 99)).toBe(99);
  });
});
