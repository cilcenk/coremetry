import { describe, expect, it } from 'vitest';
import type { SpanMetricSeries } from '@/lib/types';
import {
  ROOT_OP_COLORS, ROOT_OP_TOP_N,
  buildRootOpItems, rankRootOps, rootOpColorAt,
  rootOpLineLabel, rootOpMoreNote, rootOpName,
} from './rootOpSeries';

// v0.9.484 — Response time kartının operasyon kırılımı. Testlenen saf
// çekirdek: etiket türetimi, alan sıralaması, union hizalama ve "+N daha"
// dürüstlük notu. Hizalama testi özellikle önemli: ChartCard değerleri
// İNDEKSLE zaman eksenine eşler, dolayısıyla farklı bucket kümelerine sahip
// iki operasyon ham points ile verilirse çizgi sessizce yanlış zamana kayar.

const s = (name: string, pts: Array<[number, number]>): SpanMetricSeries => ({
  groupKey: [name],
  points: pts.map(([time, value]) => ({ time, value })),
});

describe('rootOpName / rootOpLineLabel', () => {
  it('groupKey ilk boş-olmayan elemanı ad olur', () => {
    expect(rootOpName(['GET /orders'])).toBe('GET /orders');
    expect(rootOpName(['', 'consume topic'])).toBe('consume topic');
  });

  it('boş / eksik groupKey → (adsız) — lejantta görünmez satır olmasın', () => {
    expect(rootOpName([])).toBe('(adsız)');
    expect(rootOpName(['', ''])).toBe('(adsız)');
    expect(rootOpName(undefined)).toBe('(adsız)');
  });

  it('etiket agg\'ini taşır: "op · P95"', () => {
    expect(rootOpLineLabel(['GET /orders'])).toBe('GET /orders · P95');
    expect(rootOpLineLabel([])).toBe('(adsız) · P95');
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

describe('rootOpColorAt', () => {
  it('kap içinde renk tekrarı YOK', () => {
    const used = new Set(Array.from({ length: ROOT_OP_TOP_N }, (_, i) => rootOpColorAt(i)));
    expect(used.size).toBe(ROOT_OP_TOP_N);
  });

  it('yalnız token döner (hex yok) ve --err kullanılmaz', () => {
    for (const c of ROOT_OP_COLORS) {
      expect(c).toMatch(/^var\(--[a-z0-9-]+\)$/);
      expect(c).not.toBe('var(--err)');
    }
  });

  it('kap dışına taşan indeks sarar (negatif dahil)', () => {
    expect(rootOpColorAt(ROOT_OP_TOP_N)).toBe(rootOpColorAt(0));
    expect(rootOpColorAt(-1)).toBe(ROOT_OP_COLORS[ROOT_OP_COLORS.length - 1]);
  });
});

describe('rootOpMoreNote — iki ayrı yalan ayrı ayrı söylenir', () => {
  const cases: Array<[desc: string, total: number, shown: number, capped: boolean, want: string | null]> = [
    ['kırpma yok → not yok', 3, 3, false, null],
    ['total < shown (sunucu totalSeries vermedi) → not yok', 2, 5, false, null],
    ['12 operasyondan 5 çizildi', 12, 5, false, '+7 operasyon daha (alan bazlı kırpıldı)'],
    ['tek fazla', 6, 5, false, '+1 operasyon daha (alan bazlı kırpıldı)'],
    ['satır tavanı tek başına', 5, 5, true, '⚠ satır tavanı doldu — liste eksik olabilir'],
    ['ikisi birden', 12, 5, true,
      '+7 operasyon daha (alan bazlı kırpıldı) · ⚠ satır tavanı doldu — liste eksik olabilir'],
  ];
  it.each(cases)('%s', (_d, total, shown, capped, want) => {
    expect(rootOpMoreNote(total, shown, capped)).toBe(want);
  });
});

// v0.9.844 — `buildRootOpLines` describe bloğu (7 vaka) SİLİNDİ:
// fonksiyonun kendisi eski motorla birlikte gitti (tek tüketicisi
// Service Overview'un ChartCard rt-ops paneliydi). Hizalama/sıralama/
// "+N daha" kontratı kaybolmadı — aşağıdaki buildRootOpItems bloğu
// AYNI çekirdeği (rankRootOps + rootOpMoreNote) kapılıyor.

// v0.9.728 — v2 projeksiyonu: CorePanelMulti item'ları. Hizalama BİLEREK
// yok (CorePanel frame birleşimi yapar) — ham points korunmalı; sıralama
// ve not, lines projeksiyonuyla aynı çekirdekten (rankRootOps/moreNote).
describe('buildRootOpItems (v2)', () => {
  it('alan sırasına göre ilk N, ham points korunur, not taşınır', () => {
    const res = {
      series: [
        s('low', [[1, 1], [2, 1]]),
        s('high', [[1, 100], [3, 100]]),
        s('mid', [[2, 50]]),
      ],
      totalSeries: 9,
      rowsCapped: false,
    };
    const { items, note } = buildRootOpItems(res as never, 2);
    expect(items.map(i => i.name)).toEqual(['high · P95', 'mid · P95']);
    // Ham points aynen: union hizalama v2'de CorePanel'in işi.
    expect(items[0].series[0].points).toEqual([
      { time: 1, value: 100 }, { time: 3, value: 100 },
    ]);
    expect(items.every(i => i.role === 'data')).toBe(true);
    expect(note).toContain('+7 operasyon daha');
  });

  it('boş zarf: boş items + tavan notu yok', () => {
    const { items, note } = buildRootOpItems(undefined);
    expect(items).toEqual([]);
    expect(note).toBeNull();
  });

  it('rowsCapped tavan uyarısı nota girer', () => {
    const { note } = buildRootOpItems({ series: [s('a', [[1, 1]])], rowsCapped: true } as never);
    expect(note).toContain('satır tavanı');
  });
});
