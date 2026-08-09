import { useCallback, useEffect, useRef, useState } from 'react';
import { getRaw, setItem } from '@/lib/storage';

// useQueryHistory — Explore's "Son sorgular" (recent queries) ring.
//
// New in Phase-1 (explore-v2). Keeps the last MAX_HISTORY queries the
// operator ran, so the question-card entry screen can offer a one-click
// "jump back to what I was just looking at". Stored in localStorage so
// it survives a tab reload.
//
// The merge/dedupe/cap logic is a pure function (mergeHistory) so it's
// vitest-testable without a DOM; the hook is a thin wrapper that hydrates
// from localStorage, debounces writes (1s — avoids thrashing storage on
// every keystroke-driven URL change), and persists.
//
// `state` is intentionally opaque (unknown): Phase-1 stores the full
// `?…` search string so a click restores by navigating to that URL.
// Later phases can store a structured BuilderState without changing the
// ring mechanics.

export const HISTORY_KEY = 'coremetry-explore-history';
export const MAX_HISTORY = 4;
// 5s, not 1s — every ≥debounce pause while *building* a query records an
// entry, and with a 4-slot ring construction noise evicts genuinely
// reusable queries (Phase-1 review finding). 5s only records settled
// states.
export const SAVE_DEBOUNCE_MS = 5000;

export interface QueryHistoryEntry {
  // Human-readable one-line summary shown in the recent-queries list.
  // Also the dedupe key: re-running the same query bumps it to the
  // front instead of adding a duplicate row.
  desc: string;
  // Opaque restore payload. Phase-1: the full search string ("?…").
  state: unknown;
  // Epoch ms the entry was recorded — newest first.
  tm: number;
}

// mergeHistory — pure ring update. Prepends `entry`, drops any existing
// entry with the same `desc` (so re-running a query bumps it to front,
// not duplicates it), and caps at MAX_HISTORY. Skips empty descriptions.
// Newest-first ordering is preserved.
export function mergeHistory(
  prev: QueryHistoryEntry[],
  entry: QueryHistoryEntry,
): QueryHistoryEntry[] {
  if (!entry.desc) return prev;
  const deduped = prev.filter(e => e.desc !== entry.desc);
  return [entry, ...deduped].slice(0, MAX_HISTORY);
}

// parseHistory — tolerant decode of the persisted blob. Corrupt or
// non-array JSON yields []; rows missing required fields are dropped.
// Never throws — a poisoned localStorage value must not break the page.
export function parseHistory(raw: string | null): QueryHistoryEntry[] {
  if (!raw) return [];
  let val: unknown;
  try {
    val = JSON.parse(raw);
  } catch {
    return [];
  }
  if (!Array.isArray(val)) return [];
  const out: QueryHistoryEntry[] = [];
  for (const item of val) {
    if (
      item && typeof item === 'object' &&
      typeof (item as QueryHistoryEntry).desc === 'string' &&
      (item as QueryHistoryEntry).desc !== '' &&
      typeof (item as QueryHistoryEntry).tm === 'number'
    ) {
      const e = item as QueryHistoryEntry;
      out.push({ desc: e.desc, state: e.state, tm: e.tm });
    }
  }
  return out.slice(0, MAX_HISTORY);
}

// ── v0.9.849 — halkanın GÖRÜNEN hâli ────────────────────────────────────────
//
// Halka v0.9.562'den beri yazıyordu ama hiçbir yer OKUMUYORDU: giriş ekranı
// (soru kartları) kaldırılınca tek tüketicisi de gitti, hook öksüz kaldı.
// Aşağıdaki iki saf fonksiyon o listeyi geri görünür kılıyor.
//
// SAF ve NOW'U DIŞARIDAN ALIR: Date.now()'u içeriden okusaydı test bir
// saat dilimine/anlık zamana bağlanır, ve React tarafında da her render'da
// tazelenip gereksiz iş üretirdi (bu depoda timeRangeToNs'in v0.5.184
// dersi tam olarak bu).

// Etiket kırpma sınırı. 4 slotluk bir açılır listede satır başına bir
// SATIR var; builderDesc dört sorgu + formül + viz taşıyabildiği için
// kırpılmazsa panel yatay olarak taşar.
export const HISTORY_LABEL_MAX = 72;

export interface HistoryItemView {
  text: string;    // kırpılmış, tek satırlık özet
  when: string;    // "3 dk önce"
  title: string;   // TAM özet (kırpma bilgi kaybı değil, gizleme olsun)
  search: string;  // uygulanacak arama dizesi; '' = uygulanamaz
}

// historyRelTime — kaba göreli zaman. Kaba olması BİLİNÇLİ: bu liste
// "az önce neye bakıyordum" sorusuna cevap verir, bir denetim kaydı değil.
//
// NEGATİF FARK SIFIR SAYILIR. Kayıtlar localStorage'da yaşıyor ve makinenin
// saati geri alınabilir (NTP düzeltmesi, uçuş modundan dönüş); "-3 dk önce"
// yazan bir satır okuyanı veriden şüpheye düşürür.
export function historyRelTime(tm: number, nowMs: number): string {
  const d = Math.max(0, nowMs - tm);
  if (d < 60_000) return 'şimdi';
  if (d < 3_600_000) return `${Math.floor(d / 60_000)} dk önce`;
  if (d < 86_400_000) return `${Math.floor(d / 3_600_000)} sa önce`;
  return `${Math.floor(d / 86_400_000)} gün önce`;
}

// historyItemView — bir kaydın satır gösterimi.
//
// KIRPMA KOD NOKTASI BAZINDA (Array.from), byte ya da UTF-16 birimi
// bazında DEĞİL: operatörün sorgusunda Türkçe karakter ya da '·'/'ƒ' gibi
// işaretler var ve slice() bir surrogate çiftini ikiye bölerse ekrana
// bozuk glif düşer.
export function historyItemView(e: QueryHistoryEntry, nowMs: number): HistoryItemView {
  const cps = Array.from(e.desc);
  const text = cps.length > HISTORY_LABEL_MAX
    ? cps.slice(0, HISTORY_LABEL_MAX - 1).join('') + '…'
    : e.desc;
  const when = historyRelTime(e.tm, nowMs);
  // state Phase-1'den beri OPAK (unknown) — daraltmayı burada yapıyoruz ki
  // bozuk/eski bir kayıt tıklanınca navigate'e çöp gitmesin. '?' ile
  // başlamayan her şey uygulanamaz sayılır.
  const search = typeof e.state === 'string' && e.state.startsWith('?') ? e.state : '';
  return { text, when, title: `${e.desc}\n${when}`, search };
}

function readHistory(): QueryHistoryEntry[] {
  if (typeof window === 'undefined') return [];
  // getRaw absorbs disabled-storage throws; parseHistory absorbs
  // corrupt JSON — neither path can break the page.
  return parseHistory(getRaw(HISTORY_KEY));
}

export interface UseQueryHistory {
  history: QueryHistoryEntry[];
  // Debounced save — call freely (e.g. on every URL change); the actual
  // localStorage write + state update fires SAVE_DEBOUNCE_MS after the
  // last call. `tm` is stamped at flush time.
  save: (desc: string, state: unknown) => void;
}

export function useQueryHistory(): UseQueryHistory {
  const [history, setHistory] = useState<QueryHistoryEntry[]>(() => readHistory());
  // historyRef mirrors the ring OUTSIDE React state: the unmount flush
  // runs after this component is gone, where setHistory is a silent
  // no-op in React 18 — if the localStorage write lived inside the
  // setState updater it would never execute. All mutations flow through
  // flush(), so the ref is the single source of truth for writes.
  const historyRef = useRef<QueryHistoryEntry[]>(history);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const pending = useRef<{ desc: string; state: unknown } | null>(null);

  const flush = useCallback(() => {
    const p = pending.current;
    pending.current = null;
    if (!p || !p.desc) return;
    const next = mergeHistory(historyRef.current, { desc: p.desc, state: p.state, tm: Date.now() });
    historyRef.current = next;
    setItem(HISTORY_KEY, next); // storage full / disabled → helper no-ops; ring stays in-memory
    setHistory(next); // no-op after unmount; localStorage already written
  }, []);

  const save = useCallback((desc: string, state: unknown) => {
    pending.current = { desc, state };
    if (timer.current) clearTimeout(timer.current);
    timer.current = setTimeout(flush, SAVE_DEBOUNCE_MS);
  }, [flush]);

  // Flush a pending write on unmount so a fast navigate-away doesn't
  // drop the operator's last query.
  useEffect(() => () => {
    if (timer.current) {
      clearTimeout(timer.current);
      flush();
    }
  }, [flush]);

  return { history, save };
}
