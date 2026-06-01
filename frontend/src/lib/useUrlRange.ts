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
// shareable URL. The param is omitted when it equals the default so
// clean URLs stay clean.
export function useUrlRange(
  defaultPreset = '30m',
): [TimeRange, (r: TimeRange | ((prev: TimeRange) => TimeRange)) => void] {
  const [searchParams, setSearchParams] = useSearchParams();
  const raw = searchParams.get('range');

  const range = useMemo(
    () => decodeRange(raw, { preset: defaultPreset }),
    [raw, defaultPreset],
  );

  const setRange = useCallback(
    (r: TimeRange | ((prev: TimeRange) => TimeRange)) => {
      setSearchParams(
        prev => {
          const next = new URLSearchParams(prev);
          const curr = decodeRange(prev.get('range'), { preset: defaultPreset });
          const val = typeof r === 'function' ? r(curr) : r;
          const enc = encodeRange(val);
          if (enc === defaultPreset) {
            next.delete('range');
          } else {
            next.set('range', enc);
          }
          return next;
        },
        { replace: true },
      );
    },
    [setSearchParams, defaultPreset],
  );

  return [range, setRange];
}
