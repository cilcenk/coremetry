// rowSelection.ts — v0.10.246 DataTable dilim 1 (audit §9): satır seçimi
// saf çekirdeği. Anahtar = getRowId (indeks DEĞİL — sıralama/sayfalama
// indeksi kırar); Shift-aralığı çapası id ile tutulur, yeniden sıralamada
// çapa satırı nerede ise oradan ölçülür.

export interface SelectionState {
  ids: ReadonlySet<string>;
  anchor: string | null;
}

export const EMPTY_SELECTION: SelectionState = { ids: new Set(), anchor: null };

export function toggleRow(state: SelectionState, id: string, mode: 'single' | 'multi' = 'multi'): SelectionState {
  if (mode === 'single') {
    return state.ids.has(id) && state.ids.size === 1 ? EMPTY_SELECTION : { ids: new Set([id]), anchor: id };
  }
  const ids = new Set(state.ids);
  if (ids.has(id)) ids.delete(id); else ids.add(id);
  return { ids, anchor: id };
}

/** rangeSelect — çapadan hedefe (dahil) tüm görünür satırları ekler; çapa yoksa tekil. */
export function rangeSelect(state: SelectionState, orderedIds: readonly string[], targetId: string): SelectionState {
  const t = orderedIds.indexOf(targetId);
  if (t < 0) return state;
  const a = state.anchor ? orderedIds.indexOf(state.anchor) : -1;
  if (a < 0) return { ids: new Set([...state.ids, targetId]), anchor: targetId };
  const [lo, hi] = a <= t ? [a, t] : [t, a];
  const ids = new Set(state.ids);
  for (let i = lo; i <= hi; i++) ids.add(orderedIds[i]);
  return { ids, anchor: state.anchor };
}

export function selectAll(orderedIds: readonly string[]): SelectionState {
  return { ids: new Set(orderedIds), anchor: orderedIds[0] ?? null };
}

/** pruneSelection — sayfa/sıralama değişince görünmeyen id'ler düşer, çapa korunur (varsa). */
export function pruneSelection(state: SelectionState, orderedIds: readonly string[]): SelectionState {
  const keep = new Set(orderedIds);
  const ids = new Set([...state.ids].filter(id => keep.has(id)));
  if (ids.size === state.ids.size) return state;
  return { ids, anchor: state.anchor && keep.has(state.anchor) ? state.anchor : null };
}
