// components/dashboard/topN.ts — v0.9.781: the Top-N bar panel's PURE core,
// pulled out of PanelRenderer so it can be tested without mounting React
// (panelStep.ts / panelChrome.ts precedent).
//
// ── WHY THIS FILE EXISTS AT ALL ─────────────────────────────────────────────
//
// A Top-N panel asks ONE question: "over this window, which groups rank
// highest for this aggregation?". /api/spans/metric answers a DIFFERENT
// question: "for each group, what is this aggregation per time bucket?".
//
// Turning the second into the first is only honest when the window collapses
// to a SINGLE bucket. Rank a multi-bucket response client-side and you get a
// silent lie on every ratio aggregation: summing (or averaging) two p99s is
// not the window's p99, and neither is it a legitimate ranking weight — the
// server ranks by area (Σ|value|, chstore/spanmetric.go seriesArea), which
// for a ratio agg is "sum of percentiles", a number with no meaning. Counts
// would look right and percentiles would be quietly wrong, which is the worst
// possible failure shape.
//
// So the panel asks for exactly one bucket. That is what topNStep computes.
//
// ── WHY step = WINDOW SECONDS IS NOT ENOUGH ─────────────────────────────────
//
// ClickHouse buckets with toStartOfInterval(time, INTERVAL N SECOND), which is
// EPOCH-aligned, not window-aligned. A 1h window that starts at 11:23 with
// N=3600 straddles the 11:00 and 12:00 bins — two PARTIAL buckets, verified
// against live ClickHouse (v0.9.781 ground truth: every series came back with
// npoints=2). zeroFillSpanSeries then guarantees both points exist.
//
// The fix is to keep DOUBLING N until `from` and `to` land in the same bin.
// The WHERE clause still bounds the scan to [from, to], so the single bucket
// contains EXACTLY the window's spans and its value is the exact window
// aggregate — for count, sum, avg, min, max, error_rate and every percentile
// alike. Verified live: step=172800 over a 1h window returned npoints=1 for
// count / p99 / error_rate / rate.
//
// Doubling always terminates: once N exceeds `to`, both floor to bin 0.

/** Aggregations whose per-bucket value is a COUNT-like total (additive). */
const ADDITIVE_AGGS = new Set(['count', 'errors', 'sum']);

/**
 * `rate` is count/step, so it is additive only after the step normalisation
 * applied in topNRowValue. Kept separate from ADDITIVE_AGGS because the
 * normalisation is what makes it comparable, not the raw value.
 */
const RATE_AGG = 'rate';

/** Server-side trim ceiling — chstore/spanmetric.go `spanMetricTopN = 50`. */
export const TOPN_SERVER_CAP = 50;

/**
 * clampTopNLimit — how many rows the panel may render. Never above the
 * server's own trim (asking for 60 when the wire carries 50 would render a
 * "top 60" that is really a top 50 with the tail missing).
 */
export function clampTopNLimit(limit: number | undefined): number {
  if (!limit || !isFinite(limit) || limit < 1) return 10;
  return Math.min(TOPN_SERVER_CAP, Math.floor(limit));
}

/**
 * topNStep — the bucket size, in seconds, that collapses [fromNs, toNs] into
 * ONE epoch-aligned ClickHouse bucket.
 *
 * Starts at the window length (rounded UP to the 300s grid when the window is
 * at least 5 minutes, so the MV / narrow-rollup fast paths — which require
 * step % baseSec == 0 — stay reachable instead of dropping every Top-N panel
 * onto raw `spans`) and doubles until both ends floor to the same bin.
 */
export function topNStep(fromNs: number, toNs: number): number {
  const windowSec = Math.max(1, Math.round((toNs - fromNs) / 1e9));
  let step = windowSec >= 300 ? Math.ceil(windowSec / 300) * 300 : windowSec;
  const fromSec = Math.floor(fromNs / 1e9);
  const toSec = Math.floor(toNs / 1e9);
  // Bounded by construction (step doubles past `toSec` within ~32 rounds for
  // any realistic timestamp); the counter is belt-and-braces against a NaN
  // input turning this into a hang.
  for (let i = 0; i < 64; i++) {
    if (Math.floor(fromSec / step) === Math.floor(toSec / step)) return step;
    step *= 2;
  }
  return step;
}

/** Window length in seconds — the denominator for the `rate` normalisation. */
export function topNWindowSec(fromNs: number, toNs: number): number {
  return Math.max(1, Math.round((toNs - fromNs) / 1e9));
}

/**
 * topNRowValue — reduce one series' points to the row's single number.
 *
 * The single-bucket contract (topNStep) means `points` has exactly one entry;
 * that value is already the exact window aggregate, except for `rate`, which
 * the backend computes as count/step (chstore/rollup_fastpath.go rateDiv) and
 * therefore reads `step/window` times too small when step > window.
 *
 * The multi-point branch is defensive: it cannot happen while topNStep feeds
 * the request, but if it ever did, additive aggs still reduce EXACTLY (each
 * bucket only holds in-window spans), while a ratio agg has no honest
 * reduction — so it returns null and the row renders "—" rather than a
 * confident wrong number.
 */
export function topNRowValue(
  points: { time: number; value: number }[],
  agg: string,
  stepSec: number,
  windowSec: number,
): number | null {
  if (!points || points.length === 0) return null;
  const a = (agg || '').toLowerCase();
  const scale = a === RATE_AGG && windowSec > 0 ? stepSec / windowSec : 1;
  if (points.length === 1) return points[0].value * scale;
  if (ADDITIVE_AGGS.has(a) || a === RATE_AGG) {
    let s = 0;
    for (const p of points) s += p.value;
    return s * scale;
  }
  return null;
}

/**
 * topNMoreLabel — the "+N more" footer. `totalSeries` is the PRE-trim count
 * the backend reports when it clipped a high-cardinality group-by; it is
 * omitted when no trim happened, so it defaults to what actually arrived.
 * Returns null when nothing is hidden (no footer at all, rather than a
 * "+0 more" that reads like a bug).
 */
export function topNMoreLabel(
  shown: number,
  received: number,
  totalSeries: number | undefined,
): string | null {
  const total = totalSeries ?? received;
  const hidden = total - shown;
  if (hidden <= 0) return null;
  return `+${hidden} more`;
}

/**
 * topNRowFilters — FilterExpr[] pairing each group-by key with this row's
 * value, for the "open the spans behind this bar" pivot. Exact by
 * construction: groupKey is the value tuple of the SAME key list the query
 * grouped on, so the filter selects precisely the bar's population instead of
 * a guessed free-text search.
 */
export function topNRowFilters(
  groupBy: string | undefined,
  groupKey: string[],
): { k: string; op: '='; v: string[] }[] {
  const keys = (groupBy ?? '').split(',').map(s => s.trim()).filter(Boolean);
  const out: { k: string; op: '='; v: string[] }[] = [];
  for (let i = 0; i < keys.length && i < groupKey.length; i++) {
    out.push({ k: keys[i], op: '=', v: [groupKey[i]] });
  }
  return out;
}
