import { useEffect, useId, useRef, useState } from 'react';
import { api } from '@/lib/api';

// shouldAutoCommit decides whether a single onChange event represents a
// datalist PICK (or paste) rather than the operator typing — only then does the
// picker auto-fire onEnter. A pick replaces the field with a full option value
// in one event, so the length grows by MORE THAN ONE char at once. Incremental
// typing only ever grows the value one char at a time, so a single-char change
// is NEVER a pick.
//
// v0.7.27 — Operator-reported: in service-topology "Focus on", typing the FIRST
// letter of a service immediately loaded it. The old heuristic was
// `Math.abs(next.length-prev.length) > 1 || (next.length > 0 && prev === '')`;
// the `prev === ''` clause treated the first keystroke from an empty field as a
// jump, so if that first char exact-matched a (1-char) known option it
// committed on keystroke one. Directional `>1` growth removes the false
// positive. The only case it gives up — clicking a 1-char option from an empty
// field — is negligible and ambiguous with typing anyway.
export function shouldAutoCommit(prev: string, next: string, isKnownOption: boolean): boolean {
  return isKnownOption && next.length - prev.length > 1;
}

/**
 * ServicePicker — drop-in replacement for the old `<Combobox options={services}>`
 * pattern. Fetches matching service names from /api/service-names with a
 * debounced query so it works at any scale (10k+ services).
 *
 * Why not just preload all names client-side?
 *   /api/services is top-N capped for the dashboard view, which used to
 *   silently truncate every service-name dropdown that scraped its
 *   response. This component asks the dedicated /api/service-names
 *   endpoint instead — uncapped, MV-backed, supports `*` / `?`
 *   wildcards (e.g. `pay*`, `*pay*`, `p?y`).
 *
 * The page count badge ("showing 50 of 1234 — type to refine") helps
 * users understand they're seeing a subset and need to type to narrow.
 */
export function ServicePicker({
  value, onChange, placeholder, width, onEnter,
}: {
  value: string;
  onChange: (v: string) => void;
  placeholder?: string;
  width?: number | string;
  // onEnter fires when the operator either presses Enter or
  // picks an option from the datalist. When triggered by a
  // datalist pick the freshly-selected value is passed as the
  // argument so the parent can commit without waiting for
  // setState() to settle — the previous setTimeout-based
  // commit raced React's state update on multi-step pages
  // (Traces.tsx → draft → filter), leaving the actual fetch
  // running with the prior service. Keyboard Enter passes
  // undefined; the parent should read the latest input value
  // from its own state.
  onEnter?: (value?: string) => void;
}) {
  const listId = useId();
  const [opts, setOpts] = useState<string[]>([]);
  const [total, setTotal] = useState(0);
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  // Remembers what the user typed character-by-character. When
  // onChange fires with a value that jumps to an exact match of
  // a known option, we infer it came from a datalist click
  // (browsers fire an input event with the full option value,
  // no dedicated `select` event). Lets us auto-commit on pick
  // without re-firing on every keystroke through a name like
  // "orders" that's a prefix of "orders-api".
  const lastValueRef = useRef(value);
  // Holds the freshest options list so the click-detection
  // logic sees current names even when the state hasn't
  // re-rendered yet (the useEffect that updates opts runs
  // after the synchronous onChange).
  const optsRef = useRef<string[]>([]);

  // Debounced server fetch keyed off the typed value. Empty value → load
  // top-200 (alphabetical). Updates the datalist options so the browser's
  // native dropdown reflects whatever the user is filtering for.
  useEffect(() => {
    if (debounceRef.current) clearTimeout(debounceRef.current);
    debounceRef.current = setTimeout(() => {
      api.serviceNames(value, 200)
        .then(r => {
          setOpts(r.names);
          optsRef.current = r.names;
          setTotal(r.total);
        })
        .catch(() => { setOpts([]); optsRef.current = []; setTotal(0); });
    }, 180);
    return () => { if (debounceRef.current) clearTimeout(debounceRef.current); };
  }, [value]);

  const handleChange = (next: string) => {
    const prev = lastValueRef.current;
    lastValueRef.current = next;
    onChange(next);
    // Datalist-pick heuristic: a single onChange event jumped
    // the value to an exact match of a known option AND the
    // jump wasn't a single character (= not the user typing).
    // Multi-char jumps almost always come from a click on the
    // dropdown row. Schedule onEnter for the next tick so the
    // parent has applied the state from onChange first.
    const exact = optsRef.current.includes(next);
    const jumped = Math.abs(next.length - prev.length) > 1 || (next.length > 0 && prev === '');
    if (exact && jumped && onEnter) {
      // Pass the picked value through so the parent can
      // commit immediately, sidestepping React's setState
      // batching (parent's draft hasn't propagated yet when
      // this fires in the next microtask).
      setTimeout(() => onEnter(next), 0);
    }
  };

  const truncated = total > opts.length;

  return (
    <div className="cb-wrap" style={{ width }}>
      <input
        list={listId}
        value={value}
        placeholder={placeholder}
        onChange={e => handleChange(e.target.value)}
        onKeyDown={e => e.key === 'Enter' && onEnter?.(undefined)}
        autoComplete="off"
        spellCheck={false}
        title={
          truncated
            ? `Showing ${opts.length} of ${total} services — type to refine. Wildcards: pay*, *pay*, p?y`
            : 'Type to filter. Wildcards: pay*, *pay*, p?y'
        }
      />
      {value && (
        <button className="cb-clear" type="button"
          aria-label="Clear" title="Clear"
          onClick={() => onChange('')}
          onMouseDown={e => e.preventDefault()}>
          ✕
        </button>
      )}
      <datalist id={listId}>
        {opts.map(o => <option key={o} value={o} />)}
        {truncated && <option value="" disabled>… +{total - opts.length} more — refine search</option>}
      </datalist>
    </div>
  );
}
