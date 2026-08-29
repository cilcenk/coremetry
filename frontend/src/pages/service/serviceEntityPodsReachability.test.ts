// v0.10.145 — entity pod tablosu her kapıdan ÖNCE mount edilir (kaynak taraması).
// v0.10.149 — operatör: tablo hem Infra hem Pods'ta çıkıyor + Pods'ta altta
// Thanos listesi tekrar → tek yer (Pods), Infra boş durumu oraya işaret eder,
// envanter entity satırı varken kapalı başlar.
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

describe('ServiceEntityPods reachability (v0.10.145 → v0.10.149)', () => {
  it('Infra tab does NOT mount the entity table (it lives in the Pods tab only) but points at it', () => {
    const src = read('ServiceInfraTab.tsx').replace(/^\s*\/\/.*$/gm, '').replace(/\{\/\*[\s\S]*?\*\/\}/g, '');
    expect(src).not.toContain('<ServiceEntityPods');
    expect(src).toMatch(/entityHint/);
    expect(src).toMatch(/tab: 'pods'/);
  });

  it('Pods tab mounts the entity table before its empty state and collapses the Thanos inventory when entity rows exist', () => {
    const src = read('ServicePodsTab.tsx').replace(/^\s*\/\/.*$/gm, '').replace(/\{\/\*[\s\S]*?\*\/\}/g, '');
    const mount = firstMount(src);
    const g = src.indexOf('title="No pods matched"');
    expect(g).toBeGreaterThan(-1);
    expect(mount).toBeLessThan(g);
    expect(src).toMatch(/onRows=\{setEntityRows\}/);
    expect(src).toMatch(/entityRows > 0 && !thanosOpen\) \? null/);
    expect(src).toMatch(/<DisclosureButton[^>]*expanded=\{thanosOpen\}/);
  });
});
