import { describe, it, expect } from 'vitest';
import { depRowMatches, normalizeDepSearch } from './depRowFilter';

// v0.10.704 — "Called from services" filtresi: db adı koşulsuz eşleşir,
// harf duyarsız, boşluk kırpılır, system seçicisi AND.
const row = { system: 'oracle', cluster: 'prod-eu', instance: 'db-host-1:1521', dbName: 'COREBANK', callers: ['shop-payment', 'shop-cart'] };

describe('depRowMatches', () => {
  it('db adı instance bilinse de eşleşir (denetim bulgusu 1)', () => {
    expect(depRowMatches(row, 'corebank', '')).toBe(true);
    expect(depRowMatches({ ...row, dbName: undefined }, 'corebank', '')).toBe(false);
  });
  it('system / cluster / instance / caller alt-dize, harf duyarsız', () => {
    expect(depRowMatches(row, 'ORA', '')).toBe(false); // terim zaten küçük harfe indirilmiş gelir
    expect(depRowMatches(row, 'ora', '')).toBe(true);
    expect(depRowMatches(row, 'prod-eu', '')).toBe(true);
    expect(depRowMatches(row, '1521', '')).toBe(true);
    expect(depRowMatches(row, 'shop-cart', '')).toBe(true);
    expect(depRowMatches(row, 'nomatch', '')).toBe(false);
  });
  it('queue satırı destination üzerinden', () => {
    expect(depRowMatches({ system: 'kafka', destination: 'orders.v1', callers: [] }, 'orders', '')).toBe(true);
  });
  it('system seçicisi tam eşleşme ve terimle AND', () => {
    expect(depRowMatches(row, '', 'oracle')).toBe(true);
    expect(depRowMatches(row, '', 'postgresql')).toBe(false);
    expect(depRowMatches(row, 'corebank', 'postgresql')).toBe(false);
  });
  it('boş terim her satırı geçirir', () => {
    expect(depRowMatches(row, '', '')).toBe(true);
  });
});

describe('normalizeDepSearch', () => {
  it('kırpar ve küçültür; null/boşluk → ""', () => {
    expect(normalizeDepSearch('  CoreBank ')).toBe('corebank');
    expect(normalizeDepSearch('   ')).toBe('');
    expect(normalizeDepSearch(null)).toBe('');
  });
});
