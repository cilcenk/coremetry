import { describe, it, expect } from 'vitest';
import { cappedLetters } from './RowsCappedNote';
import type { PanelData } from './PanelStack';

// v0.9.809 — satır tavanı uyarısı grafiksiz görünümlerde de görünmeli.
// v0.9.458'in şeridi QueryPanel'in başlığındaydı ve QueryPanel yalnız
// çizgi ailesinde çiziliyor: viz='table'/'stat'/'toplist'/'pie' modunda
// aynı kırpılmış veri UYARISIZ gösteriliyordu.

const panel = (letter: string, rowsCapped?: boolean): PanelData => ({
  key: letter, letter, desc: '', unit: '', isFormula: false,
  state: 'ready', series: [], more: 0, rowsCapped,
});

describe('cappedLetters', () => {
  it('yalnız tavana çarpan harfler, ALFABETİK', () => {
    expect(cappedLetters([
      panel('C', true), panel('A', false), panel('B', true),
    ])).toEqual(['B', 'C']);
  });

  it('hiçbiri çarpmadıysa boş — şerit hiç çizilmez', () => {
    expect(cappedLetters([panel('A'), panel('B', false)])).toEqual([]);
  });

  it('boş panel listesi güvenli', () => {
    expect(cappedLetters([])).toEqual([]);
  });
});
