import { describe, expect, it } from 'vitest';
import type { SpanMetricSeries } from '@/lib/types';
import { ROOT_OP_TOP_N, rankRootOps, rootOpName } from './rootOpSeries';

// v0.9.484 — Response time kartının operasyon kırılımı. v0.9.845 sonrası bu
// dosyada YALNIZ hâlâ canlı olan çekirdek test ediliyor: ad türetimi ve alan
// sıralaması. İkisinin de tüketicisi routeSeries (Response time · by route)
// — panel kırılımı gitti, SIRALAMA ÖLÇÜTÜ kaldı.

const s = (name: string, pts: Array<[number, number]>): SpanMetricSeries => ({
  groupKey: [name],
  points: pts.map(([time, value]) => ({ time, value })),
});

describe('rootOpName', () => {
  it('groupKey ilk boş-olmayan elemanı ad olur', () => {
    expect(rootOpName(['GET /orders'])).toBe('GET /orders');
    expect(rootOpName(['', 'consume topic'])).toBe('consume topic');
  });

  it('boş / eksik groupKey → (adsız) — lejantta görünmez satır olmasın', () => {
    expect(rootOpName([])).toBe('(adsız)');
    expect(rootOpName(['', ''])).toBe('(adsız)');
    expect(rootOpName(undefined)).toBe('(adsız)');
  });
});

describe('rankRootOps', () => {
  it('alana göre azalan sıralar ve kap uygular', () => {
    const input = [
      s('a', [[1, 1], [2, 1]]),      // alan 2
      s('b', [[1, 50], [2, 50]]),    // alan 100
      s('c', [[1, 10], [2, 10]]),    // alan 20
    ];
    expect(rankRootOps(input, 2).map(x => x.groupKey[0])).toEqual(['b', 'c']);
  });

  it('eşit alanda ada göre — aynı veri hep aynı renk sırası', () => {
    const input = [s('zeta', [[1, 5]]), s('alpha', [[1, 5]])];
    expect(rankRootOps(input).map(x => x.groupKey[0])).toEqual(['alpha', 'zeta']);
  });

  it('null/eksik değerler alanı bozmaz, girdi mutasyona uğramaz', () => {
    const input = [s('a', [[1, 3]]), s('b', [[1, 9]])];
    const before = input.map(x => x.groupKey[0]);
    rankRootOps(input);
    expect(input.map(x => x.groupKey[0])).toEqual(before);
  });

  it('varsayılan kap ROOT_OP_TOP_N', () => {
    const many = Array.from({ length: 12 }, (_, i) => s(`op${i}`, [[1, i + 1]]));
    expect(rankRootOps(many)).toHaveLength(ROOT_OP_TOP_N);
  });
});

// v0.9.845 — üç describe bloğu daha SİLİNDİ (toplam 12 vaka), hepsi v0.9.844
// motor sökümünün bıraktığı öksüzler:
//   • rootOpColorAt / ROOT_OP_COLORS (3) — indeks-paletiydi. v2'de renk
//     CorePanel'in işi (seriesRole sözleşmesi: aynı operasyon her yüzeyde
//     aynı renk), yani palet zaten devre dışıydı.
//   • rootOpMoreNote (6) — "+N daha" dürüstlük notu. KONTRAT KAYBOLMADI:
//     routeSeries.routeMoreNote aynı iki-yalan sözleşmesini (more +
//     rowsCapped) canlı yolda taşıyor ve kendi describe bloğunda test edili.
//   • buildRootOpItems (3) — v2 projeksiyonu, ama panelin kendisi v0.9.736'da
//     operatör düzeniyle kalkmıştı; v0.9.844 hook'u (useRootOpLatency) silince
//     projeksiyonun çağıranı kalmadı.
