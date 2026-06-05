import { useEffect, useState } from 'react';

// useDebouncedValue — defer a fast-changing value (query text, filter edits,
// brush selection) by `delayMs` so downstream react-query keys / heavy
// transforms only fire once the operator pauses (v0.8.6 Phase 0). 250ms is the
// house default: long enough to absorb a typing burst, short enough that the
// chart/table still feels live. Pair with react-query (whose queryFn forwards
// the AbortSignal) so the in-flight request for a superseded value is cancelled
// rather than racing the new one.
export function useDebouncedValue<T>(value: T, delayMs = 250): T {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const t = window.setTimeout(() => setDebounced(value), delayMs);
    return () => clearTimeout(t);
  }, [value, delayMs]);
  return debounced;
}
