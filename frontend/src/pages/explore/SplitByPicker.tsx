import { useMemo, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Combobox } from '@/components/Combobox';
import { api } from '@/lib/api';
import { SUGGESTED_GROUPBY } from './presets';

// SplitByPicker — group-by chips + a server-suggested key combobox
// (explore-v2 Phase 2). Suggestions merge the curated SUGGESTED_GROUPBY
// palette with the keys actually observed on recent spans
// (api.attributeKeys — plan ground-truth #8). The fetch is shared across
// every query row via one react-query key, 60s stale.

export function SplitByPicker({ value, onChange }: {
  value: string[];
  onChange: (keys: string[]) => void;
}) {
  const [draft, setDraft] = useState('');

  const keysQ = useQuery({
    queryKey: ['attribute-keys', '1h'],
    queryFn: () => api.attributeKeys('1h', 200),
    staleTime: 60_000,
  });

  const options = useMemo(() => {
    const seen = new Set(value);
    const out: string[] = [];
    for (const k of SUGGESTED_GROUPBY) {
      if (!seen.has(k)) { out.push(k); seen.add(k); }
    }
    for (const row of keysQ.data ?? []) {
      if (!seen.has(row.key)) { out.push(row.key); seen.add(row.key); }
    }
    return out;
  }, [keysQ.data, value]);

  const add = (k: string) => {
    const t = k.trim();
    if (!t || value.includes(t)) return;
    onChange([...value, t]);
    setDraft('');
  };

  return (
    <>
      {value.map(k => (
        <span key={k} className="fb-chip">
          <b>{k}</b>
          <button className="fb-chip-x" type="button"
            onClick={() => onChange(value.filter(x => x !== k))} aria-label="Remove">✕</button>
        </span>
      ))}
      <Combobox value={draft} onChange={setDraft}
        options={options}
        placeholder="+ split key" width={160}
        onEnter={() => add(draft)} />
      {draft && (
        <button className="sec" type="button" onClick={() => add(draft)}>Add</button>
      )}
    </>
  );
}
