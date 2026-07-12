// chartBuildSignature (v0.8.520) — the pure seam behind MultiLineChart's
// "rebuild vs setData" decision.
//
// Returns a STABLE string of every input that, when it changes, forces a full
// uPlot re-create: series structure (count/labels/order), axis unit, height,
// log scale, cursor-sync key, drag-zoom / bucket-click PRESENCE, compare
// alignment, and the deploy/threshold overlays. Two calls that differ ONLY in
// series data-point values (the 30s poll refresh) produce an IDENTICAL
// signature, so MultiLineChart rides the `u.setData()` fast-path instead of
// destroy()+new uPlot() — no canvas flicker, no lost cursor/zoom/isolation.
//
// Deliberately NOT in the signature (each handled without a rebuild):
//   • series data points + x values → the setData() fast-path itself.
//   • selectedOps  → applied live via setSeries in its own effect.
//   • colorOf      → a function; compared by identity in the effect deps.
//   • theme        → useThemeTick counter; a separate dep so a toggle
//                    re-resolves the CSS-var colors (theme change MUST rebuild).
//
// Keeping this pure + exported lets a vitest table assert the exact contract:
// data-only change → same signature (fast-path); any structural/option change
// → different signature (rebuild). See chartBuildSig.test.ts.

export interface ChartSigDeploy {
  timeUnixNs: number;
  label: string;
  description?: string;
}

export interface ChartSigThreshold {
  value: number;
  label?: string;
  severity?: 'warn' | 'err';
}

export interface ChartBuildSigInput {
  // Combined effective + compare series labels, in render order. Captures
  // series COUNT, NAMES, and ORDER in one field — the whole reason a poll's
  // fresh-but-same-shape data doesn't rebuild.
  labels: string[];
  unit?: string;
  height: number;
  syncKey?: string;
  logScale?: boolean;
  // Presence, not identity: !!onZoom / !!onBucketClick. The live callbacks are
  // read through refs, so a fresh arrow each render must NOT churn a rebuild —
  // but toggling the affordance on/off flips cursor.drag / the click listener,
  // which genuinely needs one.
  hasZoom: boolean;
  hasBucketClick: boolean;
  compareOffsetNs?: number;
  compareLabel?: string;
  deploys?: ChartSigDeploy[];
  thresholds?: ChartSigThreshold[];
}

export function chartBuildSignature(p: ChartBuildSigInput): string {
  return JSON.stringify([
    p.labels,
    p.unit ?? '',
    p.height,
    p.syncKey ?? '',
    !!p.logScale,
    !!p.hasZoom,
    !!p.hasBucketClick,
    p.compareOffsetNs ?? 0,
    p.compareLabel ?? '',
    // Digest overlays by VALUE (not object identity) so a caller passing a
    // fresh array of identical markers each render doesn't force a rebuild.
    (p.deploys ?? []).map(d => [d.timeUnixNs, d.label, d.description ?? '']),
    (p.thresholds ?? []).map(t => [t.value, t.label ?? '', t.severity ?? 'warn']),
  ]);
}
