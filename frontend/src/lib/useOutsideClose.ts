import { useEffect, type RefObject } from 'react';

// useOutsideClose — v0.8.361 (operator-reported): the trace span-detail
// side panel only closed via its ✕ (or Esc); clicking the page
// background should dismiss it like any transient surface.
//
// Listens on document mousedown while `active` and calls `onClose`
// when the press lands outside `ref`. mousedown (not click) so a
// text-selection drag that STARTS inside the panel and ends outside
// never counts as an outside press. The ref wraps the panel AND the
// surface that drives its selection (the waterfall) — a press on a
// row is a re-select, not a dismiss, and excluding it here avoids a
// close→reopen remount flicker (the panel would lose scroll position
// and refetch its profiles/logs).
export function useOutsideClose(ref: RefObject<HTMLElement | null>, active: boolean, onClose: () => void) {
  useEffect(() => {
    if (!active) return;
    const onDown = (e: MouseEvent) => {
      const t = e.target as Node | null;
      if (t && ref.current && !ref.current.contains(t)) onClose();
    };
    document.addEventListener('mousedown', onDown);
    return () => document.removeEventListener('mousedown', onDown);
  }, [active, ref, onClose]);
}
