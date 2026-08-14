import { describe, it, expect } from 'vitest';
import { edgeArrow } from './topoArrow';

// v0.9.1032 — topoloji kenar oku matematiği. Kenar şekli
// TopologyFlowGraph ile sözleşmeli (M a C (mx,a.y) (mx,b.y) b);
// bu testler o sözleşmenin nokta+tanjant yarısını çiviliyor.

describe('edgeArrow (v0.9.1032)', () => {
  it('yatay kenar: ok akış yönünde (sağa, 0°) ve eğri üzerinde', () => {
    const r = edgeArrow({ x: 0, y: 0 }, { x: 100, y: 0 }, 0.78);
    expect(r.angle).toBeCloseTo(0, 5);
    expect(r.y).toBeCloseTo(0, 5);
    expect(r.x).toBeGreaterThan(50);
    expect(r.x).toBeLessThan(100);
  });

  it('sola akan kenar: 180°', () => {
    const r = edgeArrow({ x: 100, y: 0 }, { x: 0, y: 0 }, 0.78);
    expect(Math.abs(r.angle)).toBeCloseTo(180, 5);
  });

  it('aynı kolon (dikey S): tanjant dikey, işaret dy yönünde', () => {
    const down = edgeArrow({ x: 50, y: 0 }, { x: 50, y: 100 }, 0.5);
    expect(down.angle).toBeCloseTo(90, 5);
    const up = edgeArrow({ x: 50, y: 100 }, { x: 50, y: 0 }, 0.5);
    expect(up.angle).toBeCloseTo(-90, 5);
  });

  it('reverse: aynı nokta, 180° dönük açı (çift yönlü kenarın kaynak oku)', () => {
    const f = edgeArrow({ x: 0, y: 0 }, { x: 100, y: 40 }, 0.22);
    const r = edgeArrow({ x: 0, y: 0 }, { x: 100, y: 40 }, 0.22, true);
    expect(r.x).toBeCloseTo(f.x, 9);
    expect(r.y).toBeCloseTo(f.y, 9);
    expect(((r.angle - f.angle) % 360 + 360) % 360).toBeCloseTo(180, 5);
  });

  it('uçlarda tanjant yatay (kontrol noktaları mx üstünde) — pil önü oku yana bakmaz', () => {
    // t→1 ucunda türev (b − c2) = (b.x − mx, 0): açı 0 (b sağdaysa).
    const r = edgeArrow({ x: 0, y: 0 }, { x: 100, y: 80 }, 0.999);
    expect(Math.abs(r.angle)).toBeLessThan(1);
  });

  it('dejenere a≈b: patlamaz, +x düşüşü', () => {
    const r = edgeArrow({ x: 5, y: 5 }, { x: 5, y: 5 }, 0.78);
    expect(Number.isFinite(r.angle)).toBe(true);
    expect(r.x).toBeCloseTo(5, 9);
    expect(r.y).toBeCloseTo(5, 9);
  });
});
