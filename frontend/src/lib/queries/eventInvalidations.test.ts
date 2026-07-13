import { describe, it, expect } from 'vitest';
import { eventInvalidations, catchupInvalidations } from './eventInvalidations';
import { keys } from './keys';

// v0.8.529 — single-SSE refactor: leader ve follower yolları AYNI
// event→key eşlemesini kullanmalı. Bu pure eşlemeyi sabitler.
describe('eventInvalidations', () => {
  it('problem olayları problems + anomaly-metrics + incidents geçersiz kılar', () => {
    for (const kind of ['problem.open', 'problem.resolve'] as const) {
      const got = eventInvalidations(kind);
      expect(got).toContainEqual(keys.problems.all);
      expect(got).toContainEqual(keys.anomalies.metrics);
      expect(got).toContainEqual(keys.incidents.all);
      expect(got).toHaveLength(3);
    }
  });
  it('anomaly olayları yalnız anomalies.all', () => {
    for (const kind of ['anomaly.open', 'anomaly.clear'] as const) {
      expect(eventInvalidations(kind)).toEqual([keys.anomalies.all]);
    }
  });
  it('bilinmeyen kind → boş (blanket refetch YOK)', () => {
    expect(eventInvalidations('garbage.kind')).toEqual([]);
    expect(eventInvalidations('')).toEqual([]);
  });
  it('catchup her handler anahtarının birleşimi', () => {
    const c = catchupInvalidations();
    expect(c).toContainEqual(keys.problems.all);
    expect(c).toContainEqual(keys.anomalies.all);
    expect(c).toContainEqual(keys.anomalies.metrics);
    expect(c).toContainEqual(keys.incidents.all);
  });
});
