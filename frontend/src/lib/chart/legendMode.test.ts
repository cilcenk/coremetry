import { describe, it, expect } from 'vitest';
import { legendMode, LEGEND_TABLE_MAX } from './legendMode';

// v0.9.541 — lejant modu seri sayısının FONKSİYONU, kullanıcı tercihi değil.
// Sözleşme burada pinli çünkü iki ayrı bileşen (MultiLineChart uPlot
// lejantı + TimeSeriesPanel'in kendi React tablosu) aynı kararı okuyor;
// ayrışırlarsa aynı sayfadaki iki grafik farklı lejantla çizilir.
describe('legendMode', () => {
  it('az seride TABLO — Last/Min/Max/Avg asıl bilgi', () => {
    expect(legendMode(1)).toBe('table');
    expect(legendMode(3)).toBe('table');
    expect(legendMode(LEGEND_TABLE_MAX)).toBe('table');
  });

  it('eşiğin üstünde LİSTE — sayılar okunmaz, yer kaplar', () => {
    expect(legendMode(LEGEND_TABLE_MAX + 1)).toBe('list');
    expect(legendMode(17)).toBe('list');
    expect(legendMode(40)).toBe('list');
  });

  it('sınır TAM eşikte — kayarsa iki bileşen ayrışır', () => {
    expect(legendMode(6)).toBe('table');
    expect(legendMode(7)).toBe('list');
  });

  it('seri yokken tablo (boş lejant zaten çizilmez)', () => {
    expect(legendMode(0)).toBe('table');
  });
});
