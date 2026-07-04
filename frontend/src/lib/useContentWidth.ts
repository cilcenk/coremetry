// lib/useContentWidth.ts — GRAN-A (v0.8.245): the DOM half of the
// Grafana-style width-aware step (lib/chartStep.ts holds the pure math).
//
// Tracks the #content element's clientWidth — the app-shell main column every
// page renders into — through a ResizeObserver, quantized into 200px buckets
// (quantizeWidth) so consumers re-render (and refetch: the bucket enters the
// react-query key via the effective step) only when a drag-resize crosses a
// bucket boundary, not per observer tick. Falls back to 1200 when #content
// isn't in the DOM (tests, detached renders) and is SSR-safe: the lazy
// initializer guards on `document`, and the observer only attaches in the
// browser effect.
//
// GRAN-C (v0.8.248) — optional `watch` re-measure key. Pages that mount
// BEFORE #content exists (Dashboard.tsx early-returns a bare spinner until
// the doc loads) pass a value that flips once the element is in the DOM so
// the lookup + observer re-attach; the no-argument Explore call keeps the
// GRAN-A one-shot behaviour unchanged.

import { useEffect, useState } from 'react';
import { quantizeWidth } from './chartStep';

const FALLBACK_PX = 1200;

export function useContentWidth(watch?: unknown): number {
  const [width, setWidth] = useState<number>(() => {
    if (typeof document === 'undefined') return quantizeWidth(FALLBACK_PX);
    const el = document.getElementById('content');
    return quantizeWidth(el?.clientWidth || FALLBACK_PX);
  });

  useEffect(() => {
    const el = document.getElementById('content');
    if (!el || typeof ResizeObserver === 'undefined') return;
    const update = () => setWidth(quantizeWidth(el.clientWidth || FALLBACK_PX));
    update(); // the lazy init may have run before layout settled
    const ro = new ResizeObserver(update);
    ro.observe(el);
    return () => ro.disconnect();
    // `watch` is intentionally the only dep — it re-runs the #content lookup
    // when the caller signals the element (dis)appeared.
  }, [watch]);

  return width;
}
