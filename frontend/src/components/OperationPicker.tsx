import { useEffect, useId, useRef, useState } from 'react';
import { api } from '@/lib/api';

/**
 * OperationPicker — operations-picker counterpart to ServicePicker
 * (v0.5.180). Same debounced server-side search + wildcard
 * semantics. Drop-in replacement for `<Combobox options={ops}>`
 * which eager-loaded the top-500 ops per service — long-tail
 * operations on a 10k-op service were unreachable from the
 * picker without this.
 *
 * Service filter is recommended (and usually present in the
 * parent context — Traces page, etc.) — without it the picker
 * lists every op across every service which is rarely useful
 * past tens of thousands of operations.
 */
export function OperationPicker({
  service, value, onChange, placeholder, width, onEnter,
}: {
  // Scope the search to one service. Pass undefined / empty to
  // search across every service (cardinality permitting).
  service?: string;
  value: string;
  onChange: (v: string) => void;
  placeholder?: string;
  width?: number | string;
  onEnter?: (value?: string) => void;
}) {
  const listId = useId();
  const [opts, setOpts] = useState<string[]>([]);
  const [total, setTotal] = useState(0);
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const lastValueRef = useRef(value);
  const optsRef = useRef<string[]>([]);

  useEffect(() => {
    if (debounceRef.current) clearTimeout(debounceRef.current);
    debounceRef.current = setTimeout(() => {
      api.operationNames(service || undefined, value, 200)
        .then(r => {
          setOpts(r.names);
          optsRef.current = r.names;
          setTotal(r.total);
        })
        .catch(() => { setOpts([]); optsRef.current = []; setTotal(0); });
    }, 180);
    return () => { if (debounceRef.current) clearTimeout(debounceRef.current); };
  }, [value, service]);

  const handleChange = (next: string) => {
    const prev = lastValueRef.current;
    lastValueRef.current = next;
    onChange(next);
    const exact = optsRef.current.includes(next);
    const jumped = Math.abs(next.length - prev.length) > 1 || (next.length > 0 && prev === '');
    if (exact && jumped && onEnter) {
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
            ? `Showing ${opts.length} of ${total} operations — type to refine. Wildcards: pay*, *pay*, p?y`
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
        {truncated && (
          <option value="" disabled>
            … +{total - opts.length} more — refine search
          </option>
        )}
      </datalist>
    </div>
  );
}
