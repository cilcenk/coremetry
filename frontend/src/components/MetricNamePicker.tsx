import { useEffect, useRef, useState } from 'react';
import { api } from '@/lib/api';
import { Combobox } from '@/components/Combobox';
import { shouldAutoCommit } from '@/components/ServicePicker';
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
 * v0.9.1024 — bu metadata artık açılır listenin İÇİNDE, satır
 * sonunda soluk bir etiket olarak görünüyor (`optionMeta`). Eskiden
 * datalist'in `label` niteliğine bırakılmıştı: Chromium/Firefox
 * gösteriyor, Safari HİÇ göstermiyordu — yani "birim ve tip
 * görünüyor" iddiası tarayıcıya göre doğru ya da yanlıştı.
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
    // v0.9.1024 — ServicePicker'ın saf fonksiyonu (v0.7.27 sözleşmesi).
    const picked = optsRef.current.find(m => m.name === next);
    if (shouldAutoCommit(prev, next, !!picked) && picked) {
      if (onPick) setTimeout(() => onPick(picked), 0);
      if (onEnter) setTimeout(() => onEnter(next), 0);
    }
  };

  const truncated = total > opts.length;

  // Render tarafı `opts` DURUMUNU okur, ref'i değil: ref güncellemesi
  // yeniden render tetiklemez, dolayısıyla ref'ten okunan bir etiket
  // bir tur geride kalabilirdi (ikisi birlikte yazılıyor ama render
  // sırasında doğrusu durum).
  const names = opts.map(o => o.name);
  const metaOf = (n: string) => {
    const m = opts.find(x => x.name === n);
    return m ? [m.unit, m.type].filter(Boolean).join(' · ') || undefined : undefined;
  };

  return (
    // v0.9.1024 — native <datalist> → ev Combobox'ı; sunucu arama
    // katmanı (debounce + /api/metric-names) aynen duruyor.
    <Combobox
      value={value}
      onChange={handleChange}
      options={names}
      serverFiltered
      placeholder={placeholder}
      width={width}
      onEnter={() => onEnter?.(undefined)}
      optionMeta={metaOf}
      footer={truncated
        ? `… +${total - opts.length} more — refine search`
        : undefined}
      title={
        truncated
          ? `Showing ${opts.length} of ${total} metrics — type to refine. Wildcards: http.*, *latency*, p?y`
          : 'Type to filter. Wildcards: http.*, *latency*, p?y'
      }
    />
  );
}
