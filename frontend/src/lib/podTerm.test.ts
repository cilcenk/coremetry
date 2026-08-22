import { describe, it, expect } from 'vitest';
import { termReasonTone } from './podTerm';

// v0.9.1276 (Dynatrace-parite #5) — son-sonlanma rozetinin tonu.
// Kilitlenen sözleşme: OOMKilled kırmızı, Completed nötr, BİLİNMEYEN
// her ad "warn" (whitelist değil) — KSM yeni bir sebep basınca rozet
// sessizce nötrleşip kaybolmasın.
describe('termReasonTone', () => {
  it('OOMKilled kırmızı — aranan sinyal', () => {
    expect(termReasonTone('OOMKilled')).toBe('err');
  });

  it('Completed nötr — normal çıkış hata değil', () => {
    expect(termReasonTone('Completed')).toBe('gray');
  });

  it('boş sebep nötr (çağıran zaten rozet çizmez)', () => {
    expect(termReasonTone('')).toBe('gray');
  });

  it.each([
    'Error',
    'ContainerCannotRun',
    'StartError',
    'DeadlineExceeded',
    'Evicted',
  ])('bilinen hata sebebi warn: %s', reason => {
    expect(termReasonTone(reason)).toBe('warn');
  });

  it('BİLİNMEYEN sebep warn olmalı — gray değil (whitelist yok)', () => {
    // KSM'nin ileride ekleyeceği bir ad. gray'e düşerse operatör
    // rozeti fark etmez; bu test o regresyonu tutar.
    expect(termReasonTone('SomeFutureKSMReason')).toBe('warn');
  });

  it('büyük/küçük harf sapması sebebi nötrleştirmez', () => {
    // Tam eşleşme aranıyor; "oomkilled" OOMKilled değildir ama yine
    // de dikkat çeken tonda kalmalı (gray'e DÜŞMEMELİ).
    expect(termReasonTone('oomkilled')).toBe('warn');
  });
});
