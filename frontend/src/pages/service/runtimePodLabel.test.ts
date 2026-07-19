import { describe, it, expect } from 'vitest';
import { podLineLabel } from './runtimePodLabel';

// v0.9.91 — pod-bazlı runtime çizgi etiketi kontratı (üçlü groupKey:
// k8s.pod.name > host.name > service.instance.id).
describe('podLineLabel', () => {
  it('k8s.pod.name varsa servis prefix\'i kırpılır', () => {
    expect(podLineLabel(['auth-service-6f9c7b-x2v', '', 'uuid'], 'auth-service', 'used'))
      .toBe('6f9c7b-x2v');
  });
  it('k8s.pod.name yok ama host.name (=pod adı) varsa onu kullanır', () => {
    // operatör senaryosu: metriklerde k8s.pod.name yok, host.name = pod
    expect(podLineLabel(['', 'auth-service-6f9c7b-x2v', 'abcdef1234'], 'auth-service', 'used'))
      .toBe('6f9c7b-x2v');
  });
  it('prefix eşleşmezse pod adı olduğu gibi', () => {
    expect(podLineLabel(['other-pod-1', '', ''], 'auth-service', 'used')).toBe('other-pod-1');
  });
  it('pod adı yok, yalnız instance id → ilk 8 karakter (eski davranış)', () => {
    expect(podLineLabel(['', '', 'abcdef1234567890'], 'svc', 'used')).toBe('abcdef12');
  });
  it('üçü de boş → fallback (aggregate tek çizgi)', () => {
    expect(podLineLabel(['', '', ''], 'svc', 'used')).toBe('used');
    expect(podLineLabel([], 'svc', 'gc')).toBe('gc');
  });
});
