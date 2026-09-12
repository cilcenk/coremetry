import { describe, it, expect } from 'vitest';
import { incidentRootCauseLabel, incidentRootCauseSort } from './incidentRootCause';

// v0.10.698 — incident kök neden etiketi (liste kolonu + detay çipi tek üretici).
describe('incidentRootCauseLabel', () => {
  it('alan yoksa null (FE "—" çizer, uydurma yok)', () => {
    expect(incidentRootCauseLabel(undefined)).toBeNull();
    expect(incidentRootCauseLabel(null)).toBeNull();
    expect(incidentRootCauseLabel({ topSuspect: '', topScore: 0, confidence: 0.4 })).toBeNull();
  });
  it('şüpheli + yuvarlanmış yüzde', () => {
    expect(incidentRootCauseLabel({ topSuspect: 'shop-db', topScore: 3, confidence: 0.746 }))
      .toEqual({ suspect: 'shop-db', pct: '75%' });
  });
  it('güven [0,1] dışına taşarsa kırpılır', () => {
    expect(incidentRootCauseLabel({ topSuspect: 'x', topScore: 0, confidence: 1.7 })!.pct).toBe('100%');
  });
  it('sıralama: güven, yoksa -1', () => {
    expect(incidentRootCauseSort(undefined)).toBe(-1);
    expect(incidentRootCauseSort({ topSuspect: 'x', topScore: 0, confidence: 0.3 })).toBe(0.3);
    expect(incidentRootCauseSort({ topSuspect: '', topScore: 0, confidence: 0.3 })).toBe(-1);
  });
});
