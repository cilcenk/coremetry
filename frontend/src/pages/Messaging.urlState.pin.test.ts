// Messaging.urlState.pin.test.ts — v0.10.574: sayfa URL durumunu TEK
// useSearchParams örneğinden okur/yazar. İkinci bir örnek hata değil ama
// bir sapma davetiyesi: aynı durumu iki ad taşırsa, fonksiyonel biçimden
// sapan bir düzenleme sessizce ezer (v0.8.253 sınıfının komşusu).
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const src = readFileSync(resolve(__dirname, 'Messaging.tsx'), 'utf8');

describe('Messaging URL durumu', () => {
  it('tek useSearchParams örneği', () => {
    expect(src.match(/useSearchParams\(\)/g)?.length ?? 0).toBe(1);
  });
  it('yazıcılar fonksiyonel biçim + replace:true', () => {
    // Yabancı parametreleri korumanın tek güvenli yolu prev'den türetmek.
    expect(src).toContain('setParams(prev => {');
    expect(src.match(/\{ replace: true \}/g)?.length ?? 0).toBeGreaterThanOrEqual(2);
  });
});
