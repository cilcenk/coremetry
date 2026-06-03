import { useCallback, useMemo } from 'react';
import { useSearchParams } from 'react-router-dom';
import type { TimeRange } from './types';
import { encodeRange, decodeRange } from './urlState';

// useUrlRange (v0.7.87) — the SINGLE source of truth for a page's time
// range: the URL `?range=` param, not component-local state.
//
// Why: ~20 pages held the range only in useState, so a drill link
// (?range=…) was silently dropped on the target page, cross-signal
// pivots (service → its logs/traces) lost the operator's window, and
// Share / saved-views / browser-back all produced the wrong window.
// This hook makes the range shareable + restorable everywhere with a
// drop-in swap: `useState<TimeRange>({preset:'30m'})` → `useUrlRange()`.
// Same [value, setValue] tuple, same SetStateAction signature, so a
// page adopts it by changing one line.
//
// CRITICAL — object identity. `range` is derived from the URL string on
// every render. If it were a fresh object each render, any
// `useMemo(() => timeRangeToNs(range), [range])` downstream would see a
// new dep every render and refetch forever (the v0.5.184 trap). So
// `range` is memoised on the raw `?range=` STRING: its identity changes
// only when the URL range actually changes. defaultPreset is a string
// primitive (stable) for the same reason.
//
// Writes use { replace: true }: a range tweak refines the current view,
// it shouldn't pile a history entry per click — browser-back returns to
// the previous PAGE, while the current page's range still lives in its
// shareable URL.
//
// GLOBAL window (v0.7.124 — UX pass #2). A page that loads WITHOUT an
// explicit `?range=` inherits the last range the operator picked anywhere,
// persisted in localStorage, so switching pages keeps the window. Precedence:
//   1. `?range=` in the URL  — wins (shareable links + browser back/forward)
//   2. localStorage          — cross-page continuity
//   3. defaultPreset         — first-ever load
// A fresh pick writes BOTH the URL and localStorage. `effective` stays a
// stable string so the memo identity only changes when the resolved range
// actually changes (the v0.5.184 infinite-refetch trap).
const RANGE_STORE_KEY = 'coremetry-range';
function readStoredRange(): string | null {
  try { return localStorage.getItem(RANGE_STORE_KEY); } catch { return null; }
}
function writeStoredRange(enc: string): void {
  try { localStorage.setItem(RANGE_STORE_KEY, enc); } catch { /* private mode / quota */ }
}

// storedRangeString — public read of the persisted global range, for pages
// that own a bespoke URL-range pipeline (Explore, Metrics) and only need to
// INHERIT the cross-page window on first render without adopting the hook's
// write path. Returns null when nothing's been picked yet. (UX pass #2.)
export function storedRangeString(): string | null {
  return readStoredRange();
}

export function useUrlRange(
  defaultPreset = '30m',
): [TimeRange, (r: TimeRange | ((prev: TimeRange) => TimeRange)) => void] {
  const [searchParams, setSearchParams] = useSearchParams();
  const raw = searchParams.get('range');
  const effective = raw ?? readStoredRange() ?? defaultPreset;

  const range = useMemo(
    () => decodeRange(effective, { preset: defaultPreset }),
    [effective, defaultPreset],
  );

  const setRange = useCallback(
    (r: TimeRange | ((prev: TimeRange) => TimeRange)) => {
      setSearchParams(
        prev => {
          const next = new URLSearchParams(prev);
          const curr = decodeRange(prev.get('range') ?? readStoredRange(), { preset: defaultPreset });
          const val = typeof r === 'function' ? r(curr) : r;
          const enc = encodeRange(val);
          writeStoredRange(enc);   // persist globally → cross-page continuity
          next.set('range', enc);  // reflect in the URL → shareable + back/forward
          return next;
        },
        { replace: true },
      );
    },
    [setSearchParams, defaultPreset],
  );

  return [range, setRange];
}
