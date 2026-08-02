import { describe, it, expect } from 'vitest';
import { podLineLabel, podClusterOf, withClusterPrefix } from './runtimePodLabel';

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

// v0.9.532 — cluster öneki sözleşmesi (operatör: "jvm var ama hangi
// cluster belli değil"). Önek YALNIZ çok-cluster'lı seri kümesinde ve
// yalnız cluster'ı bilinen seride basılır — tek cluster'da her etikete
// "ocpma · " eklemek gürültü, cluster'sız seriye önek basmak yalan.
describe('withClusterPrefix / podClusterOf', () => {
  it('çok cluster + cluster bilinen → önek', () => {
    expect(withClusterPrefix('6f9c7b-x2v', 'ocpma', true)).toBe('ocpma · 6f9c7b-x2v');
  });
  it('tek cluster → önek YOK (gürültü)', () => {
    expect(withClusterPrefix('6f9c7b-x2v', 'ocpma', false)).toBe('6f9c7b-x2v');
  });
  it('cluster bilinmiyor → önek YOK (attr basmayan ~%1-2 pod)', () => {
    expect(withClusterPrefix('6f9c7b-x2v', '', true)).toBe('6f9c7b-x2v');
  });
  it('podClusterOf groupKey[3] okur, kırpar, yoksa boş', () => {
    expect(podClusterOf(['p', 'h', 'i', ' ocpma '])).toBe('ocpma');
    expect(podClusterOf(['p', 'h', 'i'])).toBe('');
    expect(podClusterOf([])).toBe('');
  });
  it('4. anahtar eklenmesi podLineLabel sözleşmesini BOZMAZ', () => {
    // groupKey[0..2] indeksleri v0.9.91 kontratı; [3] cluster.
    expect(podLineLabel(['auth-service-6f9c7b-x2v', '', 'uuid', 'ocpma'], 'auth-service', 'used'))
      .toBe('6f9c7b-x2v');
  });
});
