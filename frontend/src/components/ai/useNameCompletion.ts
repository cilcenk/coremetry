import { useEffect, useMemo, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { keys } from '@/lib/queries/keys';
import { completionQuery, moveHighlight, type CompletionQuery } from './chatCompletion';

// useNameCompletion — v0.10.687 (D4): CoSRE girişindeki token için sunucu
// taraflı servis adı adayları. Debounce 180 ms (picker kuralı), boş
// sorguda İSTEK YOK (enabled), aynı önek 5 dk cache (keys.services.names —
// ServicePicker ile aynı anahtar, iki yüzey birbirini ısıtır). Kapatılan
// (Esc) sorgu aynı token için yeniden açılmaz; token değişince yine açılır.
export interface NameCompletion {
  open: boolean;
  items: string[];
  highlight: number;
  cq: CompletionQuery | null;
  setHighlight: (i: number) => void;
  move: (delta: number) => void;
  dismiss: () => void;
}

export function useNameCompletion(text: string, caret: number): NameCompletion {
  const cq = useMemo(() => completionQuery(text, caret), [text, caret]);
  const live = cq?.query ?? '';
  const [dq, setDq] = useState('');
  useEffect(() => {
    const t = setTimeout(() => setDq(live), 180);
    return () => clearTimeout(t);
  }, [live]);
  const q = useQuery({
    queryKey: keys.services.names(dq),
    queryFn: () => api.serviceNames(dq, 20),
    enabled: dq.length > 0,
    staleTime: 5 * 60_000,
  });
  const items = useMemo(
    () => (cq && dq === cq.query ? (q.data?.names ?? []).slice(0, 8) : []),
    [cq, dq, q.data]);
  const [highlight, setHighlight] = useState(0);
  useEffect(() => { setHighlight(0); }, [dq]);
  const [dismissed, setDismissed] = useState<string | null>(null);
  const open = !!cq && items.length > 0 && dismissed !== cq.query;
  return {
    open, items, highlight, cq, setHighlight,
    move: d => setHighlight(h => moveHighlight(h, d, items.length)),
    dismiss: () => setDismissed(cq?.query ?? null),
  };
}
