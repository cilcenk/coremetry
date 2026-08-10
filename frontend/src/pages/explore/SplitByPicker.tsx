import { useMemo, useState } from 'react';
import { attrKeySince } from '@/lib/attrKeyWindow';
import { useUrlRange } from '@/lib/useUrlRange';
import { useQuery } from '@tanstack/react-query';
import { Combobox } from '@/components/Combobox';
import { Button } from '@/components/ui';
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

  // v0.9.953 (F3/Ö14c) — keşif penceresi sayfanın aralığından, BASAMAKLI.
  // Basamak hem sunucu cache anahtarına hem react-query anahtarına aynen
  // giriyor: serbest bir pencere ikisini de her dokunuşta ıskalatırdı
  // (v0.8.270).
  // useMemo ŞART (v0.9.956, v0.5.184 sınıfı): attrKeySince içeride
  // timeRangeToNs çağırıyor ve o da preset aralıklarda now() okuyor.
  // Çıplak çağrı bugün zararsız görünüyor (snapSince beş basamağa
  // yuvarladığı için dize sabit kalıyor, yani sorgu anahtarı oynamıyor)
  // AMA yasak olan ŞEKLİN kendisi: basamak listesi bir gün incelirse
  // aynı satır sessizce sonsuz refetch'e döner.
  const [range] = useUrlRange();
  const since = useMemo(() => attrKeySince(range), [range]);
  const keysQ = useQuery({
    queryKey: ['attribute-keys', since],
    queryFn: () => api.attributeKeys(since, 200),
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
        <Button variant="secondary" onClick={() => add(draft)}>Add</Button>
      )}
    </>
  );
}
