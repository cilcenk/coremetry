import { describe, it, expect } from 'vitest';
import { worstSaturation, saturationLabel, saturationTone } from './dbSaturation';
import type { DBSaturationRow } from './types';

const row = (o: Partial<DBSaturationRow>): DBSaturationRow => ({
  system: 'oracle', instance: 'i', check: 'sessions',
  usage: 0, limit: 100, pct: 0, ...o,
});

// dbSaturation regresyon testleri — v0.9.822.
//
// Karonun sayısı ile karonun ADI aynı satırdan çıkmak ZORUNDA; ayrı
// hesaplansalar karo "%92" deyip başka bir instance'ı işaret edebilirdi.

describe('worstSaturation', () => {
  it('boş liste → null (çağıran karoyu HİÇ KURMAZ)', () => {
    expect(worstSaturation([])).toBeNull();
  });

  it('en yüksek yüzdeyi seçer', () => {
    const w = worstSaturation([
      row({ instance: 'a', pct: 12.9 }),
      row({ instance: 'b', pct: 91.2 }),
      row({ instance: 'c', pct: 5.6 }),
    ]);
    expect(w?.instance).toBe('b');
  });

  it('eşitlikte MUTLAK kalan alan küçük olanı seçer (yüzde ölçek saklar)', () => {
    const w = worstSaturation([
      row({ instance: 'buyuk', pct: 90, usage: 9000, limit: 10000 }), // 1000 kaldı
      row({ instance: 'kucuk', pct: 90, usage: 90, limit: 100 }),      // 10 kaldı
    ]);
    expect(w?.instance).toBe('kucuk');
  });

  it('NaN yüzdeli satırı atlar, çökmez', () => {
    const w = worstSaturation([
      row({ instance: 'bozuk', pct: NaN }),
      row({ instance: 'saglam', pct: 4 }),
    ]);
    expect(w?.instance).toBe('saglam');
  });

  it('yalnız bozuk satır varsa null döner', () => {
    expect(worstSaturation([row({ pct: NaN })])).toBeNull();
  });

  it('tavanı aşmış havuz (>%100) gerçek bir hâl, seçilebilir', () => {
    const w = worstSaturation([row({ instance: 'asmis', pct: 120 }), row({ pct: 99 })]);
    expect(w?.instance).toBe('asmis');
  });
});

describe('saturationLabel', () => {
  it('boyutsuz kontrol', () => {
    expect(saturationLabel(row({ instance: 'corebank-dg.prod', check: 'sessions' })))
      .toBe('oracle · corebank-dg.prod · sessions');
  });
  it('boyutlu kontrol subkey taşır (tablespace adı)', () => {
    expect(saturationLabel(row({ instance: 'db1', check: 'tablespace', subkey: 'USERS' })))
      .toBe('oracle · db1 · tablespace USERS');
  });
});

describe('saturationTone', () => {
  // Eşikler evaluator'ın capacityDecision'ıyla AYNI olmalı: sayfa ile
  // alarm aynı sayıya bakıp farklı renk gösteremez.
  it('crit >= 90', () => {
    expect(saturationTone(90)).toBe('err');
    expect(saturationTone(99.9)).toBe('err');
  });
  it('warn >= 85', () => {
    expect(saturationTone(85)).toBe('warn');
    expect(saturationTone(89.9)).toBe('warn');
  });
  it('altı sağlıklı', () => {
    expect(saturationTone(84.9)).toBe('ok');
    expect(saturationTone(0)).toBe('ok');
  });
});
