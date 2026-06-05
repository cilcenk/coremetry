// LTTB — Largest-Triangle-Three-Buckets downsampling (v0.8.6 Phase 0).
//
// Charts must never receive more than ~2k points/series — past that uPlot
// (and any canvas renderer) spends all its time in path construction and the
// interaction budget (<100ms) blows. LTTB picks the `threshold` points that
// best preserve the VISUAL SHAPE of the series (it keeps peaks/troughs a naive
// every-Nth-point decimation would drop), so a 50k-point series downsampled to
// 2k looks identical at chart resolution. Zoom triggers a refine round-trip for
// the visible window, so detail is never lost — only the pixels you can't see.
//
// Pure + allocation-light so it runs equally on the main thread or inside the
// transform Web Worker (see transforms.ts / transform.worker.ts).

export interface Point {
  x: number;
  y: number;
}

// lttb reduces `data` (sorted ascending by x) to at most `threshold` points,
// always keeping the first and last. Returns `data` unchanged when it's
// already at/under the threshold (or threshold < 3 — LTTB needs the two anchor
// buckets). NaN y-values are treated as 0 for area math; callers that need true
// gap handling should segment on null first (see downsampleXY).
export function lttb(data: Point[], threshold: number): Point[] {
  const n = data.length;
  if (threshold >= n || threshold < 3) return data;

  const sampled: Point[] = [];
  // Bucket size; the first and last points get their own (singleton) buckets.
  const every = (n - 2) / (threshold - 2);

  let a = 0; // index of the point selected from the previous bucket
  sampled.push(data[0]); // always keep the first point

  for (let i = 0; i < threshold - 2; i++) {
    // Average point of the NEXT bucket — the triangle's far vertex.
    let avgX = 0;
    let avgY = 0;
    let avgStart = Math.floor((i + 1) * every) + 1;
    let avgEnd = Math.floor((i + 2) * every) + 1;
    if (avgEnd >= n) avgEnd = n;
    const avgLen = avgEnd - avgStart || 1;
    for (let j = avgStart; j < avgEnd; j++) {
      avgX += data[j].x;
      avgY += safeY(data[j].y);
    }
    avgX /= avgLen;
    avgY /= avgLen;

    // This bucket's range.
    let rangeStart = Math.floor(i * every) + 1;
    const rangeEnd = Math.floor((i + 1) * every) + 1;
    const ax = data[a].x;
    const ay = safeY(data[a].y);

    let maxArea = -1;
    let maxPoint = data[rangeStart];
    let nextA = rangeStart;
    for (; rangeStart < rangeEnd; rangeStart++) {
      // Triangle area with vertices a, candidate, next-bucket-average.
      const area = Math.abs(
        (ax - avgX) * (safeY(data[rangeStart].y) - ay) -
        (ax - data[rangeStart].x) * (avgY - ay),
      );
      if (area > maxArea) {
        maxArea = area;
        maxPoint = data[rangeStart];
        nextA = rangeStart;
      }
    }
    sampled.push(maxPoint);
    a = nextA;
  }

  sampled.push(data[n - 1]); // always keep the last point
  return sampled;
}

function safeY(y: number): number {
  return Number.isFinite(y) ? y : 0;
}

// downsampleXY is the gap-aware wrapper for our chart series shape: parallel
// `xs` / `ys` arrays where `ys[i]` may be null (a gap the chart should NOT
// bridge). It LTTBs each contiguous non-null segment independently so a
// downsampled series keeps its gaps, then stitches them back with the null
// breaks preserved. Returns parallel arrays of the same shape.
export function downsampleXY(
  xs: number[],
  ys: (number | null)[],
  threshold: number,
): { xs: number[]; ys: (number | null)[] } {
  const n = xs.length;
  if (n <= threshold || threshold < 3) return { xs, ys };

  // Split into [start,end) runs of non-null y.
  const segments: Array<[number, number]> = [];
  let runStart = -1;
  for (let i = 0; i < n; i++) {
    const present = ys[i] != null && Number.isFinite(ys[i] as number);
    if (present && runStart < 0) runStart = i;
    if (!present && runStart >= 0) {
      segments.push([runStart, i]);
      runStart = -1;
    }
  }
  if (runStart >= 0) segments.push([runStart, n]);

  if (segments.length === 0) return { xs, ys };

  // Distribute the threshold budget across segments proportional to length,
  // min 3 per segment so LTTB engages; the gaps cost one null marker each.
  const total = segments.reduce((s, [a, b]) => s + (b - a), 0);
  const outX: number[] = [];
  const outY: (number | null)[] = [];
  for (let s = 0; s < segments.length; s++) {
    const [start, end] = segments[s];
    const segLen = end - start;
    const budget = Math.max(3, Math.round((segLen / total) * threshold));
    const pts: Point[] = [];
    for (let i = start; i < end; i++) pts.push({ x: xs[i], y: ys[i] as number });
    const reduced = budget < segLen ? lttb(pts, budget) : pts;
    for (const p of reduced) {
      outX.push(p.x);
      outY.push(p.y);
    }
    // Preserve the gap between segments with a single null break.
    if (s < segments.length - 1) {
      outX.push((xs[end - 1] + xs[segments[s + 1][0]]) / 2);
      outY.push(null);
    }
  }
  return { xs: outX, ys: outY };
}
