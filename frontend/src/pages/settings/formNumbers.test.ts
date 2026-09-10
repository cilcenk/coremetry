import { describe, expect, it } from 'vitest';
import { listToText, numFromForm, numToForm, parseList } from './formNumbers';

// v0.10.604 — influxForm'dan taşınan ortak çeviriler; sözleşme taşınırken
// çivilendi ki Influx sökümü (şablonun silinmesi) davranışı götürmesin.
describe('formNumbers', () => {
  it('numFromForm: boş/çöp → undefined, sayı → sayı, kırpar', () => {
    expect(numFromForm('')).toBeUndefined();
    expect(numFromForm('  ')).toBeUndefined();
    expect(numFromForm('abc')).toBeUndefined();
    expect(numFromForm(' 42 ')).toBe(42);
    expect(numFromForm('1.5')).toBe(1.5);
    expect(numFromForm('-3')).toBe(-3); // negatifi sunucu reddeder, form geçirir
  });
  it('numToForm: 0/undefined/null → boş kutu (sunucu varsayılanı)', () => {
    expect(numToForm(0)).toBe('');
    expect(numToForm(undefined)).toBe('');
    expect(numToForm(null)).toBe('');
    expect(numToForm(7)).toBe('7');
  });
  it('parseList / listToText: virgül ve yeni satır, kırpma, boş atma, gidiş-dönüş', () => {
    expect(parseList('a, b\n c ,,')).toEqual(['a', 'b', 'c']);
    expect(parseList('')).toEqual([]);
    expect(listToText(['a', 'b'])).toBe('a, b');
    expect(listToText(undefined)).toBe('');
    expect(parseList(listToText(['x', 'y']))).toEqual(['x', 'y']);
  });
});
