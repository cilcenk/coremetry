// components/dashboard/panelStep.ts — GRAN-C (v0.8.248): width-aware step for
// dashboard panels. PURE — the sibling usePanelWidth.ts owns the DOM half.
//
// Same model as Explore's GRAN-A (v0.8.245, lib/chartStep.ts): an explicit
// operator-picked step passes through untouched; step absent/0 (auto — every
// dashboard saved before this release) resolves against the panel's own pixel
// budget instead of deferring to the backend's ~120-point ladder. The
// backend's min-step clamp (v0.8.243) floors whatever we ask at the metric's
// export interval, so requesting fine is safe.

import { quantizeWidth, stepForWidth } from '@/lib/chartStep';

// effectivePanelStep — the step (seconds) a panel fetch should send.
//   cfgStep > 0        → the operator pinned it in PanelEditor; use verbatim.
//   auto + panelPx     → width-aware rung via the shared quantize + ladder.
//   auto + panelPx null→ null: the panel div isn't measured yet — the caller
//                        defers the fetch one beat (usePanelWidth resolves in
//                        the same commit's layout pass) instead of firing a
//                        throwaway request at a guessed width.
// Old dashboards persisted without a step field decode to cfgStep=undefined
// and land in the auto branch — the backward-compat contract (the field is
// optional both here and in the saved JSON; nothing re-writes old documents).
export function effectivePanelStep(
  cfgStep: number | undefined,
  rangeSec: number,
  panelPx: number | null,
): number | null {
  if (cfgStep && cfgStep > 0) return cfgStep;
  if (panelPx == null) return null;
  return stepForWidth(rangeSec, quantizeWidth(panelPx));
}

// estimatePanelPx — approximate a panel's pixel width from the dashboard's
// #content width and the panel's grid span (PanelWidth 1..4 of a 4-col grid).
// Used by the bundle fetch (Dashboard.tsx), which builds every panel's query
// BEFORE the panel divs are measurable; grid gaps/padding are ignored on
// purpose — the 200px quantize buckets + rung snap-up absorb far more than
// the ~30px they'd correct. Per-panel fallback fetches measure the real div
// via usePanelWidth instead.
export function estimatePanelPx(contentPx: number, panelWidth: number): number {
  const span = Math.max(1, Math.min(4, panelWidth || 4));
  return contentPx * (span / 4);
}
