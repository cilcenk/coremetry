// components/dashboard/panelChrome.ts — v0.9.778: the dashboard panel's
// *chrome* constants, pulled out of PanelRenderer so the renderer and the
// editor read ONE source. PURE (no React, no DOM) — the sibling panelStep.ts
// precedent: shared, testable, side-effect free.
//
// Two things live here:
//
//  1. THRESHOLD_COLOURS — the green/amber/red band palette. It used to be a
//     hard-coded rgb() triple duplicated in PanelRenderer (THRESHOLD_COLOURS)
//     and PanelEditor (ThresholdEditor's local PALETTE), which meant the
//     swatch in the editor and the paint on the panel could drift, and NEITHER
//     followed the theme: the same #2ea043-ish green sat on the dark, light
//     and Red Hat palettes alike. Now both read the theme tokens, so a stat
//     tile's "red" is the same red as every .b-err badge on the page.
//
//  2. PANEL_HEIGHTS — the S/M/L pixel map behind Panel.height. Two families
//     because a chart needs more vertical room than a single-number box to
//     stay readable; 'm' reproduces the pre-v0.9.778 hard-coded 220 (box) /
//     280 (chart), which is why an absent height decodes to today's look.

import type { PanelHeight } from '@/lib/types';

export type ThresholdColour = 'green' | 'amber' | 'red';

// Theme tokens, not literals — re-resolved per theme (dark / light / redhat)
// exactly like the rest of the UI. Safe in SVG presentation attributes too
// (the gauge already paints its track with stroke="var(--bg2)").
export const THRESHOLD_COLOURS: Record<ThresholdColour, string> = {
  green: 'var(--ok)',
  amber: 'var(--warn)',
  red:   'var(--err)',
};

// thresholdTint — a 12% wash of a band colour for colorMode:'background'.
// MUST be color-mix, not a hand-rolled rgba(): the old hexToRgba() parsed
// three decimal groups out of an `rgb(r,g,b)` literal and returned its input
// unchanged on a miss — feed it `var(--err)` and the panel body would paint
// FULLY OPAQUE red instead of a tint.
export function thresholdTint(colour: string, pct = 12): string {
  return `color-mix(in srgb, ${colour} ${pct}%, transparent)`;
}

// Panel body heights. 'm' == the legacy constants, so a panel saved without a
// height field renders byte-for-byte the pre-v0.9.778 layout.
export const PANEL_HEIGHTS = {
  box:   { s: 160, m: 220, l: 320 },
  chart: { s: 200, m: 280, l: 400 },
} as const;

// panelBoxHeight — stat / gauge tiles and the loading / empty / error states
// that stand in for them. undefined → 'm' (the pre-height default).
export function panelBoxHeight(h?: PanelHeight): number {
  return PANEL_HEIGHTS.box[h ?? 'm'];
}

// panelChartHeight — line / bar / heatmap panels and their placeholder
// states. Taller than the box family at every size so a chart never gets
// squeezed into a number-tile's footprint.
export function panelChartHeight(h?: PanelHeight): number {
  return PANEL_HEIGHTS.chart[h ?? 'm'];
}
