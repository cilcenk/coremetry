// LogPillEditor — v0.9.1219 (Kibana paritesi, dilim 1). Pill'e tıkla →
// yerinde düzenleme popover'ı: alan / operatör (is · is not · exists ·
// not exists · is one of · not one of) / değer(ler). Kibana'nın "Edit
// filter" diyaloğunun pill-altı hâli; bugüne dek tek yol sil+yeniden
// ekleydi.
//
// ES disiplini: değer önerisi MEVCUT logsFieldValues ucundan (since-
// sınırlı, sunucuda 30 sn cache), yalnız alan doluyken ve 200 ms
// debounce'la — yeni uç yok, sorgu sınıfı KqlSearchInput'unkiyle aynı.
// Commit compileSearch'e derlenir: backend sözleşmesi değişmez.

import { useEffect, useRef, useState } from 'react';
import { Button } from '@/components/ui';
import { api } from '@/lib/api';
import { useEscLayer } from '@/lib/escLayer';
import type { LogFilter } from '@/lib/logFilters';

type PillOp = 'is' | 'is_not' | 'exists' | 'not_exists' | 'one_of' | 'not_one_of';

function opOf(f: LogFilter): PillOp {
  if (f.exists) return f.negated ? 'not_exists' : 'exists';
  if (f.values && f.values.length > 1) return f.negated ? 'not_one_of' : 'one_of';
  return f.negated ? 'is_not' : 'is';
}

const OP_LABELS: Record<PillOp, string> = {
  is: 'is', is_not: 'is not', exists: 'exists', not_exists: 'not exists',
  one_of: 'is one of', not_one_of: 'is not one of',
};

export function LogPillEditor({ filter, since, onApply, onCancel }: {
  filter: LogFilter;
  since?: string;
  onApply: (next: LogFilter) => void;
  onCancel: () => void;
}) {
  const [key, setKey] = useState(filter.key);
  const [op, setOp] = useState<PillOp>(opOf(filter));
  const [val, setVal] = useState(
    filter.values && filter.values.length > 1 ? filter.values.join(', ') : filter.value);
  const [suggestions, setSuggestions] = useState<string[]>([]);
  const boxRef = useRef<HTMLDivElement>(null);
  useEscLayer(true, onCancel);

  // Dışarı tıklama = iptal (popover disiplini).
  useEffect(() => {
    const onDown = (e: MouseEvent) => {
      if (boxRef.current && !boxRef.current.contains(e.target as Node)) onCancel();
    };
    document.addEventListener('mousedown', onDown);
    return () => document.removeEventListener('mousedown', onDown);
  }, [onCancel]);

  const needsValue = op === 'is' || op === 'is_not' || op === 'one_of' || op === 'not_one_of';

  // Değer önerileri — alan doluyken, 200 ms debounce, tek uç.
  useEffect(() => {
    const k = key.trim();
    if (!k || !needsValue) { setSuggestions([]); return; }
    let cancelled = false;
    const t = window.setTimeout(() => {
      api.logsFieldValues(k, '', 8, since)
        .then(r => { if (!cancelled) setSuggestions(r?.values ?? []); })
        .catch(() => { if (!cancelled) setSuggestions([]); });
    }, 200);
    return () => { cancelled = true; clearTimeout(t); };
  }, [key, needsValue, since]);

  const commit = () => {
    const k = key.trim();
    const negated = op === 'is_not' || op === 'not_exists' || op === 'not_one_of';
    if (op === 'exists' || op === 'not_exists') {
      onApply({ key: k, value: '', negated, disabled: filter.disabled, exists: true });
      return;
    }
    const parts = val.split(',').map(v => v.trim()).filter(Boolean);
    if ((op === 'one_of' || op === 'not_one_of') && parts.length > 1) {
      onApply({ key: k, value: '', negated, disabled: filter.disabled, values: parts });
      return;
    }
    onApply({ key: k, value: parts[0] ?? val.trim(), negated, disabled: filter.disabled });
  };

  return (
    <div ref={boxRef} style={{
      position: 'absolute', top: '100%', left: 0, marginTop: 4, zIndex: 'var(--z-dropdown)',
      background: 'var(--bg1)', border: '1px solid var(--border)', borderRadius: 8,
      padding: 10, minWidth: 300, boxShadow: '0 6px 24px rgba(0,0,0,.25)',
      display: 'grid', gap: 6,
    }}>
      <input className="field" value={key} onChange={e => setKey(e.target.value)}
        placeholder="alan" aria-label="Filtre alanı" />
      <select value={op} onChange={e => setOp(e.target.value as PillOp)} aria-label="Operatör">
        {(Object.keys(OP_LABELS) as PillOp[]).map(o => (
          <option key={o} value={o}>{OP_LABELS[o]}</option>
        ))}
      </select>
      {needsValue && (
        <>
          <input className="field" value={val} onChange={e => setVal(e.target.value)}
            placeholder={op === 'one_of' || op === 'not_one_of' ? 'değerler (virgülle)' : 'değer'}
            aria-label="Filtre değeri"
            onKeyDown={e => { if (e.key === 'Enter') { e.preventDefault(); commit(); } }} />
          {suggestions.length > 0 && (
            <div style={{ display: 'flex', flexWrap: 'wrap', gap: 4 }}>
              {suggestions.map(sv => (
                <button key={sv} type="button" className="sec"
                  style={{ fontSize: 10.5, padding: '1px 7px' }}
                  onClick={() => setVal(v => {
                    if (op === 'one_of' || op === 'not_one_of') {
                      const parts = v.split(',').map(x => x.trim()).filter(Boolean);
                      if (parts.includes(sv)) return v;
                      return [...parts, sv].join(', ');
                    }
                    return sv;
                  })}>
                  {sv}
                </button>
              ))}
            </div>
          )}
        </>
      )}
      <div style={{ display: 'flex', gap: 6, justifyContent: 'flex-end' }}>
        <Button variant="secondary" size="sm" onClick={onCancel}>İptal</Button>
        <Button variant="primary" size="sm" onClick={commit} disabled={!key.trim()}>Uygula</Button>
      </div>
    </div>
  );
}
