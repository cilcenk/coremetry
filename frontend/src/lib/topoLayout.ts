// topoLayout — pure layout geometry for TopologyFlowGraph (v0.8.545,
// operator-reported: "prod'ta onlarca servis aynı hizada olunca üst üste
// biniyor gözüküyor ve topology anlaşılmasını zor hale getiriyor").
//
// These numbers mirror .topo-node in globals.css. They cannot be read from
// CSS at layout time (the pills are positioned before paint), so the two
// have to be kept in step by hand — topoLayout.test.ts pins the arithmetic
// so a future padding/font change fails a test instead of silently
// overlapping rows again.

// The measured pill box. .topo-node is `display:flex` with a dot and a
// STACKED name+sub column beside it, so its height is two lines, not one —
// the mistake the previous `~46px per row (pill ~34px + breathing)` comment
// made by reading the flex row as single-line.
//
//   body { font: 13px/1.5 } (globals.css) → line-height 1.5 inherited
//   .topo-name  12px   × 1.5 = 18.0
//   .topo-sub    9.5px × 1.5 = 14.25
//   .topo-node  padding 8px ×2 = 16, border 1px ×2 = 2  → 50.25
//   .topo-node.focus  padding 9px ×2 = 18, border 2px ×2 = 4 → 54.25
export const NODE_H = 50.25;
export const NODE_H_FOCUS = 54.25;

// Vertical distance between row centres. Must clear the TALLEST pill (the
// focused one — every graph has one) plus a visible gap, or dozens of peers
// at one depth render on top of each other at every zoom level. At 46 the
// focused pill overflowed its own row by 8px.
//
// Widening this makes the canvas taller, which is only affordable because
// v0.8.544 stopped the viewport from auto-shrinking to fit: the extra height
// now becomes honest overflow the operator pans through, not a smaller `k`
// that lands the on-screen density right back where it started.
export const ROW_GAP = 10;
export const ROW_PITCH = NODE_H_FOCUS + ROW_GAP; // 64.25
