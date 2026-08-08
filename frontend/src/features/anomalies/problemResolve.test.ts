// v0.9.825 — cache-first basamağının regresyon testleri.
//
// Bu dosyanın varlık sebebi: v0.9.455 liste yanıtını çıplak diziden
// {items,total,truncated} zarfına çevirdiğinde bu tarama SESSİZCE öldü.
// Derleme geçti, tipler geçti, ekran çalışıyor göründü. Zarf vakası
// aşağıda birinci sırada ve bilerek orada.
import { describe, it, expect } from 'vitest';
import { problemRowsFrom, findProblemInCaches } from './problemResolve';
import type { Problem } from '@/lib/types';

const row = (id: string): Problem => ({
  id, ruleId: 'r-1', ruleName: 'Anomaly · P99 latency',
  severity: 'critical', service: 'payments', metric: 'p99_ms',
  value: 9.69, threshold: 1.9, status: 'resolved',
  description: '', startedAt: 1_700_000_000_000_000_000,
} as Problem);

describe('problemRowsFrom', () => {
  it('ZARFI okur — v0.9.455 regresyonunun tam vakası', () => {
    const envelope = { items: [row('p-1')], total: 1, truncated: false };
    expect(problemRowsFrom(envelope)).toHaveLength(1);
  });

  it('çıplak diziyi de okur (zarf öncesi girdiler cache’te yaşayabilir)', () => {
    expect(problemRowsFrom([row('p-1')])).toHaveLength(1);
  });

  it('satır taşımayan kardeş şekilleri atlar, patlamaz', () => {
    // Hepsi 'problems' anahtar ağacının altında yaşıyor.
    expect(problemRowsFrom({ count: 12 })).toBeNull();                    // /count
    expect(problemRowsFrom({ status: 'ok', ageSec: 4 })).toBeNull();      // evaluator sağlığı
    expect(problemRowsFrom(row('p-1'))).toBeNull();                       // tekil kayıt (byID)
    expect(problemRowsFrom(null)).toBeNull();
    expect(problemRowsFrom(undefined)).toBeNull();
    expect(problemRowsFrom({ items: 'değil' })).toBeNull();
  });
});

describe('findProblemInCaches', () => {
  it('zarflı bir listede satırı bulur', () => {
    const entries = [
      [['problems', 'count'], { count: 3 }],
      [['problems', 'list', { status: 'open' }], { items: [row('p-1'), row('p-2')], total: 2, truncated: false }],
    ] as const;
    expect(findProblemInCaches(entries, 'p-2')?.id).toBe('p-2');
  });

  it('birden çok listede gezer — operatörün tıkladığı liste FİLTRELİ olabilir', () => {
    // Gerçek senaryo: /problems?priority=P1 filtresinden tıklanan satır,
    // host'un kendi "en yeni 200" penceresinde OLMAYABİLİR.
    const entries = [
      [['problems', 'list', { status: 'open' }], { items: [row('p-1')], total: 1, truncated: false }],
      [['problems', 'list', { priority: ['P1'] }], { items: [row('gizli')], total: 1, truncated: false }],
    ] as const;
    expect(findProblemInCaches(entries, 'gizli')?.id).toBe('gizli');
  });

  it('bulamazsa undefined — çağıran bir alt basamağa (liste, sonra by-id) düşer', () => {
    const entries = [
      [['problems', 'list', {}], { items: [row('p-1')], total: 1, truncated: false }],
    ] as const;
    expect(findProblemInCaches(entries, 'yok')).toBeUndefined();
  });

  it('boş kimlik hiçbir satırla eşleşmez', () => {
    const entries = [
      [['problems', 'list', {}], { items: [row('')], total: 1, truncated: false }],
    ] as const;
    expect(findProblemInCaches(entries, '')).toBeUndefined();
  });

  it('bozuk girdiler taramayı düşürmez', () => {
    const entries = [
      [['problems', 'count'], undefined],
      [['problems', 'list', {}], { items: [null, row('p-9')] }],
    ] as const;
    expect(findProblemInCaches(entries, 'p-9')?.id).toBe('p-9');
  });
});
