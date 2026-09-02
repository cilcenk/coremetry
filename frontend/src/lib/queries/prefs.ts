// prefs.ts — v0.10.248 (DataTable/ContextBar audit §11): kalıcı sütun
// tercihi istemcisi. GET /api/preferences/{key} (staleTime 5 dk, sekme
// kapanınca iptal — cancellation.test.ts), PUT 800 ms debounce + iyimser
// yerel yazım, sekmeler arası BroadcastChannel('coremetry-prefs').
//
// Hata sözleşmesi (audit): GET hatası → model null (istemci yerel/varsayılan
// modele düşer; tablo asla tercihe kilitlenmez); PUT hatası → yerel kalır,
// bir sonraki değişimde yeniden dener. Kimlik yoksa (401) da aynı: null.
// Kullanıcı bağlamı anahtara girmez — oturum değişince queryClient
// temizlenir (AuthProvider).

import { useCallback, useEffect, useMemo, useRef } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '../api';
import { keys } from './keys';
import { parseColumnModel, serializeColumnModel, type ColumnModel } from '../columnModel';

const PUT_DEBOUNCE_MS = 800;
const CHANNEL = 'coremetry-prefs';

export interface TablePrefs {
  /** undefined = henüz bilinmiyor (sorgu bekliyor); null = sunucuda yok/hata. */
  model: ColumnModel | null | undefined;
  status: 'pending' | 'success' | 'error';
  /** İyimser: cache'e hemen yazar, 800 ms sonra PUT. */
  save: (next: ColumnModel) => void;
  /** Sunucudaki tercihi siler (tombstone) — "Sıfırla". */
  reset: () => void;
}

function channel(): BroadcastChannel | null {
  try {
    return typeof BroadcastChannel === 'function' ? new BroadcastChannel(CHANNEL) : null;
  } catch {
    return null;
  }
}

export function useTablePrefs(storageKey: string, opts?: { enabled?: boolean }): TablePrefs {
  const qc = useQueryClient();
  const enabled = opts?.enabled ?? true;
  const q = useQuery<ColumnModel | null>({
    queryKey: keys.prefs.get(storageKey),
    queryFn: ({ signal }) =>
      api.getPreference(storageKey, signal)
        .then(r => (r?.model == null ? null : parseColumnModel(r.model)))
        .catch(() => null),
    staleTime: 5 * 60_000,
    enabled,
  });

  // Sekmeler arası: başka sekme kaydedince cache'i güncelle (ikinci GET yok).
  useEffect(() => {
    if (!enabled) return;
    const ch = channel();
    if (!ch) return;
    const onMsg = (ev: MessageEvent) => {
      const d = ev.data as { key?: string; model?: unknown } | null;
      if (!d || d.key !== storageKey) return;
      qc.setQueryData(keys.prefs.get(storageKey), d.model == null ? null : parseColumnModel(d.model));
    };
    ch.addEventListener('message', onMsg);
    return () => { ch.removeEventListener('message', onMsg); ch.close(); };
  }, [storageKey, enabled, qc]);

  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const pending = useRef<ColumnModel | null>(null);
  const flush = useCallback(() => {
    timer.current = null;
    const m = pending.current;
    if (!m) return;
    pending.current = null;
    // Sunucu genişlik saklamaz (serializeColumnModel widths'i düşürür).
    api.putPreference(storageKey, JSON.parse(serializeColumnModel(m)))
      .then(() => { channel()?.postMessage({ key: storageKey, model: JSON.parse(serializeColumnModel(m)) }); })
      .catch(() => { /* yerel kalır; bir sonraki save yeniden dener */ });
  }, [storageKey]);

  const save = useCallback((next: ColumnModel) => {
    qc.setQueryData(keys.prefs.get(storageKey), next);
    pending.current = next;
    if (timer.current) clearTimeout(timer.current);
    timer.current = setTimeout(flush, PUT_DEBOUNCE_MS);
  }, [qc, storageKey, flush]);

  const reset = useCallback(() => {
    if (timer.current) { clearTimeout(timer.current); timer.current = null; }
    pending.current = null;
    qc.setQueryData(keys.prefs.get(storageKey), null);
    api.deletePreference(storageKey)
      .then(() => { channel()?.postMessage({ key: storageKey, model: null }); })
      .catch(() => { /* yerel sıfırlandı; sunucu bir sonraki save'de üzerine yazar */ });
  }, [qc, storageKey]);

  // Unmount'ta bekleyen PUT'u hemen gönder (sekme kapanışı kaybetmesin).
  useEffect(() => () => { if (timer.current) { clearTimeout(timer.current); flush(); } }, [flush]);

  return useMemo<TablePrefs>(() => ({
    model: q.status === 'pending' ? undefined : (q.data ?? null),
    status: q.status,
    save, reset,
  }), [q.status, q.data, save, reset]);
}
