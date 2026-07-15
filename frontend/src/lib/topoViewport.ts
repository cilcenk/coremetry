// topoViewport — pure zoom/pan math for TopologyFlowGraph (v0.8.296,
// operator-reported: "çok fazla servis olduğunda ekrana sığmıyor").
// The component applies `translate(x, y) scale(k)` (origin 0 0) to a wrapper
// holding the SVG edge layer + pill nodes; every transform decision funnels
// through these helpers so the invariants are vitest-pinned (topoViewport.test.ts).

export interface Viewport {
  k: number; // scale
  x: number; // translate px (screen space)
  y: number;
}

export const ZOOM_MAX = 2.5;

// Floor for the OPENING transform (v0.8.544). Below roughly this scale the
// 12px service name in a pill stops being readable and the 12px gap the
// layout leaves between rows collapses to ~4px, so a busy graph opens as an
// unreadable clump — operator-reported: "fazla service olduğunda sanki
// hepsi kümelenmiş gibi oluyor … topology ekrana sığdırmak zorunda değilsin,
// zaten zoom in out özelliği var."
//
// Note the layout ALREADY spaces rows honestly (TopologyFlowGraph grows its
// canvas to maxCol * 46px). Widening that spacing cannot fix the clump —
// a taller canvas just makes the auto-fit scale down further and the
// on-screen density lands in the same place. The only real lever is to stop
// fitting.
export const READABLE_MIN_K = 0.7;

// fitViewport — the "show everything" transform: scale the content box into
// the view (never above 1:1) with `pad` breathing room, centered on both
// axes. Degenerate content (0×0) resolves to k=1 so a transient empty graph
// can't poison the transform with Infinity/NaN.
export function fitViewport(contentW: number, contentH: number, viewW: number, viewH: number, pad = 24): Viewport {
  const cw = Math.max(1, contentW);
  const ch = Math.max(1, contentH);
  const k = Math.min(1, (viewW - pad * 2) / cw, (viewH - pad * 2) / ch);
  const safe = k > 0 && Number.isFinite(k) ? k : 1;
  return {
    k: safe,
    x: (viewW - cw * safe) / 2,
    y: (viewH - ch * safe) / 2,
  };
}

// readableFit — the OPENING transform (v0.8.544). fitViewport with a floor:
// a graph small enough to fit still fits exactly as before, but one that
// would have to shrink past READABLE_MIN_K opens at that floor and overflows
// instead, for the operator to pan/zoom. Overflow is the point — the ⛶
// control still calls fitViewport for a true "show everything".
//
// When the floor binds, the content is anchored to the view's top-left plus
// `pad` rather than centred: centring a box larger than the view pushes its
// origin negative, so the graph would open scrolled into its own middle with
// the first column off-screen left. Roots live in that first column.
export function readableFit(
  contentW: number, contentH: number, viewW: number, viewH: number,
  pad = 24, minK = READABLE_MIN_K,
): Viewport {
  const fit = fitViewport(contentW, contentH, viewW, viewH, pad);
  if (fit.k >= minK) return fit;
  const cw = Math.max(1, contentW);
  const ch = Math.max(1, contentH);
  return {
    k: minK,
    // Keep centring on whichever axis still has room at the floor.
    x: cw * minK <= viewW - pad * 2 ? (viewW - cw * minK) / 2 : pad,
    y: ch * minK <= viewH - pad * 2 ? (viewH - ch * minK) / 2 : pad,
  };
}

// zoomAt — multiply the scale by `factor`, clamped to [kMin, kMax], while
// keeping the content point under the cursor (cx, cy — screen/view coords)
// stationary. Standard anchor algebra: the content point (cx-x)/k must map to
// the same screen position under the new transform.
export function zoomAt(vp: Viewport, cx: number, cy: number, factor: number, kMin: number, kMax: number): Viewport {
  const k = Math.min(kMax, Math.max(kMin, vp.k * factor));
  if (k === vp.k) return vp;
  const ratio = k / vp.k;
  return {
    k,
    x: cx - (cx - vp.x) * ratio,
    y: cy - (cy - vp.y) * ratio,
  };
}

// zoomRange — kMin is the fit scale (you can never zoom out past "everything
// visible", so content can't be lost as a speck), capped at 1 so a
// fits-at-1:1 graph simply doesn't zoom out. kMax is the fixed read-the-text
// ceiling.
export function zoomRange(fitK: number): { kMin: number; kMax: number } {
  return { kMin: Math.min(1, fitK), kMax: ZOOM_MAX };
}
