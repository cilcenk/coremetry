import { useEffect, useRef, useState } from 'react';
import { api } from '@/lib/api';
import { Combobox } from '@/components/Combobox';
import { shouldAutoCommit } from '@/components/ServicePicker';

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
    // v0.9.1024 — ServicePicker'ın SAF fonksiyonu. Buradaki kopya da
    // eski (v0.7.27 öncesi) ifadeydi: ilk tuş vuruşunda ve çok
    // karakterli SİLME sıçramalarında yanlış commit ediyordu.
    if (shouldAutoCommit(prev, next, optsRef.current.includes(next)) && onEnter) {
      setTimeout(() => onEnter(next), 0);
    }
  };

  const truncated = total > opts.length;

  return (
    // v0.9.1024 — native <datalist> → ev Combobox'ı. Sunucu arama
    // katmanı (debounce + /api/operation-names, servis kapsamı) aynen
    // duruyor; `serverFiltered` joker karakterleri korumak için şart.
    <Combobox
      value={value}
      onChange={handleChange}
      options={opts}
      serverFiltered
      placeholder={placeholder}
      width={width}
      onEnter={() => onEnter?.(undefined)}
      footer={truncated
        ? `… +${total - opts.length} more — refine search`
        : undefined}
      title={
        truncated
          ? `Showing ${opts.length} of ${total} operations — type to refine. Wildcards: pay*, *pay*, p?y`
          : 'Type to filter. Wildcards: pay*, *pay*, p?y'
      }
    />
  );
}
