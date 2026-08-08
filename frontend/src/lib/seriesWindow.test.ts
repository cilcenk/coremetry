import { describe, it, expect } from 'vitest';
import { splitPartialTail, coveredRangeNote, partialBucketNote } from './seriesWindow';

// seriesWindow regresyon testleri — v0.9.819.
// Zarf sözleşmesi /api/endpoints/series ile /api/databases/series
// arasında PAYLAŞILIYOR; buradaki davranış ikisini birden bağlar.

describe('splitPartialTail', () => {
  const pts = [1, 2, 3, 4, 5];

  it('kısmi değilse hiçbir şey ayrılmaz', () => {
    expect(splitPartialTail(pts, false)).toEqual({ solid: pts, tail: [] });
    expect(splitPartialTail(pts, undefined)).toEqual({ solid: pts, tail: [] });
  });

  it('kısmiyse son nokta kesikli parçaya geçer', () => {
    const { solid, tail } = splitPartialTail(pts, true);
    expect(solid).toEqual([1, 2, 3, 4]);
    // tail son SOLID noktayı da taşır → çizgi kopmaz
    expect(tail).toEqual([4, 5]);
  });

  it('hiçbir nokta KAYBOLMAZ', () => {
    const { solid, tail } = splitPartialTail(pts, true);
    const seen = new Set([...solid, ...tail]);
    for (const p of pts) expect(seen.has(p)).toBe(true);
  });

  it('tek noktalı seri bölünmez (kesikli parça çizilemez)', () => {
    expect(splitPartialTail([7], true)).toEqual({ solid: [7], tail: [] });
  });

  it('boş seri boş kalır', () => {
    expect(splitPartialTail([], true)).toEqual({ solid: [], tail: [] });
  });
});

describe('coveredRangeNote', () => {
  const from = 1_000_000_000_000_000_000; // 1e18 ns

  it('kapsam istenenle aynıysa not YOK', () => {
    expect(coveredRangeNote({ coveredFromNs: from, bucketSeconds: 60 }, from)).toBeNull();
  });
  it('kapsam SONRA başlıyorsa not YOK (bu ayrı bir durum)', () => {
    expect(coveredRangeNote({ coveredFromNs: from + 60e9, bucketSeconds: 60 }, from)).toBeNull();
  });
  it('kapsam ÖNCE başlıyorsa kaç saniye önce olduğunu söyler', () => {
    const n = coveredRangeNote({ coveredFromNs: from - 45e9, bucketSeconds: 60 }, from);
    expect(n).toContain('45 sn');
    expect(n).toContain('60 sn');
  });
  it('zarf yoksa / coveredFromNs 0 ise not YOK', () => {
    expect(coveredRangeNote(undefined, from)).toBeNull();
    expect(coveredRangeNote({ coveredFromNs: 0, bucketSeconds: 60 }, from)).toBeNull();
  });
});

describe('partialBucketNote', () => {
  it('kısmi değilse not YOK', () => {
    expect(partialBucketNote({ partialLastBucket: false, bucketSeconds: 60 })).toBeNull();
    expect(partialBucketNote(undefined)).toBeNull();
  });
  it('kısmiyse kova genişliğini söyler ve "düşüş değil" der', () => {
    const n = partialBucketNote({ partialLastBucket: true, bucketSeconds: 20 })!;
    expect(n).toContain('20 sn');
    expect(n).toContain('düşüş');
  });
});
