import { useMemo, useState } from 'react';
import { attrKeyWindowParams } from '@/lib/attrKeyWindow';
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

export function SplitByPicker({ value, onChange, metric, service }: {
  value: string[];
  onChange: (keys: string[]) => void;
  /**
   * v0.9.1200 — metrik-kaynak modu: split önerileri span taramasından değil
   * o metrikte GÖRÜLMÜŞ etiketlerden (metricAttrKeys — metricsource seam'i,
   * VM'de /api/v1/labels). Span paleti (http.route vb.) metrik etiketi
   * olmayabilir ve VM'de sessizce hiç eşleşmezdi.
   */
  metric?: string;
  service?: string;
}) {
  const [draft, setDraft] = useState('');
  const metricMode = !!metric?.trim();

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
  const attrWindow = useMemo(() => attrKeyWindowParams(range), [range]);
  // Sorgu anahtarı pencerenin İMZASI — nesnenin kendisi her render'da yeni
  // referans olurdu ve React Query onu her seferinde yeni bir sorgu sayardı.
  const winSig = JSON.stringify(attrWindow);
  const keysQ = useQuery({
    queryKey: ['attribute-keys', winSig],
    queryFn: () => api.attributeKeys(attrWindow, 200),
    staleTime: 60_000,
    enabled: !metricMode,
  });
  const metricKeysQ = useQuery({
    queryKey: ['metric-attr-keys', metric ?? '', service ?? ''],
    queryFn: () => api.metricAttrKeys(metric!.trim(), service ?? ''),
    staleTime: 60_000,
    enabled: metricMode,
  });

  const options = useMemo(() => {
    const seen = new Set(value);
    const out: string[] = [];
    if (metricMode) {
      // Yalnız gerçek etiketler — span paleti burada tahmin olurdu.
      for (const k of metricKeysQ.data ?? []) {
        if (!seen.has(k)) { out.push(k); seen.add(k); }
      }
      return out;
    }
    for (const k of SUGGESTED_GROUPBY) {
      if (!seen.has(k)) { out.push(k); seen.add(k); }
    }
    for (const row of keysQ.data ?? []) {
      if (!seen.has(row.key)) { out.push(row.key); seen.add(row.key); }
    }
    return out;
  }, [keysQ.data, metricKeysQ.data, metricMode, value]);

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
