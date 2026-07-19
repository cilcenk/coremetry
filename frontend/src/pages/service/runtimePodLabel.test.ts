import { describe, it, expect } from 'vitest';
import { podLineLabel } from './runtimePodLabel';

// v0.9.89 — pod-bazlı runtime çizgi etiketi kontratı.
describe('podLineLabel', () => {
  it('pod adı varsa servis prefix\'i kırpılır', () => {
    expect(podLineLabel(['auth-service-6f9c7b-x2v', 'uuid'], 'auth-service', 'used'))
      .toBe('6f9c7b-x2v');
  });
  it('prefix eşleşmezse pod adı olduğu gibi', () => {
    expect(podLineLabel(['other-pod-1', ''], 'auth-service', 'used')).toBe('other-pod-1');
  });
  it('pod yok, instance id varsa ilk 8 karakter', () => {
    expect(podLineLabel(['', 'abcdef1234567890'], 'svc', 'used')).toBe('abcdef12');
  });
  it('ikisi de boş → fallback (aggregate tek çizgi)', () => {
    expect(podLineLabel(['', ''], 'svc', 'used')).toBe('used');
    expect(podLineLabel([], 'svc', 'gc')).toBe('gc');
  });
});
