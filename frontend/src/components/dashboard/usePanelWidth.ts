// components/dashboard/usePanelWidth.ts — GRAN-C (v0.8.248): measure a
// dashboard panel's OWN container for the width-aware auto step.
//
// Dashboard panels sit side-by-side in a 4-col grid, so the app-shell-level
// useContentWidth (#content — Explore's GRAN-A hook) is the wrong yardstick
// here: a quarter-width panel has a quarter of the pixels. Attach the
// returned ref to the panel body div; widthPx is the 200px-bucketed
// (quantizeWidth) clientWidth, or null until the first layout measurement —
// callers gate their auto-step fetch on non-null so no request fires at a
// guessed width. Bucketing means a drag-resize re-renders (and refetches)
// only on bucket crossings, not per ResizeObserver tick.

import { useLayoutEffect, useRef, useState } from 'react';
import { quantizeWidth } from '@/lib/chartStep';

const FALLBACK_PX = 1200; // ref never attached (defensive) — same as useContentWidth

export function usePanelWidth(): {
  ref: React.MutableRefObject<HTMLDivElement | null>;
  widthPx: number | null;
} {
  const ref = useRef<HTMLDivElement | null>(null);
  const [widthPx, setWidthPx] = useState<number | null>(null);

  // useLayoutEffect (not useEffect) so the first measurement lands before
  // paint — the fetch effect that depends on widthPx then fires on the very
  // next pass instead of after a visible spinner frame.
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) { setWidthPx(quantizeWidth(FALLBACK_PX)); return; }
    const update = () => setWidthPx(quantizeWidth(el.clientWidth || FALLBACK_PX));
    update();
    if (typeof ResizeObserver === 'undefined') return;
    const ro = new ResizeObserver(update);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  return { ref, widthPx };
}
