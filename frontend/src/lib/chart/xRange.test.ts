import { describe, it, expect } from 'vitest';
import { timeScaleRange, xRangePinned } from './xRange';

// v0.9.93 — x-ekseni veriye-fit'e GERİ ALINDI (v0.9.83 pinning prod'da
// veriyi "belirli bir alana" sıkıştırıyordu). Helper artık uPlot'un
// verdiği veri uçlarını (reqMin/reqMax) aynen döndürür — pin/times yok
// sayılır.

describe('xRangePinned (veriye-fit, v0.9.93 revert)', () => {
  const times = [1000, 1100, 1200];
  const pin = { from: 900, to: 2000 };

  it('pin verilse de ekseni uzatmaz — veri uçları döner', () => {
    expect(xRangePinned(times, pin, 1000, 1200)).toEqual([1000, 1200]);
  });
  it('zoom/dar istek aynen geçer', () => {
    expect(xRangePinned(times, pin, 1050, 1150)).toEqual([1050, 1150]);
  });
  it('pin yok → veri uçları', () => {
    expect(xRangePinned(times, null, 1000, 1200)).toEqual([1000, 1200]);
    expect(xRangePinned(times, undefined, 1050, 1150)).toEqual([1050, 1150]);
  });
  it('times boş olsa da reqMin/reqMax döner', () => {
    expect(xRangePinned([], pin, 300, 800)).toEqual([300, 800]);
  });
});

// v0.9.1042 (operator-reported): Clusters grafiklerinde 3h pencere
// eksende 00:00–21:00 olarak çiziliyordu. CorePanel `xRange` yokken
// `range: undefined` bırakıyor, @grafana/ui zaman eksenine SAYISAL
// pad'li rangeFn kuruyordu. Bu tablo iki sözleşmeyi mühürler:
// pin → sorgu penceresine mıhlı (sn→ms), pin yok → veri uçları AYNEN
// (pad/yuvarlama YOK).
describe('timeScaleRange (CorePanel x-scale, v0.9.1042)', () => {
  // 2026-08-15 09:00–12:00 TR penceresi, unix saniye.
  const pin = { from: 1786773600, to: 1786784400 };

  it('pin → sorgu penceresi, saniye→ms', () => {
    expect(timeScaleRange(pin, 1786774000_000, 1786780000_000))
      .toEqual([1786773600_000, 1786784400_000]);
  });
  it('pin varken veri boş olsa da pencere döner (boş pencere ekseni)', () => {
    expect(timeScaleRange(pin, null, null))
      .toEqual([1786773600_000, 1786784400_000]);
  });
  it('pin yok → veri uçları bayt-bayt aynen (pad yok)', () => {
    expect(timeScaleRange(null, 1786774000_000, 1786784800_000))
      .toEqual([1786774000_000, 1786784800_000]);
    expect(timeScaleRange(undefined, 1786774000_000, 1786784800_000))
      .toEqual([1786774000_000, 1786784800_000]);
  });
  it('pin yok + veri boş → [null, null] (uPlot boş-veri sözleşmesi)', () => {
    expect(timeScaleRange(null, null, null)).toEqual([null, null]);
  });
});
