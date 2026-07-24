// sparkline.ts — pure geometry + threshold helpers for
// components/Sparkline.tsx (granular-sparklines sweep M4, 2026-07-24).
// Kept out of the component so the node vitest harness pins the math:
// the bar-mode bucket→rect projection and the two-level threshold
// classification that colours breach buckets. No DOM, no React.

export type SparkThresholdClass = 'ok' | 'warn' | 'err';

// classifyThreshold — two-level classification for bar-mode sparklines:
//   v ≥ threshold             → 'err'  (breach bucket, var(--err))
//   v ≥ warnRatio · threshold → 'warn' (approaching, var(--warn))
//   otherwise                 → 'ok'   (neutral faint-grey bucket)
// Missing / non-positive threshold classifies everything 'ok' so a
// caller without a budget renders uniformly neutral bars.
export function classifyThreshold(
  v: number,
  threshold?: number,
  warnRatio = 0.7,
): SparkThresholdClass {
  if (threshold === undefined || !isFinite(threshold) || threshold <= 0) return 'ok';
  if (v >= threshold) return 'err';
  if (v >= threshold * warnRatio) return 'warn';
  return 'ok';
}

export interface SparkBar {
  i: number; // bucket index (position on the time axis)
  v: number; // the bucket's value (drives hover tooltip + colouring)
  x: number;
  y: number;
  w: number;
  h: number;
}

export interface BarGeometryOpts {
  // Shared y-scale (compare overlays); when set, bars normalise to
  // [0, domainMax] instead of the series' own max.
  domainMax?: number;
  // Top inset in px so a full-height bar isn't clipped (default 1,
  // matching the line-mode stroke inset).
  pad?: number;
  // Fraction of each slot left as inter-bar gap (default 0.28 — dense
  // grids still read as discrete buckets).
  gapRatio?: number;
  // Minimum rendered height for a non-zero bucket (default 1px) so a
  // tiny-but-real value never vanishes next to a spike.
  minBarH?: number;
}

// barGeometry projects bucket values onto zero-based bar rects for a
// width×height viewBox. Bars are ALWAYS zero-based (a min-normalised
// bar chart lies about magnitude). Zero / negative / non-finite
// buckets produce NO rect — an empty slot reads as "0", which is the
// meaningful resting state for error-rate and counter distributions.
export function barGeometry(
  values: number[],
  width: number,
  height: number,
  opts: BarGeometryOpts = {},
): SparkBar[] {
  const n = values.length;
  if (n === 0 || width <= 0 || height <= 0) return [];
  const pad = opts.pad ?? 1;
  const gapRatio = opts.gapRatio ?? 0.28;
  const minBarH = opts.minBarH ?? 1;
  const own = Math.max(...values.filter(v => isFinite(v)), 0);
  const max = opts.domainMax !== undefined && opts.domainMax > 0 ? opts.domainMax : own;
  if (max <= 0) return [];
  const slot = width / n;
  const w = Math.max(1, slot * (1 - gapRatio));
  const usable = Math.max(1, height - pad);
  const out: SparkBar[] = [];
  for (let i = 0; i < n; i++) {
    const v = values[i];
    if (!isFinite(v) || v <= 0) continue;
    const h = Math.max(minBarH, (Math.min(v, max) / max) * usable);
    out.push({
      i,
      v,
      x: i * slot + (slot - w) / 2,
      y: height - h,
      w,
      h,
    });
  }
  return out;
}

// ── v0.9.207 review-fix (granular-sparklines set) ─────────────────────
// Bars mode drew one <rect> per raw 5-min bucket. Feeds without a slot
// cap (Services error-rate rides raw service_summary_5m: 24h = 288,
// 7d = 2016, 30d = 8640 buckets) made the DOM unbounded across a
// 50-row table, and the slot width (80/2016 ≈ 0.04px) fell far below
// the 1px minimum bar width, so bars painted over each other ~25:1 and
// the per-bucket threshold colouring smeared into a solid block. The
// two helpers below cap the rendered bar count at a width-derived
// budget by merging adjacent buckets BEFORE geometry.

// maxBarsForWidth — how many discrete bars fit a drawn width. Each
// slot needs minSlotPx (default 2px: ~1.4px bar + gap at the default
// gapRatio) to read as a separate bucket; below that neighbouring
// bars overlap and per-bucket colour becomes unreadable. Default
// sparkline width 80 → 40 bars, which also keeps per-row DOM small
// (≤ ~40 rects) on 50-row tables. Degenerate width answers 0.
export function maxBarsForWidth(width: number, minSlotPx = 2): number {
  if (!isFinite(width) || width <= 0 || !isFinite(minSlotPx) || minSlotPx <= 0) return 0;
  return Math.max(1, Math.floor(width / minSlotPx));
}

export type BucketReducer = 'sum' | 'max';

// downsampleBuckets — merge adjacent buckets so at most maxBars values
// remain, preserving time order. Group size k = ceil(n / maxBars);
// every output bucket covers k adjacent inputs except a possibly
// shorter remainder tail. Reducer semantics — pick per render mode:
//   'max' — rate / threshold cells (mode='bars'): a single breached
//           5-min bucket must survive the merge and still colour
//           var(--err); averaging would dilute a short spike below
//           the threshold and hide exactly what the mode exists for.
//   'sum' — counter cells (mode='count'): fires/events add up, so the
//           merged bar is the true count over the wider window.
// Non-finite inputs are ignored; an all-ignored group yields 0 (empty
// slot, consistent with barGeometry's zero-suppression). n ≤ maxBars
// returns the input untouched; maxBars ≤ 0 has no drawable slots → [].
export function downsampleBuckets(
  values: number[],
  maxBars: number,
  reducer: BucketReducer,
): number[] {
  const n = values.length;
  if (n === 0 || maxBars <= 0) return [];
  if (n <= maxBars) return values;
  const k = Math.ceil(n / maxBars);
  const out: number[] = [];
  for (let start = 0; start < n; start += k) {
    const end = Math.min(n, start + k);
    let acc = 0;
    let seen = false;
    for (let j = start; j < end; j++) {
      const v = values[j];
      if (!isFinite(v)) continue;
      if (!seen) { acc = v; seen = true; }
      else if (reducer === 'sum') acc += v;
      else if (v > acc) acc = v;
    }
    out.push(seen ? acc : 0);
  }
  return out;
}

// barIndexAt — cursor x → bucket index for bar-mode hover (bars use
// slot-centred rects, so unlike line mode the mapping is floor-based,
// not nearest-point). The exact right edge clamps into the last slot
// (mouse coords can land on rect.width); outside the box answers null.
export function barIndexAt(x: number, width: number, count: number): number | null {
  if (count <= 0 || width <= 0) return null;
  if (x < 0 || x > width) return null;
  return Math.min(count - 1, Math.floor((x / width) * count));
}
