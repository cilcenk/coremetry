import { describe, it, expect } from 'vitest';
import { groupResourceAttrs } from './spanAttrGroups';

// v0.10.681 — kiosk span paneli, Tempo'nun "Resource attributes" düzenini
// verir: Service / Deployment / Container / Kubernetes / Host / Cloud /
// Process / Telemetry, kalan Custom. Sabit grup sırası, yalnız dolu gruplar,
// grup içi giriş sırası korunur (groupSpanAttrs ile aynı sözleşme).
describe('groupResourceAttrs (v0.10.681)', () => {
  it('semconv ön eklerine göre gruplar; sıra sabit, boş grup yok', () => {
    const g = groupResourceAttrs({
      'k8s.pod.name': 'shop-1', 'service.name': 'shop', 'container.image.tag': 'r1',
      'service.version': '1.2', 'deployment.environment.name': 'prod-eu', 'team': 'core',
    });
    expect(g.map(x => x.key)).toEqual(['service', 'deployment', 'container', 'k8s', 'custom']);
    expect(g[0].entries).toEqual([['service.name', 'shop'], ['service.version', '1.2']]);
    expect(g[0].label).toBe('Service');
    expect(g[3].label).toBe('Kubernetes');
    expect(g[4].entries).toEqual([['team', 'core']]);
  });
  it('boş/null girdi boş liste', () => {
    expect(groupResourceAttrs(null)).toEqual([]);
    expect(groupResourceAttrs({})).toEqual([]);
  });
});
