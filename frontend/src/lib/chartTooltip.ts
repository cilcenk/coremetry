// Floating-tooltip placement for the uPlot-based charts (MultiLineChart +
// TimeSeriesPanel). Pure + DOM-free so it's unit-testable; the chart hooks pass
// in the cursor position, the tooltip size, and the plot geometry.
//
// Two bugs this owns the fix for:
//
//  1. (v0.7.11) The panel rendered UNDER the cursor. The original logic flipped
//     per-axis then `Math.max(0, …)` clamped a flipped position back to the
//     edge — on a narrow chart or a wide multi-series tooltip the clamp pulled
//     the panel over the pointer, hiding the value at the cursor.
//
//  2. (operator-reported) The panel STILL sometimes sat under the pointer. Root
//     cause: a coordinate-basis mismatch. uPlot's `cursor.left`/`cursor.top` are
//     relative to the `.u-over` plotting box, but the tooltip is positioned
//     `absolute` inside the OUTER container — whose origin is offset from
//     `.u-over` by the left-axis width and top padding. The old code clamped the
//     over-relative cursor against the container's full width and wrote the
//     result straight to `style.left/top`, so near the y-axis the panel landed
//     ~axis-width px to the LEFT of the real pointer and its right edge fell
//     under the cursor. We now take the over-element origin (`ox`,`oy`) and map
//     the cursor INTO container coordinates first, clamp against the true
//     container box, and place + flip in that single shared basis — mirroring
//     LatencyHeatmap's left/top clamp.
//
// Per axis we place the panel BESIDE the cursor: after it (`c + pad`), else
// before it (`c - pad - size`). Either of those is `clear` — the pointer is not
// on the panel along that axis. Only when the panel is larger than the
// available room on BOTH sides of an axis (fits beside on neither) do we
// centre+clamp, which can't avoid overlap on that one axis; in that case we
// force the vertical axis to whichever side has more room so at least one axis
// keeps the pointer off the panel. The returned top-left is clamped within the
// container [0, max-size] so nothing renders off-canvas.

export interface TooltipPlacement {
  /** left, in CONTAINER pixels (ready for style.left). */
  x: number;
  /** top, in CONTAINER pixels (ready for style.top). */
  y: number;
}

/**
 * @param cursorX  cursor x relative to the plot/over box (uPlot `cursor.left`)
 * @param cursorY  cursor y relative to the plot/over box (uPlot `cursor.top`)
 * @param tw       tooltip width  (offsetWidth)
 * @param th       tooltip height (offsetHeight)
 * @param ow       width  of the plot/over box (uPlot `bbox.width / dpr` ≈ over.clientWidth)
 * @param oh       height of the plot/over box
 * @param ox       left offset of the over box inside the container (over.offsetLeft)
 * @param oy       top  offset of the over box inside the container (over.offsetTop)
 * @param cw       container width  (clientWidth) — the clamp box for style.left
 * @param ch       container height (clientHeight) — the clamp box for style.top
 * @param pad      gap between cursor and panel (px)
 */
export function placeTooltip(
  cursorX: number, cursorY: number,
  tw: number, th: number,
  ow: number, oh: number,
  ox: number, oy: number,
  cw: number, ch: number,
  pad = 12,
): TooltipPlacement {
  // Map the over-relative cursor into the container basis the tooltip is
  // positioned in. From here on every number — cursor, sides, clamp — is in
  // container pixels, so the placement and the rendered position can't drift.
  const cx = cursorX + ox;
  const cy = cursorY + oy;

  // place() decides one axis. It tries to sit the panel just AFTER the cursor,
  // else just BEFORE it (both keep the pointer off the panel = `clear`). When
  // neither side has room it centres on the cursor and clamps on-canvas —
  // overlap is unavoidable on that axis, flagged `clear:false`.
  const place = (c: number, size: number, max: number): { p: number; clear: boolean } => {
    if (c + pad + size <= max) return { p: c + pad, clear: true };       // after the cursor
    if (c - pad - size >= 0)   return { p: c - pad - size, clear: true }; // before the cursor
    return { p: Math.max(0, Math.min(c - size / 2, max - size)), clear: false };
  };

  const hx = place(cx, tw, cw);
  const hy = place(cy, th, ch);

  // Neither axis clear → force vertical to a clear side so the pointer is never
  // on top of the panel. Prefer the side of the cursor (within the plot box,
  // not the container) with more room; clamp the result on-canvas.
  if (!hx.clear && !hy.clear) {
    const cursorInTopHalf = cursorY < oh / 2;
    hy.p = cursorInTopHalf
      ? Math.min(cy + pad, Math.max(0, ch - th)) // below the cursor
      : Math.max(0, cy - pad - th);              // above the cursor
  }

  return { x: hx.p, y: hy.p };
}
