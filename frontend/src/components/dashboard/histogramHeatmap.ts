import type { HistogramResult, LatencyHeatmap as HeatmapData } from '@/lib/types';

// histogramHeatmap (v0.9.109, C2) — adapts the /api/metrics/histogram response
// (HistogramResult, the machine F3 works on) into the LatencyHeatmap viz's data
// shape, so a dashboard Heatmap panel can render a histogram METRIC's latency
// density (the first dashboard surface for the metric-histogram path).
//
// Two shape mismatches to bridge:
//   1. HistogramResult.counts[t] has len = bounds.length + 1 — the last entry is
//      the +Inf overflow bucket (observations above the top explicit bound).
//      LatencyHeatmap needs one finite upper-bound per bin, so we append a
//      synthetic overflow bin above the top bound.
//   2. bounds are in the metric's native unit; the viz labels the y-axis in ms,
//      so seconds-valued bounds (http.server.request.duration) scale ×1000.

export function histogramResultToHeatmap(hr: HistogramResult, unit?: string): HeatmapData {
  const toMs = unit === 's' || unit === 'seconds' ? 1000 : 1;
  const bounds = (hr.bounds ?? []).map(b => b * toMs);

  // Synthetic upper bound for the +Inf overflow bin: one bounds-step above the
  // top bound (or 2× when a single bound). This value only positions the top
  // y-axis TICK — the viz labels the row "> {top}" via overflowTop, never
  // asserting this synthetic number as a real ceiling (v0.9.110 review fix).
  const top = bounds.length ? bounds[bounds.length - 1] : 0;
  let overflow = 1;
  if (bounds.length >= 2) {
    overflow = top + (top - bounds[bounds.length - 2]);
  } else if (bounds.length === 1) {
    overflow = top * 2 || 1;
  }
  // Defensive: keep overflow strictly above the top bound even if a
  // non-conformant SDK emitted non-increasing bounds — the log-scale tick must
  // not collapse onto the top bound.
  if (bounds.length && overflow <= top) overflow = top > 0 ? top * 1.5 : 1;
  const durationBins = bounds.length ? [...bounds, overflow] : [1];

  let maxCount = 0;
  const counts = (hr.counts ?? []).map(col => {
    const row = durationBins.map((_, j) => (col && col[j] != null ? col[j] : 0));
    for (const c of row) if (c > maxCount) maxCount = c;
    return row;
  });

  return {
    times: hr.times ?? [], durationBins, counts, maxCount,
    // The top bin is the histogram's +Inf overflow (">top explicit bound"),
    // labeled as such by LatencyHeatmap — not a finite ceiling.
    overflowTop: bounds.length > 0,
    // metric-histogram datapoints, not spans.
    countNoun: 'samples',
  };
}
