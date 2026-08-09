// volumeSeries.test.ts — /traces histogramının EKSEN SÖZLEŞMESİNİ pinler
// (v0.9.843, operatör isteği: "süre solda, sayı sağda" — Grafana düzeni).
//
// Neden test: eksen alanı üç seride tek kelimelik bir string ('left' /
// 'right'). Bir refactor sırasında sessizce geri dönerse hiçbir tip hatası
// vermez, hiçbir gate yakalamaz — yalnız grafik yanlış okunur. Buradaki
// tablo, takasın hangi seride olduğunu ve süre biçimlendiricisinin HER
// birim dalını (ms / s, v0.6.36 birim-karışımı disiplini) sabitler.

import { describe, expect, it } from 'vitest';
import type { SpanMetricSeries } from '@/lib/types';
import { buildVolumeSeries, fmtVolumeDuration } from './volumeSeries';

const S = 1_000_000_000; // 1 saniye, ns
const T0 = 1_700_000_000 * S;

/** ns damgalı seri kurucu; step saniye cinsinden. */
function mk(values: (number)[], stepSec = 300): SpanMetricSeries[] {
  return [{ groupKey: [], points: values.map((v, i) => ({ time: T0 + i * stepSec * S, value: v })) }];
}

const byKey = (cfg: ReturnType<typeof buildVolumeSeries>, k: string) =>
  cfg.series.find(s => s.key === k)!;

describe('buildVolumeSeries — eksen eşlemesi (v0.9.843 Grafana düzeni)', () => {
  const cfg = buildVolumeSeries(mk([10, 20, 30]), mk([1, 2, 3]), mk([120, 130, 140]));

  // TABLO: seri → beklenen eksen. Takasın TEK kaynağı bu.
  it.each([
    ['total', 'right', 'bar'],   // span sayısı → SAĞ
    ['error', 'right', 'bar'],   // span sayısı → SAĞ
    ['p50', 'left', 'line'],     // SÜRE → SOL
  ] as const)('%s serisi %s eksende ve %s tipinde', (key, axis, type) => {
    const s = byKey(cfg, key);
    expect(s.axis).toBe(axis);
    expect(s.type).toBe(type);
  });

  it('süre ekseni ile sayı ekseni AYRI taraflarda (çift eksen korunur)', () => {
    expect(byKey(cfg, 'p50').axis).not.toBe(byKey(cfg, 'total').axis);
  });

  it('çizim sırası korunur: tam bar → hata payı → p50 çizgisi en üstte', () => {
    expect(cfg.series.map(s => s.key)).toEqual(['total', 'error', 'p50']);
  });
});

describe('buildVolumeSeries — veri dönüşümü', () => {
  it('ns → unix saniye ve bucket dakikası adımdan türer', () => {
    const cfg = buildVolumeSeries(mk([1, 2, 3], 300), null, null);
    expect(cfg.times).toEqual([1_700_000_000, 1_700_000_300, 1_700_000_600]);
    expect(cfg.bucketMin).toBe(5);
  });

  it('tek bucket ya da alt-dakika adımda bucketMin en az 1', () => {
    expect(buildVolumeSeries(mk([1]), null, null).bucketMin).toBe(1);
    expect(buildVolumeSeries(mk([1, 2], 30), null, null).bucketMin).toBe(1);
  });

  it('boş count → boş konfig (grafik yerine Empty durumu)', () => {
    const cfg = buildVolumeSeries(null, mk([1]), mk([2]));
    expect(cfg.times).toEqual([]);
    expect(cfg.series).toEqual([]);
    expect(cfg.bucketMin).toBe(1);
  });

  it('hata payı toplamı aşamaz, eksik bucket 0 olur', () => {
    const errs: SpanMetricSeries[] = [{
      groupKey: [],
      points: [{ time: T0, value: 99 }], // toplam 10 — kırpılmalı
    }];
    const cfg = buildVolumeSeries(mk([10, 20]), errs, null);
    expect(byKey(cfg, 'error').data).toEqual([10, 0]);
  });

  it('p50 GAP korunur: örneği olmayan ya da 0 dönen bucket null (v0.9.73)', () => {
    const p50: SpanMetricSeries[] = [{
      groupKey: [],
      points: [{ time: T0, value: 0 }, { time: T0 + 300 * S, value: 42 }],
    }];
    const cfg = buildVolumeSeries(mk([1, 2, 3]), null, p50);
    expect(byKey(cfg, 'p50').data).toEqual([null, 42, null]);
  });
});

// Değer+birim şablonu → HER birim dalı (v0.6.36). Bu fonksiyon artık SOL
// ekseni biçimlendiriyor; eşiklerin ikisi de (1000ms → s, 10000ms → ondalıksız)
// ve <10ms ondalık dalı burada sabit.
describe('fmtVolumeDuration — her birim dalı', () => {
  it.each([
    [0, '0.0ms'],
    [0.9, '0.9ms'],
    [9.94, '9.9ms'],
    [10, '10ms'],
    [125.4, '125ms'],
    [999, '999ms'],
    [1000, '1.0s'],
    [3100, '3.1s'],
    [9999, '10.0s'],
    [10000, '10s'],
    [65000, '65s'],
  ])('%dms → %s', (v, want) => {
    expect(fmtVolumeDuration(v)).toBe(want);
  });

  it('ms dalı hiç "s" ile, s dalı hiç "ms" ile bitmez (birim karışımı)', () => {
    for (const v of [0, 1, 42, 999.4]) expect(fmtVolumeDuration(v).endsWith('ms')).toBe(true);
    for (const v of [1000, 5000, 120000]) expect(fmtVolumeDuration(v).endsWith('s')).toBe(true);
    for (const v of [1000, 5000, 120000]) expect(fmtVolumeDuration(v).endsWith('ms')).toBe(false);
  });
});
