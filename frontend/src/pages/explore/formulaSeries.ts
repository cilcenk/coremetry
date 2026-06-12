// pages/explore/formulaSeries.ts — the formula row's series derivation.
//
// Phase-2 (explore-v2). Per the plan's data-fetching section: for each
// referenced letter, Σ over that query's group fan-out per time bucket,
// then evalExpr across the letters' SHARED buckets (step is global, so
// buckets align by construction). Buckets missing any referenced letter —
// or producing a non-finite result (divide-by-zero) — become gaps.
//
// Pure module (no React, no fetch) — table-driven tests in
// formulaSeries.test.ts.

import { evalExpr, exprRefs } from '@/lib/metricFormula';
import type { SpanMetricSeries } from '@/lib/types';

// letterTotals — collapse a query's group fan-out into one value per bucket.
// A null/NaN point inside a group contributes 0 (a gap in one group must not
// punch a hole through the letter's total — same posture as the stacked
// transform in TimeSeriesPanel).
export function letterTotals(series: SpanMetricSeries[]): Map<number, number> {
  const out = new Map<number, number>();
  for (const s of series) {
    for (const p of s.points) {
      const v = p.value;
      if (v == null || !Number.isFinite(v)) continue;
      out.set(p.time, (out.get(p.time) ?? 0) + v);
    }
  }
  return out;
}

// formulaSeries — evaluate `expr` over the letters' per-bucket totals.
// Returns points sorted by time; empty when the expression references a
// letter with no data (the caller renders the panel's empty state).
export function formulaSeries(
  expr: string,
  byLetter: Record<string, SpanMetricSeries[] | undefined>,
): { time: number; value: number }[] {
  const refs = exprRefs(expr);
  if (refs.length === 0) return [];
  const totals: Record<string, Map<number, number>> = {};
  for (const id of refs) {
    const data = byLetter[id];
    if (!data || data.length === 0) return []; // unknown / empty ref → no series
    totals[id] = letterTotals(data);
  }
  // Shared buckets = intersection across all referenced letters.
  const [first, ...others] = refs;
  const times = [...totals[first].keys()]
    .filter(t => others.every(id => totals[id].has(t)))
    .sort((a, b) => a - b);
  const pts: { time: number; value: number }[] = [];
  for (const t of times) {
    const vars: Record<string, number> = {};
    for (const id of refs) vars[id] = totals[id].get(t)!;
    const v = evalExpr(expr, vars);
    if (v !== null) pts.push({ time: t, value: v }); // null (e.g. /0) → gap
  }
  return pts;
}
