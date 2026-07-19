import { describe, it, expect } from 'vitest';
import { xRangePinned } from './xRange';

// uPlot Aşama 2 madde 2 — x-scale sorgu-penceresi sabitleme kararı.
// Kontrat: auto-fit → pin ∪ veri; zoom (dar istek) → istek aynen geçer.

describe('xRangePinned', () => {
  const times = [1000, 1100, 1200]; // veri erken bitmiş (sorgu 900–2000)
  const pin = { from: 900, to: 2000 };

  it('auto-fit (istek = veri aralığı) → sorgu penceresine genişler', () => {
    // emit etmeyi bırakan servis: eksen artık 2000'e kadar uzanır
    expect(xRangePinned(times, pin, 1000, 1200)).toEqual([900, 2000]);
  });
  it('zoom (veriden dar istek) → istek aynen geçer, drag-zoom kırılmaz', () => {
    expect(xRangePinned(times, pin, 1050, 1150)).toEqual([1050, 1150]);
  });
  it('veri pencereden taşarsa union taşanı gizlemez', () => {
    // saat kayması / geniş fetch: veri 800'den başlıyor, pin 900'den
    expect(xRangePinned([800, 1500, 2500], pin, 800, 2500)).toEqual([800, 2500]);
  });
  it('pin yok → istek aynen (mevcut davranış birebir)', () => {
    expect(xRangePinned(times, null, 1000, 1200)).toEqual([1000, 1200]);
    expect(xRangePinned(times, undefined, 1050, 1150)).toEqual([1050, 1150]);
  });
  it('bozuk pin (to <= from) → yok sayılır', () => {
    expect(xRangePinned(times, { from: 2000, to: 900 }, 1000, 1200)).toEqual([1000, 1200]);
  });
  it('boş veri → doğrudan pin (boş grafik yine pencereyi gösterir)', () => {
    expect(xRangePinned([], pin, 0, 0)).toEqual([900, 2000]);
  });
  it('tolerans: kenar bucket kayması auto-fit sayılır', () => {
    expect(xRangePinned(times, pin, 1000.3, 1199.8)).toEqual([900, 2000]);
  });
});
