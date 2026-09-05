import { describe, it, expect } from 'vitest';
import { clampRegion, fitLabel, thresholdVisible, thresholdSoftRange, regionsToScale, assignLanes, mergeIntervals, regionLaneHit, fmtRegionSpan, LANE_H } from './overlays';

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

// v0.10.164 — saniye bölgeleri ms ekseni için ölçeklenir; 1 = kimlik (aynı dizi).
describe('regionsToScale — sn → ms', () => {
  const rg = [{ fromSec: 1_700_000_000, toSec: 1_700_000_600, color: 'var(--warn)', label: 'x' }];
  it('1000 ile fromSec/toSec ms olur, öteki alanlar aynen', () => {
    expect(regionsToScale(rg, 1000)).toEqual([{ fromSec: 1_700_000_000_000, toSec: 1_700_000_600_000, color: 'var(--warn)', label: 'x' }]);
  });
  it('1 → aynı referans (eski saniye motorları için bedava)', () => {
    expect(regionsToScale(rg, 1)).toBe(rg);
  });
  it('ms penceresine karşı saniye bölgesi clampRegion ile ELENİRDİ; ölçekli hâli kesişir', () => {
    const xMin = 1_699_999_000_000, xMax = 1_700_001_000_000; // ms
    expect(clampRegion(rg[0].fromSec, rg[0].toSec, xMin, xMax)).toBeNull();
    const m = regionsToScale(rg, 1000)[0];
    expect(clampRegion(m.fromSec, m.toSec, xMin, xMax)).toEqual({ from: 1_700_000_000_000, to: 1_700_000_600_000 });
  });
});

// v0.10.166 — çakışan bölgeler ayrı şeritlere; ayrık olanlar şerit 0'ı paylaşır.
describe('assignLanes — çakışan bölge etiketleri alt alta', () => {
  const R = (fromSec: number, toSec: number) => ({ fromSec, toSec });
  it('üç tam-pencere bölge → 0,1,2; girdi sırası korunur', () => {
    expect(assignLanes([R(0, 100), R(0, 100), R(0, 100)])).toEqual([0, 1, 2]);
  });
  it('ayrık bölgeler şerit 0; bitişik (bitiş == başlangıç) çakışmaz', () => {
    expect(assignLanes([R(0, 10), R(10, 20), R(30, 40)])).toEqual([0, 0, 0]);
  });
  it('kısmi çakışma: sonrakinin başlangıcı öncekinin bitişinden önceyse yeni şerit; boşalan şerit yeniden kullanılır', () => {
    expect(assignLanes([R(0, 50), R(40, 60), R(55, 70)])).toEqual([0, 1, 0]);
    expect(assignLanes([R(40, 60), R(0, 50)])).toEqual([1, 0]); // sıralama başlangıca göre
  });
  it('boş → boş', () => { expect(assignLanes([])).toEqual([]); });
});

// v0.10.166 — dolgu birleşik aralıklarla tek kat; çakışma koyulaştırmaz.
describe('mergeIntervals — dolgu birleşimi', () => {
  it('çakışan/bitişik aralıklar birleşir, ilk rengi taşır; ayrık kalır; girdi bozulmaz', () => {
    const inp = [{ fromSec: 40, toSec: 60, color: 'b' }, { fromSec: 0, toSec: 50, color: 'a' }, { fromSec: 60, toSec: 70 }, { fromSec: 100, toSec: 110, color: 'c' }];
    expect(mergeIntervals(inp)).toEqual([{ fromSec: 0, toSec: 70, color: 'a' }, { fromSec: 100, toSec: 110, color: 'c' }]);
    expect(inp[0]).toEqual({ fromSec: 40, toSec: 60, color: 'b' });
    expect(mergeIntervals([])).toEqual([]);
  });
});

// v0.10.180 — bant isabeti yalnız şerit satırında; çakışan şeritte son çizilen kazanır.
describe('regionLaneHit — şerit satırı isabeti', () => {
  const items = [{ x1: 10, x2: 200, lane: 0 }, { x1: 50, x2: 120, lane: 1 }, { x1: 150, x2: 300, lane: 0 }];
  it("lane 0 satırında x'e göre; lane 1 satırı ikinci bölge", () => {
    expect(regionLaneHit(items, 20, 5)).toBe(0);
    expect(regionLaneHit(items, 60, LANE_H + 3)).toBe(1);
    expect(regionLaneHit(items, 60, 5)).toBe(0);
    expect(regionLaneHit(items, 250, 5)).toBe(2);
  });
  it('çakışan x aralığında son çizilen üstte; şerit satırı dışında -1; NaN geometri asla isabet etmez', () => {
    expect(regionLaneHit(items, 160, 5)).toBe(2);
    expect(regionLaneHit(items, 20, 40)).toBe(-1);
    expect(regionLaneHit([{ x1: NaN, x2: NaN, lane: 0 }], 20, 5)).toBe(-1);
  });
  it('fmtRegionSpan basamakları', () => {
    expect(fmtRegionSpan(45)).toBe('45 s');
    expect(fmtRegionSpan(720)).toBe('12 dk');
    expect(fmtRegionSpan(12600)).toBe('3.5 sa');
    expect(fmtRegionSpan(180000)).toBe('2.1 g');
    expect(fmtRegionSpan(0)).toBe('—');
  });
});

// v0.10.384 — eşik y ölçeğini kapsar (dış skill denetimi A8).
describe('thresholdSoftRange — eşik ölçeğe girer', () => {
  it('seriden büyük eşik softMax olur; pozitif eşik softMin vermez', () => {
    expect(thresholdSoftRange([{ value: 500 }, { value: 800 }], { log: false, bars: false })).toEqual({ softMax: 800 });
  });
  it('negatif eşik line/area\'da softMin, çubukta değil (taban 0 kalır)', () => {
    expect(thresholdSoftRange([{ value: -5 }], { log: false, bars: false })).toEqual({ softMax: -5, softMin: -5 });
    expect(thresholdSoftRange([{ value: -5 }], { log: false, bars: true })).toEqual({ softMax: -5 });
  });
  it('log ölçek, eşiksiz ve sonlu-olmayan eşik → boş (ölçek seriden)', () => {
    expect(thresholdSoftRange([{ value: 500 }], { log: true, bars: false })).toEqual({});
    expect(thresholdSoftRange([], { log: false, bars: false })).toEqual({});
    expect(thresholdSoftRange([{ value: NaN }], { log: false, bars: false })).toEqual({});
  });
});
