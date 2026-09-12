// @vitest-environment jsdom
// trendSpark.test.tsx — v0.10.697: tek geniş trend grafiği. Çubuk sayısı =
// kova sayısı (bütçe altında), hata payı yalnız hata olan kovada, p99 çizgisi
// tüm noktaları taşır, son değer noktası var; veri yoksa "—".
import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act } from 'react';
import { TrendSpark } from './TrendSpark';

let host: HTMLDivElement; let root: Root;
beforeEach(() => { host = document.createElement('div'); document.body.appendChild(host); root = createRoot(host); });
afterEach(() => { act(() => root.unmount()); host.remove(); });

describe('TrendSpark (v0.10.697)', () => {
  it('çubuk + hata payı + p99 çizgisi + son nokta', () => {
    act(() => { root.render(<TrendSpark calls={[10, 20, 30, 40]} errors={[0, 5, 0, 0]} p99={[10, 20, 15, 30]} />); });
    expect(host.querySelectorAll('rect.ts-bar').length).toBe(4);
    expect(host.querySelectorAll('rect.ts-err').length).toBe(1);
    const line = host.querySelector('polyline.ts-p99');
    expect(line).not.toBeNull();
    expect(line!.getAttribute('points')!.split(' ').length).toBe(4);
    expect(host.querySelector('circle.ts-cur')).not.toBeNull();
  });
  it('veri yoksa "—"; hepsi sıfırsa çubuk yok ama grafik var', () => {
    act(() => { root.render(<TrendSpark calls={[]} />); });
    expect(host.textContent).toBe('—');
    act(() => { root.render(<TrendSpark calls={[0, 0, 0]} />); });
    expect(host.querySelector('svg')).not.toBeNull();
    expect(host.querySelectorAll('rect.ts-bar').length).toBe(0);
  });
  it('genişlik bütçesi: 120 kova 160 px e sığar (≤53 çubuk)', () => {
    const calls = Array.from({ length: 120 }, (_, i) => i + 1);
    act(() => { root.render(<TrendSpark calls={calls} width={160} />); });
    expect(host.querySelectorAll('rect.ts-bar').length).toBeLessThanOrEqual(53);
    expect(host.querySelectorAll('rect.ts-bar').length).toBeGreaterThan(30);
  });
});
