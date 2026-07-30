// v0.9.395 (Faz C-2 Ş2) — clusterAnnotations saf kümeleme pinleri.
import { describe, expect, it } from 'vitest';
import { clusterAnnotations } from './AnnotationLane';
import type { AnnotationItem } from '@/lib/types';

const it_ = (ts: number, kind: AnnotationItem['kind'] = 'deploy'): AnnotationItem =>
  ({ ts, kind, title: kind });

const F = 1_000_000_000_000_000_000; // from ns
const T = F + 3_600_000_000_000;     // +1h

describe('clusterAnnotations', () => {
  it('aynı dilime düşenler tek küme, temsilci ts = ilk olay', () => {
    // 120 dilim / 1h = 30s dilim; 5s arayla iki olay aynı dilimde.
    const c = clusterAnnotations([it_(F + 10e9), it_(F + 15e9, 'alert_fired')], F, T);
    expect(c).toHaveLength(1);
    expect(c[0].items).toHaveLength(2);
    expect(c[0].ts).toBe(F + 10e9);
  });

  it('farklı dilimler ayrı kümeler, frac 0..1 sıralı', () => {
    const c = clusterAnnotations([it_(F + 60e9), it_(F + 1_800e9)], F, T);
    expect(c).toHaveLength(2);
    expect(c[0].frac).toBeLessThan(c[1].frac);
    expect(c[1].frac).toBeGreaterThan(0.4);
    expect(c[1].frac).toBeLessThan(0.6);
  });

  it('pencere dışı olaylar atılır; boş girdi boş çıktı', () => {
    // F-1 değil F-1e9: 1e18 ölçeğinde JS float'ı ±1'i çözemez (testin
    // kendi hassasiyet tuzağı — kod değil).
    expect(clusterAnnotations([it_(F - 1e9), it_(T)], F, T)).toHaveLength(0);
    expect(clusterAnnotations([], F, T)).toHaveLength(0);
  });

  it('ters/boş pencere güvenli', () => {
    expect(clusterAnnotations([it_(F)], T, F)).toHaveLength(0);
  });
});
