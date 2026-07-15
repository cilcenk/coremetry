import { describe, it, expect } from 'vitest';
import { fitViewport, readableFit, zoomAt, zoomRange, READABLE_MIN_K, type Viewport } from './topoViewport';

// v0.8.296 (operator-reported: "çok fazla servis olduğunda ekrana sığmıyor") —
// TopologyFlowGraph gains zoom/pan. The transform math lives in this pure
// seam so the invariants are pinned without a DOM:
//   - fitViewport scales content INTO the view (never above 1) and centers it;
//   - zoomAt keeps the content point under the cursor stationary — the
//     defining property of zoom-to-cursor;
//   - zoomRange never allows zooming below the fit scale (content can't be
//     lost off-screen smaller than "everything visible") and caps zoom-in.

describe('fitViewport', () => {
  it('small content in a big view: k=1, centered', () => {
    const vp = fitViewport(400, 300, 900, 600, 24);
    expect(vp.k).toBe(1);
    expect(vp.x).toBeCloseTo((900 - 400) / 2);
    expect(vp.y).toBeCloseTo((600 - 300) / 2);
  });

  it('content taller than the view scales down to fit with padding', () => {
    const vp = fitViewport(900, 2400, 900, 600, 24);
    expect(vp.k).toBeCloseTo((600 - 48) / 2400);
    // horizontally centered at that scale
    expect(vp.x).toBeCloseTo((900 - 900 * vp.k) / 2);
    // vertically the padded content fills the view
    expect(vp.y).toBeCloseTo(24);
  });

  it('degenerate content dimensions do not produce NaN/Infinity', () => {
    const vp = fitViewport(0, 0, 900, 600, 24);
    expect(Number.isFinite(vp.k)).toBe(true);
    expect(vp.k).toBeGreaterThan(0);
    expect(Number.isFinite(vp.x)).toBe(true);
    expect(Number.isFinite(vp.y)).toBe(true);
  });
});

// v0.8.544 (operator-reported: "fazla service olduğunda sanki hepsi
// kümelenmiş gibi oluyor … topology ekrana sığdırmak zorunda değilsin,
// zaten zoom in out özelliği var") — the OPENING transform gains a floor.
// fitViewport shrank without limit, so a busy neighbourhood opened at k≈0.4:
// the 12px pill name went sub-8px and the 12px the layout leaves between
// rows collapsed to ~4px. Widening the row spacing cannot fix that — a
// taller canvas only makes the fit scale down further.
describe('readableFit', () => {
  it('defers to fitViewport whenever the content already fits', () => {
    // Small graphs must keep the exact pre-v0.8.544 placement.
    expect(readableFit(400, 300, 900, 600, 24)).toEqual(fitViewport(400, 300, 900, 600, 24));
  });

  it('defers whenever the fit scale is still at or above the floor', () => {
    // Content needing k=0.8: above the floor, so no clamp.
    const vp = readableFit(900, (600 - 48) / 0.8, 900, 600, 24);
    expect(vp.k).toBeCloseTo(0.8);
    expect(vp).toEqual(fitViewport(900, (600 - 48) / 0.8, 900, 600, 24));
  });

  it('clamps to the floor and lets the content overflow instead of shrinking', () => {
    // The reported shape: one very tall column.
    const tall = 2400;
    expect(fitViewport(900, tall, 900, 600, 24).k).toBeLessThan(READABLE_MIN_K); // precondition
    const vp = readableFit(900, tall, 900, 600, 24);
    expect(vp.k).toBe(READABLE_MIN_K);
    expect(tall * vp.k).toBeGreaterThan(600); // overflows — that is the point
  });

  it('anchors the overflowing axis to the pad, never negative', () => {
    // Centring a box taller than the view would put y negative and open the
    // graph scrolled into its own middle — roots (first column) off-screen.
    const vp = readableFit(2000, 2400, 900, 600, 24);
    expect(vp.y).toBe(24);
    expect(vp.x).toBe(24);
  });

  it('keeps centring the axis that still has room at the floor', () => {
    // Narrow but very tall: x stays centred, only y anchors.
    const vp = readableFit(200, 2400, 900, 600, 24);
    expect(vp.x).toBeCloseTo((900 - 200 * READABLE_MIN_K) / 2);
    expect(vp.y).toBe(24);
  });

  it('degenerate content dimensions stay finite', () => {
    const vp = readableFit(0, 0, 900, 600, 24);
    expect(Number.isFinite(vp.k)).toBe(true);
    expect(vp.k).toBeGreaterThan(0);
    expect(Number.isFinite(vp.x)).toBe(true);
    expect(Number.isFinite(vp.y)).toBe(true);
  });

  it('leaves zoom-out to the true fit reachable — the floor is only the OPENING scale', () => {
    // ⛶ and wheel-out still ride fitViewport, so "show everything" survives.
    const trueFit = fitViewport(900, 2400, 900, 600, 24);
    expect(zoomRange(trueFit.k).kMin).toBeLessThan(READABLE_MIN_K);
  });
});

describe('zoomAt', () => {
  const start: Viewport = { k: 1, x: 0, y: 0 };

  it('keeps the content point under the cursor stationary', () => {
    const cursor = { x: 300, y: 200 };
    const vp = zoomAt(start, cursor.x, cursor.y, 1.5, 0.2, 2.5);
    // content point that was under the cursor: (cx - x)/k
    const before = { cx: (cursor.x - start.x) / start.k, cy: (cursor.y - start.y) / start.k };
    // ...must map back to the same screen position after the zoom
    expect(before.cx * vp.k + vp.x).toBeCloseTo(cursor.x);
    expect(before.cy * vp.k + vp.y).toBeCloseTo(cursor.y);
    expect(vp.k).toBeCloseTo(1.5);
  });

  it('clamps to kMax and still anchors the cursor', () => {
    const vp = zoomAt(start, 100, 100, 10, 0.2, 2.5);
    expect(vp.k).toBe(2.5);
    expect((100 - vp.x) / vp.k).toBeCloseTo((100 - start.x) / start.k);
  });

  it('clamps to kMin on zoom-out', () => {
    const vp = zoomAt(start, 0, 0, 0.01, 0.4, 2.5);
    expect(vp.k).toBe(0.4);
  });

  it('factor 1 is an identity', () => {
    const vp = zoomAt({ k: 0.8, x: 40, y: -20 }, 250, 250, 1, 0.2, 2.5);
    expect(vp).toEqual({ k: 0.8, x: 40, y: -20 });
  });
});

describe('zoomRange', () => {
  it('kMin is the fit scale when content overflows (never zoom below fit)', () => {
    const { kMin, kMax } = zoomRange(0.35);
    expect(kMin).toBeCloseTo(0.35);
    expect(kMax).toBe(2.5);
  });

  it('kMin never exceeds 1 even when content fits at 1:1', () => {
    const { kMin } = zoomRange(1);
    expect(kMin).toBe(1);
  });
});
