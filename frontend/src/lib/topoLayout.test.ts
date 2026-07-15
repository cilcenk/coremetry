import { describe, it, expect } from 'vitest';
import { NODE_H, NODE_H_FOCUS, ROW_GAP, ROW_PITCH } from './topoLayout';

// v0.8.545 — operator-reported: "prod'ta onlarca servis aynı hizada olunca
// üst üste biniyor gözüküyor ve topology anlaşılmasını zor hale getiriyor".
//
// The row pitch was 46px against a pill that actually measures 50.25px
// (54.25 focused), so every column of peers overlapped by 4-8px at EVERY
// zoom level — not a zoom artefact. The old comment read the pill as
// "~34px" because .topo-node is a flex ROW and the name+sub column inside
// it was mistaken for a single line.
//
// These constants shadow globals.css and can't be read from it at layout
// time, so this file is the guard: change .topo-node's padding, border or
// font and the arithmetic below fails instead of prod overlapping again.
describe('topoLayout', () => {
  it('derives the pill height from the CSS box (body line-height 1.5)', () => {
    const nameLine = 12 * 1.5;   // .topo-name  font-size 12px
    const subLine = 9.5 * 1.5;   // .topo-sub   font-size 9.5px
    expect(NODE_H).toBeCloseTo(8 * 2 + 1 * 2 + nameLine + subLine, 2);       // padding 8px, border 1px
    expect(NODE_H_FOCUS).toBeCloseTo(9 * 2 + 2 * 2 + nameLine + subLine, 2); // .focus: padding 9px, border 2px
  });

  it('the focused pill is the tallest — it is what the pitch must clear', () => {
    expect(NODE_H_FOCUS).toBeGreaterThan(NODE_H);
  });

  // The whole bug in one assertion.
  it('row pitch clears the tallest pill with a visible gap', () => {
    expect(ROW_PITCH).toBeGreaterThan(NODE_H_FOCUS);
    expect(ROW_PITCH - NODE_H_FOCUS).toBe(ROW_GAP);
    expect(ROW_GAP).toBeGreaterThanOrEqual(8); // below this the rows read as touching
  });

  it('rejects the pre-v0.8.545 pitch — 46 overlapped both pill variants', () => {
    expect(46).toBeLessThan(NODE_H);
    expect(46).toBeLessThan(NODE_H_FOCUS);
    expect(ROW_PITCH).toBeGreaterThan(46);
  });

  // A column of N peers must be able to occupy N pitches without any two
  // pills sharing a pixel — the "dozens at one depth" shape from prod.
  it('a dense column stays collision-free at any node count', () => {
    for (const n of [2, 12, 40, 120]) {
      const centres = Array.from({ length: n }, (_, i) => (i + 1) * ROW_PITCH);
      for (let i = 1; i < centres.length; i++) {
        const gap = (centres[i] - NODE_H_FOCUS / 2) - (centres[i - 1] + NODE_H_FOCUS / 2);
        expect(gap).toBeGreaterThan(0);
      }
    }
  });
});
