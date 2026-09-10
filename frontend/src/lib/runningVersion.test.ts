import { describe, it, expect } from 'vitest';
import { runningVersion } from './runningVersion';

// v0.10.590 — öncelik ve yer tutucu kuralı effectiveVersionExpr ile aynı.
describe('runningVersion', () => {
  it('image tag service.version\'ın ÖNÜNDE (tag değişimi = rollout)', () => {
    expect(runningVersion({ 'service.version': '1.0.0', 'container.image.tag': 'release.20260817.1' })).toBe('release.20260817.1');
  });
  it('k8s.container.image.tag ikinci sırada', () => {
    expect(runningVersion({ 'k8s.container.image.tag': 'release.20260817.2', 'service.version': '1.0.0' })).toBe('release.20260817.2');
  });
  it('yer tutucu tag atlanır, service.version\'a düşer', () => {
    expect(runningVersion({ 'container.image.tag': 'latest', 'service.version': '2.3.4' })).toBe('2.3.4');
    expect(runningVersion({ 'container.image.tag': '1.0.0-SNAPSHOT', 'service.version': '2.3.4' })).toBe('2.3.4');
  });
  it('hiçbiri yoksa boş — uydurma yok', () => {
    expect(runningVersion({ 'service.version': '${project.version}' })).toBe('');
    expect(runningVersion(undefined)).toBe('');
  });
});
