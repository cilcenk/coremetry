// Regression tests for placeTooltip — the chart hover-panel placer.
//
// Original symptom (operator-reported): on the metric line charts the value
// hover tooltip sometimes rendered directly UNDER the mouse pointer, hiding the
// value. Two root causes: (1) v0.7.11 the flip+clamp could pull the panel over
// the cursor; (2) the cursor was given in `.u-over` coordinates while the panel
// was clamped/positioned in the OUTER container basis — near the y-axis the
// panel landed ~axis-width px left of the real pointer, so its right edge fell
// under the cursor.
//
// The invariant we assert: in CONTAINER coordinates the rendered panel rect
// must not contain the cursor point — UNLESS the panel is genuinely larger than
// the plot box on BOTH axes (no room exists; documented unavoidable case).

import { describe, expect, it } from 'vitest';
import { placeTooltip } from './chartTooltip';

const PAD = 12;

// A typical chart: 600px-wide container, 320px tall. The left y-axis eats 56px
// and the bottom x-axis 30px, so the plotting (.u-over) box is inset.
const OW = 544;  // over width  (600 - 56 axis - 0 right pad)
const OH = 290;  // over height (320 - 30 axis)
const OX = 56;   // over left offset inside container (y-axis width)
const OY = 0;    // over top offset
const CW = 600;  // container width
const CH = 320;  // container height

// Cursor is over-relative; the rendered cursor point in container coords.
function containerCursor(cursorX: number, cursorY: number) {
  return { px: cursorX + OX, py: cursorY + OY };
}

// True iff the cursor point lies strictly inside the panel rect (= the bug).
function overlaps(
  res: { x: number; y: number }, tw: number, th: number,
  cursorX: number, cursorY: number,
): boolean {
  const { px, py } = containerCursor(cursorX, cursorY);
  return px > res.x && px < res.x + tw && py > res.y && py < res.y + th;
}

describe('placeTooltip — never under the cursor', () => {
  // A normal-sized tooltip (fits beside the cursor on at least one axis).
  const TW = 180, TH = 90;

  // The four plot corners + centre, in over-relative cursor coords.
  const corners: Array<[string, number, number]> = [
    ['top-left',     0,        0],
    ['top-right',    OW,       0],
    ['bottom-left',  0,        OH],
    ['bottom-right', OW,       OH],
    ['centre',       OW / 2,   OH / 2],
    // Just inside the y-axis edge — the historical failure zone where the
    // over→container offset made the panel slide under the pointer.
    ['near-y-axis',  2,        OH / 2],
  ];

  for (const [name, cursorX, cursorY] of corners) {
    it(`${name}: panel does not cover the cursor`, () => {
      const res = placeTooltip(cursorX, cursorY, TW, TH, OW, OH, OX, OY, CW, CH, PAD);
      expect(overlaps(res, TW, TH, cursorX, cursorY)).toBe(false);
    });

    it(`${name}: panel stays within the container`, () => {
      const res = placeTooltip(cursorX, cursorY, TW, TH, OW, OH, OX, OY, CW, CH, PAD);
      expect(res.x).toBeGreaterThanOrEqual(0);
      expect(res.y).toBeGreaterThanOrEqual(0);
      expect(res.x + TW).toBeLessThanOrEqual(CW);
      expect(res.y + TH).toBeLessThanOrEqual(CH);
    });
  }

  it('near the y-axis the panel is offset to the RIGHT of the cursor (after it)', () => {
    // cursorX=2 over-relative → container x = 58. With the old container-only
    // basis the panel landed at x≈14 (2+pad) and its right edge (194) covered
    // the cursor at 58. Now it sits after the container cursor: 58+pad=70.
    const res = placeTooltip(2, OH / 2, TW, TH, OW, OH, OX, OY, CW, CH, PAD);
    expect(res.x).toBe(2 + OX + PAD);
  });
});

describe('placeTooltip — wide tooltip on a narrow panel', () => {
  // A multi-series tooltip wider than the plot box but still narrower than the
  // container: x can't sit beside the cursor inside the plot, but y can — so
  // the pointer must still be off the panel (cleared on the vertical axis) and
  // the panel stays on-canvas.
  const NCW = 280, NCH = 280;
  const NOW = 224, NOH = 250, NOX = 56, NOY = 0;
  const TW = 240, TH = 90;

  const spots: Array<[string, number, number]> = [
    ['top',    NOW / 2, 0],
    ['middle', NOW / 2, NOH / 2],
    ['bottom', NOW / 2, NOH],
  ];

  for (const [name, cx, cy] of spots) {
    it(`${name}: y-axis clears even though x can't`, () => {
      const res = placeTooltip(cx, cy, TW, TH, NOW, NOH, NOX, NOY, NCW, NCH, PAD);
      const py = cy + NOY;
      // Pointer is above or below the panel — not inside it vertically.
      const clearVertically = py <= res.y || py >= res.y + TH;
      expect(clearVertically).toBe(true);
      // And the panel is fully on-canvas.
      expect(res.x).toBeGreaterThanOrEqual(0);
      expect(res.x + TW).toBeLessThanOrEqual(NCW);
      expect(res.y).toBeGreaterThanOrEqual(0);
      expect(res.y + TH).toBeLessThanOrEqual(NCH);
    });
  }
});

describe('placeTooltip — degenerate panel (smaller than tooltip both axes)', () => {
  // Documented unavoidable case: the panel is bigger than the plot box on both
  // axes, so SOME overlap is forced. We only assert the result stays on-canvas
  // (no off-screen render) and the vertical fallback picks a side.
  it('clamps on-canvas without throwing', () => {
    const cw = 160, ch = 120, ow = 120, oh = 100, ox = 40, oy = 0;
    const tw = 220, th = 140;
    const res = placeTooltip(ow / 2, oh / 2, tw, th, ow, oh, ox, oy, cw, ch, PAD);
    expect(res.x).toBeGreaterThanOrEqual(0);
    expect(res.y).toBeGreaterThanOrEqual(0);
    // Clamped to max-size (which may be negative when tooltip > container; the
    // clamp floors at 0 so it never renders off the left/top edge).
    expect(res.x).toBeLessThanOrEqual(Math.max(0, cw - tw));
  });
});
