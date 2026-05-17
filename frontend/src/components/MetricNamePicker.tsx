import { useEffect, useId, useRef, useState } from 'react';
import { api } from '@/lib/api';
import type { MetricInfo } from '@/lib/types';

/**
 * MetricNamePicker — metric-names counterpart of ServicePicker /
 * OperationPicker (v0.5.181). Same debounced server-side search
 * + wildcards. Replaces the previous Combobox-with-eager-list
 * pattern in /metrics that fetched every metric name on mount;
 * at 10k+ metric names that round-trip was the main contributor
 * to the page's TTFI.
 *
 * Differs from ServicePicker / OperationPicker in that each
 * option is annotated with unit + instrument type, since
 * operators routinely need to know "is this a counter or a
 * gauge, in seconds or milliseconds" before picking. The
 * <datalist> can't render rich rows so we surface the metadata
 * via the input's `title` (and via the optional onPick callback
 * for downstream UI).
 */
export function MetricNamePicker({
  service, value, onChange, placeholder, width, onEnter, onPick,
}: {
  service: string;
  value: string;
  onChange: (v: string) => void;
  placeholder?: string;
  width?: number | string;
  onEnter?: (value?: string) => void;
  // Fires with the full MetricInfo when an option is picked from
  // the dropdown — gives downstream callers access to unit /
  // type without a second round-trip. Optional.
  onPick?: (m: MetricInfo) => void;
}) {
  const listId = useId();
  const [opts, setOpts] = useState<MetricInfo[]>([]);
  const [total, setTotal] = useState(0);
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const lastValueRef = useRef(value);
  const optsRef = useRef<MetricInfo[]>([]);

  useEffect(() => {
    if (debounceRef.current) clearTimeout(debounceRef.current);
    debounceRef.current = setTimeout(() => {
      api.metricNamesSearch(service, value, 200)
        .then(r => {
          const arr = r.names ?? [];
          setOpts(arr);
          optsRef.current = arr;
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
    const picked = optsRef.current.find(m => m.name === next);
    const jumped = Math.abs(next.length - prev.length) > 1 || (next.length > 0 && prev === '');
    if (picked && jumped) {
      if (onPick) setTimeout(() => onPick(picked), 0);
      if (onEnter) setTimeout(() => onEnter(next), 0);
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
            ? `Showing ${opts.length} of ${total} metrics — type to refine. Wildcards: http.*, *latency*, p?y`
            : 'Type to filter. Wildcards: http.*, *latency*, p?y'
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
        {opts.map(o => (
          <option key={o.name} value={o.name}>
            {/* datalist `label` shows alongside the value in
                Chromium/Firefox dropdowns. Operators glance at
                unit + type without having to read every metric
                name. */}
            {[o.unit, o.type].filter(Boolean).join(' · ') || undefined}
          </option>
        ))}
        {truncated && (
          <option value="" disabled>
            … +{total - opts.length} more — refine search
          </option>
        )}
      </datalist>
    </div>
  );
}
