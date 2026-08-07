import { describe, expect, it } from 'vitest';
import {
  PANEL_HEIGHTS, THRESHOLD_COLOURS, panelBoxHeight, panelChartHeight, thresholdTint,
} from './panelChrome';
import type { PanelHeight } from '@/lib/types';

// v0.9.778 — panel chrome contracts. Three of these are regression guards for
// bugs this release fixed or narrowly avoided:
//
//   (1) the S/M/L map's 'm' rung MUST equal the pre-release hard-coded
//       220 / 280, or every dashboard saved without a height field silently
//       re-lays-out on upgrade;
//   (2) THRESHOLD_COLOURS must be THEME TOKENS — the old rgb() literals sat
//       unchanged on dark, light and Red Hat palettes;
//   (3) the background tint must be a real color-mix expression. Its
//       predecessor, hexToRgba(), regex-parsed three decimal groups out of
//       `rgb(r,g,b)` and RETURNED ITS INPUT UNCHANGED on a miss — feeding it
//       a var() token would have painted the stat tile fully opaque instead
//       of a 12% wash.

const SIZES: PanelHeight[] = ['s', 'm', 'l'];

describe('panelBoxHeight / panelChartHeight — S/M/L resolution', () => {
  it('undefined falls back to m (the pre-v0.9.778 default)', () => {
    expect(panelBoxHeight(undefined)).toBe(panelBoxHeight('m'));
    expect(panelChartHeight(undefined)).toBe(panelChartHeight('m'));
    // Pinned literals: these ARE the constants the renderer used before the
    // height field existed. Changing them re-lays-out every saved dashboard.
    expect(panelBoxHeight(undefined)).toBe(220);
    expect(panelChartHeight(undefined)).toBe(280);
  });

  it('maps each rung to its pixel budget', () => {
    expect(panelBoxHeight('s')).toBe(160);
    expect(panelBoxHeight('m')).toBe(220);
    expect(panelBoxHeight('l')).toBe(320);
    expect(panelChartHeight('s')).toBe(200);
    expect(panelChartHeight('m')).toBe(280);
    expect(panelChartHeight('l')).toBe(400);
  });

  it('is monotonic in size and chart is always taller than box', () => {
    expect(panelBoxHeight('s')).toBeLessThan(panelBoxHeight('m'));
    expect(panelBoxHeight('m')).toBeLessThan(panelBoxHeight('l'));
    expect(panelChartHeight('s')).toBeLessThan(panelChartHeight('m'));
    expect(panelChartHeight('m')).toBeLessThan(panelChartHeight('l'));
    for (const h of SIZES) {
      // A chart squeezed into a number-tile's footprint is unreadable.
      expect(panelChartHeight(h)).toBeGreaterThan(panelBoxHeight(h));
    }
  });

  it('covers every PanelHeight rung — no undefined pixel value', () => {
    for (const h of SIZES) {
      expect(Number.isFinite(panelBoxHeight(h))).toBe(true);
      expect(Number.isFinite(panelChartHeight(h))).toBe(true);
    }
    expect(Object.keys(PANEL_HEIGHTS.box).sort()).toEqual(['l', 'm', 's']);
    expect(Object.keys(PANEL_HEIGHTS.chart).sort()).toEqual(['l', 'm', 's']);
  });
});

describe('THRESHOLD_COLOURS — theme tokens, not literals', () => {
  it('every band resolves to a CSS var() token', () => {
    for (const key of ['green', 'amber', 'red'] as const) {
      expect(THRESHOLD_COLOURS[key]).toMatch(/^var\(--[a-z0-9-]+\)$/);
    }
  });

  it('maps to the status tokens the rest of the UI already uses', () => {
    expect(THRESHOLD_COLOURS.green).toBe('var(--ok)');
    expect(THRESHOLD_COLOURS.amber).toBe('var(--warn)');
    expect(THRESHOLD_COLOURS.red).toBe('var(--err)');
  });

  it('carries no baked rgb/hex literal (the pre-v0.9.778 shape)', () => {
    for (const v of Object.values(THRESHOLD_COLOURS)) {
      expect(v).not.toMatch(/rgb\(|#[0-9a-f]{3,6}/i);
    }
  });

  it('the three bands are distinct tokens', () => {
    expect(new Set(Object.values(THRESHOLD_COLOURS)).size).toBe(3);
  });
});

describe('thresholdTint — colour-mix, never a parsed rgba()', () => {
  it('wraps a token in color-mix at the requested percentage', () => {
    expect(thresholdTint(THRESHOLD_COLOURS.red))
      .toBe('color-mix(in srgb, var(--err) 12%, transparent)');
    expect(thresholdTint(THRESHOLD_COLOURS.green, 30))
      .toBe('color-mix(in srgb, var(--ok) 30%, transparent)');
  });

  it('never returns its input unchanged — the hexToRgba opaque-panel bug', () => {
    for (const v of Object.values(THRESHOLD_COLOURS)) {
      const tinted = thresholdTint(v);
      expect(tinted).not.toBe(v);
      expect(tinted).toContain('transparent');
    }
  });
});
