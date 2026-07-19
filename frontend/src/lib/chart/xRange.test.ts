import { describe, it, expect } from 'vitest';
import { xRangePinned } from './xRange';

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
