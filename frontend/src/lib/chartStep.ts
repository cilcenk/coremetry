// lib/chartStep.ts — GRAN-A (v0.8.245): Grafana-style width-aware step.
//
// Until now the frontend sent step=0 (auto) and the backend picked a bucket
// off its ~120-point ladder (1h → 30s), so a 4K monitor and a laptop half-pane
// got the same coarse resolution. This module lets each chart ASK for a step
// matched to the pixels it actually has (~2px per point, Grafana's rule of
// thumb). The frontend may request aggressively fine steps: the backend's
// min-step clamp (v0.8.243) guarantees the effective step never undercuts the
// metric's export interval, so a 1s request against a 15s-export metric
// degrades safely server-side.
//
// PURE — no DOM, no Date. The DOM half lives in lib/useContentWidth.ts.

// Grafana-ish step ladder (seconds): 1s…2h plus 4h/12h/1d for wide windows.
// Steps snap UP to a rung so buckets stay human-readable (5s, not 5.14s).
export const STEP_RUNGS = [
  1, 2, 5, 10, 15, 30, 60, 120, 300, 600, 900, 1800,
  3600, 7200, 14400, 43200, 86400,
];

const LAST_RUNG = STEP_RUNGS[STEP_RUNGS.length - 1]; // 86400 (1d)

// stepForWidth — the step (seconds) a chart of `widthPx` should request for a
// `rangeSec` window. targetPoints ≈ one point per 2px, clamped to [120, 720]:
// never coarser than the backend's old ~120-point auto ladder, never more
// points than a wide monitor can distinguish. The raw step rounds UP to the
// first rung that covers it (rung >= raw — a smaller rung would overshoot the
// point budget); beyond the 1d rung it rounds up to a whole multiple of 1d.
export function stepForWidth(rangeSec: number, widthPx: number): number {
  if (!Number.isFinite(rangeSec) || rangeSec <= 0) return STEP_RUNGS[0];
  const targetPoints = Math.min(720, Math.max(120, Math.floor(widthPx / 2)));
  const raw = rangeSec / targetPoints;
  for (const rung of STEP_RUNGS) {
    if (rung >= raw) return rung;
  }
  return Math.ceil(raw / LAST_RUNG) * LAST_RUNG;
}

// quantizeWidth — round a live pixel width into 200px buckets, clamped to
// [400, 2400]. The bucket (not the raw width) feeds stepForWidth, so a drag-
// resize only changes the requested step — and thus the react-query cache key
// — when the width crosses a bucket boundary, instead of refetching per
// ResizeObserver tick.
export function quantizeWidth(px: number): number {
  if (!Number.isFinite(px)) return 1200; // same fallback as useContentWidth
  return Math.min(2400, Math.max(400, Math.round(px / 200) * 200));
}
