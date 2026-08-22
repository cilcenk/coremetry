import { describe, it, expect } from 'vitest';
import { scorePaletteEntry } from './paletteScore';

// v0.9.1270 — B5#1: etiket davranışı bire bir + alias bulunabilirliği.
describe('scorePaletteEntry', () => {
  it('keeps the legacy label ladder byte-for-byte', () => {
    expect(scorePaletteEntry('traces', 'Traces')).toBe(1000);
    expect(scorePaletteEntry('tra', 'Traces')).toBe(500);
    expect(scorePaletteEntry('race', 'Traces')).toBe(200);
    expect(scorePaletteEntry('tcs', 'Traces')).toBe(50);
    expect(scorePaletteEntry('xyz', 'Traces')).toBe(0);
  });
  it('old name still finds the renamed page via alias, below an exact label', () => {
    // Sidebar nav.inbox → "Problems"; eski palet adı 'Inbox' alias.
    expect(scorePaletteEntry('inbox', 'Problems', ['inbox'])).toBe(900);
    expect(scorePaletteEntry('inb', 'Problems', ['inbox'])).toBe(450);
    // Tam-etiket her zaman alias'ı yener: 'problems' sorgusunda
    // "Problems" etiketli sayfa (1000), alias'la 'problems' taşıyan
    // başka sayfadan (900) önde gelir.
    expect(scorePaletteEntry('problems', 'Problems')).toBe(1000);
    expect(scorePaletteEntry('problems', 'Exceptions', ['problems'])).toBe(900);
  });
  it('no fuzzy on aliases (short aliases would be noise)', () => {
    expect(scorePaletteEntry('ibx', 'Problems', ['inbox'])).toBe(0);
  });
  it('label score wins when higher than alias score', () => {
    expect(scorePaletteEntry('exception', 'Exceptions', ['problems'])).toBe(500);
  });
});
