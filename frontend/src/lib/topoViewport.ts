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
