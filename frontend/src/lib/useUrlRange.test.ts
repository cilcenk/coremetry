// useUrlRange.test.ts — v0.8.409 regression: an ABSOLUTE brushed
// window must never become the sticky cross-page default.
// Operator-reported: after a chart brush pinned custom:from-to into
// localStorage, every page load (and F5) served the same past window
// — read as "the page is cached". Relative presets stay sticky;
// custom windows are URL-only.
import { describe, expect, it } from 'vitest';
import { persistableRange } from './useUrlRange';

describe('persistableRange (v0.8.409)', () => {
  it('relative presets persist as the global default', () => {
    for (const enc of ['5m', '15m', '30m', '1h', '24h', '7d']) {
      expect(persistableRange(enc)).toBe(true);
    }
  });
  it('absolute custom windows never persist', () => {
    expect(persistableRange('custom:1751980000000-1751983600000')).toBe(false);
  });
});
