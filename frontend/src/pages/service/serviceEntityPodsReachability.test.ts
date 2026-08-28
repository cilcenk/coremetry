// v0.10.145 — entity pod tablosu her kapıdan ÖNCE mount edilir (kaynak taraması).
//
// Bug: ServiceInfraTab'ın "No pods matched" / "No Thanos clusters configured"
// erken dönüşleri <ServiceEntityPods> mount'unun ÜSTÜNDEYDİ; Thanos ad-regex'i
// (`<service>-*`) eşleşmeyince — entity katmanının var olma sebebi olan
// durum — tablo hiç çizilmiyordu, API 2 pod döndürürken. Pods sekmesi ise
// tabloyu hiç mount etmiyordu. Bu test dosya sırasını piner: ilk
// `<ServiceEntityPods` her boş-durum başlığından önce gelmeli, iki sekmede de.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

const read = (f: string) => readFileSync(join(__dirname, f), 'utf8');

function firstMount(src: string): number {
  // JSX mount'u say, import satırını / const tanımını değil: `<ServiceEntityPods `.
  const i = src.indexOf('<ServiceEntityPods ');
  expect(i, '<ServiceEntityPods> mount yok').toBeGreaterThan(-1);
  return i;
}

describe('ServiceEntityPods reachability (v0.10.145)', () => {
  it('Infra tab mounts the entity table before every empty-state gate', () => {
    const src = read('ServiceInfraTab.tsx');
    const mount = firstMount(src);
    for (const gate of ['title="No pods matched"', 'title="No Thanos clusters configured"']) {
      const g = src.indexOf(gate);
      expect(g, `${gate} bulunamadı`).toBeGreaterThan(-1);
      expect(mount, `${gate} mount'tan önce dönüyor — tablo ulaşılamaz`).toBeLessThan(g);
    }
  });

  it('Pods tab mounts the entity table before its empty state', () => {
    const src = read('ServicePodsTab.tsx');
    const mount = firstMount(src);
    const g = src.indexOf('title="No pods matched"');
    expect(g).toBeGreaterThan(-1);
    expect(mount).toBeLessThan(g);
  });
});
