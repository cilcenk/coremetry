import { describe, it, expect } from 'vitest';
import {
  AXIS_TICK_SIZE, AXIS_GAP, AXIS_MAX_GUTTER_RATIO,
  niceIncr, maxTicksFor, axisTickPlan, decimalsForIncr,
  estimateLabelWidthPx, axisGutterPx, widestLabelPx,
  seriesExtent, paddedExtent, decimalsForScaledIncr, displayScaleOf, scaleRefTick,
  labelWidthFloorPx, AXIS_EM_PER_CHAR_FLOOR, AXIS_FONT_SIZE,
} from './axisSize';

// v0.9.799 — İKİ operatör-bildirimli kusurun saf çekirdeği:
//   • "00 req/s"  → oluk genişliği en uzun etiketi TAŞIMALI (madde 2).
//   • "0 req/s ×4" → küçük aralıkta tick'ler ondalıklı olmalı (madde 3).
//
// Ölçüm (canvas) burada YOK: node ortamında measureLabelWidthPx kaba
// tahmine düşer ve testler o tahminle sözleşmeyi çiviler — asıl korunan
// şey "etiket oluğa sığar" eşitsizliği, mutlak piksel değeri değil.

describe('niceIncr — uPlot 1/2/5 merdiveni', () => {
  const cases: [number, number, number][] = [
    // [span, maxTicks, beklenen incr]
    [330, 5, 100],   // 0-300 req/s → tam sayı tick
    [0.22, 5, 0.05], // 0-0.2 req/s → ondalıklı tick
    [10, 5, 2],
    [10, 10, 1],
    [1, 5, 0.2],
    [4500, 5, 1000],
    [45, 5, 10],
  ];
  for (const [span, maxTicks, want] of cases) {
    it(`span ${span} / ${maxTicks} tick → ${want}`, () => {
      expect(niceIncr(span, maxTicks)).toBeCloseTo(want, 10);
    });
  }
  it('dejenere girdi 0 döndürür (tek tick)', () => {
    expect(niceIncr(0, 5)).toBe(0);
    expect(niceIncr(-5, 5)).toBe(0);
    expect(niceIncr(Infinity, 5)).toBe(0);
    expect(niceIncr(10, 0)).toBe(0);
  });
});

describe('maxTicksFor — @grafana/ui calculateSpace y dalı', () => {
  it('150px altı panel SIK tick (15px), üstü normal (30px)', () => {
    expect(maxTicksFor(120)).toBe(8);  // 120/15
    expect(maxTicksFor(300)).toBe(10); // 300/30
  });
  it('en az iki tick', () => {
    expect(maxTicksFor(1)).toBe(2);
    expect(maxTicksFor(0)).toBe(2);
  });
});

// ── madde 3: küçük aralıkta ondalık ─────────────────────────────────────
//
// decimalsForIncr, @grafana/ui'nin eksen sarmalayıcısının kullandığı
// guessDecimals(roundDecimals(tickIncr, 6)) ifadesinin ikizi; CorePanel
// oluğu hesaplarken çizilecek etiketi bu ondalıkla üretiyor.
describe('decimalsForIncr', () => {
  const cases: [number, number][] = [
    [100, 0], [50, 0], [1, 0], [1000, 0],
    [0.5, 1], [0.2, 1], [0.1, 1],
    [0.05, 2], [0.02, 2],
    [0.001, 3],
    [0, 0], [NaN, 0],
  ];
  for (const [incr, want] of cases) {
    it(`incr ${incr} → ${want} ondalık`, () => {
      expect(decimalsForIncr(incr)).toBe(want);
    });
  }
});

describe('axisTickPlan — brief tablosu (0-0.2 ondalıklı · 0-300 tam sayı)', () => {
  it('0-300 (throughput) → tam sayı tick, ondalık YOK', () => {
    const p = axisTickPlan(0, 330, 166);
    expect(p.incr).toBe(100);
    expect(p.ticks).toEqual([0, 100, 200, 300]);
    expect(decimalsForIncr(p.incr)).toBe(0);
  });

  it('0-0.2 (failure rate) → 0.05 adımlı ondalıklı tick', () => {
    const p = axisTickPlan(0, 0.22, 166);
    expect(p.incr).toBeCloseTo(0.05, 10);
    expect(p.ticks).toEqual([0, 0.05, 0.1, 0.15, 0.2]);
    // ASIL KAPI: bu aralıkta ondalık ZORUNLU — 0 ondalıkla dört tick de
    // "0" yazardı, operatörün gördüğü kusurun ta kendisi.
    expect(decimalsForIncr(p.incr)).toBe(2);
  });

  it('kayan nokta birikimi yok — 0.30000000000000004 üretilmez', () => {
    const p = axisTickPlan(0, 0.55, 166);
    for (const t of p.ticks) {
      expect(String(t).length).toBeLessThanOrEqual(5);
    }
  });

  it('log ölçek DECADE üretir (lineer merdiven değil)', () => {
    const p = axisTickPlan(1, 1000, 200, true);
    expect(p.ticks).toEqual([1, 10, 100, 1000]);
  });

  it('dejenere: tek değer / geçersiz uç', () => {
    expect(axisTickPlan(5, 5, 200).ticks).toEqual([5]);
    expect(axisTickPlan(NaN, 5, 200).ticks).toEqual([]);
  });
});

// ── madde 2: etiket oluğa SIĞAR ─────────────────────────────────────────
describe('axisGutterPx — "formatlanmış tick eksen boyutunu aşmaz"', () => {
  // Bu diziler gerçek çıktıdır: @grafana/data'nın display processor'ı
  // reqps/ms birimlerinde tam olarak bunları üretiyor (ölçüldü, v0.9.799).
  const REQPS = ['0 req/s', '100 req/s', '200 req/s', '300 req/s', '1.04K req/s'];
  const MS = ['0 ms', '300 ms', '1.04 s', '25 mins'];

  for (const [unit, labels] of [['req/s', REQPS], ['ms/s', MS]] as const) {
    it(`${unit}: oluk her etiketi + çentik + boşluğu taşır`, () => {
      const w = widestLabelPx(labels, '12px sans-serif');
      const gutter = axisGutterPx(w);
      for (const l of labels) {
        expect(gutter).toBeGreaterThanOrEqual(
          estimateLabelWidthPx(l) + AXIS_TICK_SIZE + AXIS_GAP);
      }
    });
  }

  it('en uzun etiket oluğu belirler — kısası daralt(ma)maz', () => {
    const wide = axisGutterPx(widestLabelPx(REQPS, '12px sans-serif'));
    const narrow = axisGutterPx(widestLabelPx(['0 req/s'], '12px sans-serif'));
    expect(wide).toBeGreaterThan(narrow);
  });

  it('Grafana tavanı korunur: oluk panelin %40\'ını aşamaz', () => {
    const g = axisGutterPx(5000, 600);
    expect(g).toBeLessThanOrEqual(
      Math.ceil(600 * AXIS_MAX_GUTTER_RATIO) + AXIS_TICK_SIZE + AXIS_GAP + 4);
  });

  it('boş / geçersiz ölçüm oluğu negatife düşürmez', () => {
    expect(axisGutterPx(0)).toBeGreaterThan(0);
    expect(axisGutterPx(NaN)).toBeGreaterThan(0);
    expect(axisGutterPx(-10)).toBeGreaterThan(0);
  });
});

describe('estimateLabelWidthPx — canvas yokken kaba üst sınır', () => {
  it('uzunlukla monoton artar', () => {
    expect(estimateLabelWidthPx('1')).toBeLessThan(estimateLabelWidthPx('100'));
    expect(estimateLabelWidthPx('100')).toBeLessThan(estimateLabelWidthPx('100 req/s'));
  });
  it('boş etiket 0', () => {
    expect(estimateLabelWidthPx('')).toBe(0);
  });
});

describe('seriesExtent / paddedExtent', () => {
  it('null ve NaN uçları KİRLETMEZ', () => {
    expect(seriesExtent([[1, null, 3], [null, 2, NaN as unknown as number]])).toEqual([1, 3]);
  });
  it('hiç sayı yoksa null', () => {
    expect(seriesExtent([[null, null], []])).toBeNull();
    expect(seriesExtent([])).toBeNull();
  });
  it('pozitif veride taban 0\'a mıhlanır (uPlot soft-0) — hayali negatif tick yok', () => {
    expect(paddedExtent([10, 110])).toEqual([0, 120]);
  });
  it('negatif veride iki uç da dolgulanır', () => {
    const [lo, hi] = paddedExtent([-10, 10]);
    expect(lo).toBeCloseTo(-12, 10);
    expect(hi).toBeCloseTo(12, 10);
  });
  it('düz seri (min === max) sıfır genişlikli aralık üretmez', () => {
    const [lo, hi] = paddedExtent([5, 5]);
    expect(hi).toBeGreaterThan(lo);
  });
});

// ── v0.9.1368 — tick ondalığı ADIMDAN gelir, büyüklükten değil ────────────
//
// Operatör-bildirimi: "Clusters altında memory hep aynı değeri
// gösteriyor" — dört y tick'i de "6.85 TiB", tooltip de öyle, çizgi ise
// gözle görülür oynuyor. 6.85 TiB'de 2 ondalık ≈ 10 GiB çözünürlük.
//
// Değer+birim şablonu → CLAUDE.md unit-mixing disiplini: HER birim
// ailesi ayrı satır (bytes/ms/s/req-s), hem ölçekli hem ölçeksiz.
describe('decimalsForScaledIncr', () => {
  const KiB = 1024, MiB = 1024 ** 2, GiB = 1024 ** 3, TiB = 1024 ** 4;
  const cases: [string, number, number, number][] = [
    // [ad, ham tick adımı, ölçek (ham/gösterilen), beklenen ondalık]
    // — bytes ailesi: adım TiB'ye küçülünce ondalık BÜYÜR (bug'ın özü)
    ['bytes: 2 GiB adım, TiB ekseni', 2 * GiB, TiB, 3],
    ['bytes: 512 MiB adım, TiB ekseni', 512 * MiB, TiB, 4],
    ['bytes: 256 GiB adım, TiB ekseni', 256 * GiB, TiB, 1],
    ['bytes: 1 TiB adım, TiB ekseni', TiB, TiB, 0],
    ['bytes: 2 TiB adım, TiB ekseni', 2 * TiB, TiB, 0],
    ['bytes: 128 MiB adım, GiB ekseni', 128 * MiB, GiB, 1],
    ['bytes: 64 KiB adım, MiB ekseni', 64 * KiB, MiB, 2],
    ['bytes: 1 KiB adım, B ekseni (ölçek yok)', KiB, 1, 0],
    // — süre ailesi: ns→ms, ms→s
    ['ms: 250 ms adım, s ekseni', 250, 1000, 1],
    ['ms: 5 ms adım, s ekseni', 5, 1000, 3],
    ['ns: 1e6 ns adım, ms ekseni', 1e6, 1e6, 0],
    // — oran ailesi: ölçek 1, v0.9.799 ekseni AYNEN korunur
    ['req/s: 0.05 adım, ölçeksiz', 0.05, 1, 2],
    ['req/s: 60 adım, ölçeksiz', 60, 1, 0],
    ['req/s: 0.1 adım, ölçeksiz', 0.1, 1, 1],
    ['percent: 0.25 adım, ölçeksiz', 0.25, 1, 2],
  ];
  it.each(cases)('%s', (_n, incr, scale, want) => {
    expect(decimalsForScaledIncr(incr, scale)).toBe(want);
  });

  it('ondalık ASLA düşmez — her vakada decimalsForIncr tabanı korunur', () => {
    for (const [, incr, scale] of cases) {
      expect(decimalsForScaledIncr(incr, scale)).toBeGreaterThanOrEqual(decimalsForIncr(incr));
    }
  });

  it('bozuk girdi bugünkü davranışa düşer (taban), patlamaz', () => {
    expect(decimalsForScaledIncr(0, 1024)).toBe(0);
    expect(decimalsForScaledIncr(NaN, 1024)).toBe(0);
    expect(decimalsForScaledIncr(Infinity, 1024)).toBe(0);
    expect(decimalsForScaledIncr(0.05, 0)).toBe(2);
    expect(decimalsForScaledIncr(0.05, -1)).toBe(2);
    expect(decimalsForScaledIncr(0.05, NaN)).toBe(2);
  });

  it('tavan var — mikroskobik adım etiketi sonsuza uzatmaz', () => {
    expect(decimalsForScaledIncr(1, 1e30)).toBe(6);
  });
});

describe('displayScaleOf', () => {
  it('ölçeği biçimli metinden geri okur', () => {
    expect(displayScaleOf(6.85 * 1024 ** 4, '6.85')).toBeCloseTo(1024 ** 4, -6);
    expect(displayScaleOf(1500, '1.5')).toBeCloseTo(1000, 6);
  });
  it('binlik ayraç ölçeği 1000 kat yanıltmaz', () => {
    expect(displayScaleOf(1024, '1,024')).toBeCloseTo(1, 6);
  });
  it('ayrıştırılamayan/sıfır girdi → 1 (ölçek yok, davranış değişmez)', () => {
    expect(displayScaleOf(100, 'N/A')).toBe(1);
    expect(displayScaleOf(100, '0')).toBe(1);
    expect(displayScaleOf(0, '0')).toBe(1);
  });
});


describe('scaleRefTick', () => {
  it('mutlak değerce en büyük tick — eksenin hâkim birimini o belirler', () => {
    expect(scaleRefTick([994, 996, 998, 1000])).toBe(1000);
    expect(scaleRefTick([0, 1024, 2048])).toBe(2048);
  });
  it('negatif tabanlı eksende işareti KORUR', () => {
    expect(scaleRefTick([-8000, -2000, 1000])).toBe(-8000);
  });
  it('ilk tick DEĞİL — küçük birimden ölçek okunması bug\'ın ta kendisi', () => {
    expect(scaleRefTick([994, 1000])).not.toBe(994);
  });
  it('boş/bozuk küme → 0 (çağıran ölçeği 1 sayar)', () => {
    expect(scaleRefTick([])).toBe(0);
    expect(scaleRefTick([NaN, Infinity])).toBe(0);
  });
});

// v0.10.191 — ölçüm tabanı: yedek/dar font ölçümü oluğu kırptırmaz.
describe('labelWidthFloorPx (v0.10.191)', () => {
  it('punto font dizesinden, 0.55 em/karakter; punto yoksa AXIS_FONT_SIZE', () => {
    expect(labelWidthFloorPx('4.407 GiB', '12px Inter, Arial')).toBeCloseTo(9 * 12 * AXIS_EM_PER_CHAR_FLOOR, 6);
    expect(labelWidthFloorPx('0.15 cores', '10px x')).toBeCloseTo(10 * 10 * AXIS_EM_PER_CHAR_FLOOR, 6);
    expect(labelWidthFloorPx('ab', 'bold serif')).toBeCloseTo(2 * AXIS_FONT_SIZE * AXIS_EM_PER_CHAR_FLOOR, 6);
  });
  it('taban kaba tahminin (0.62 em) ALTINDA — normal ölçümü büyütmez', () => {
    expect(labelWidthFloorPx('100 req/s', '12px Inter')).toBeLessThan(estimateLabelWidthPx('100 req/s'));
  });
});

