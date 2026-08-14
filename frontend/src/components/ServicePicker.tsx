import { useEffect, useRef, useState } from 'react';
import { api } from '@/lib/api';
import { Combobox } from '@/components/Combobox';

// shouldAutoCommit decides whether a single onChange event represents a
// PICK (or paste) rather than the operator typing — only then does the
// picker auto-fire onEnter. A pick replaces the field with a full option value
// in one event, so the length grows by MORE THAN ONE char at once. Incremental
// typing only ever grows the value one char at a time, so a single-char change
// is NEVER a pick.
//
// v0.9.1024 — BU FONKSİYON ÜÇ YIL BOYUNCA ÇAĞRILMADI. v0.7.27'de
// yazıldı, test edildi, ama bileşen eski satır içi ifadeyi
// (`Math.abs(next.length - prev.length) > 1 || (next.length > 0 &&
// prev === '')`) kullanmaya devam etti. Yani düzeltmenin KENDİSİ
// canlıya hiç çıkmadı: testler yeşil, davranış hatalı. Saf fonksiyonu
// çıkarıp test etmek yetmiyor — BAĞLAMAK da gerekiyor.
// Bağlantı artık ServicePicker.test.ts'te ayrıca çivili.
//
// v0.7.27 — Operator-reported: in service-topology "Focus on", typing the FIRST
// letter of a service immediately loaded it. The old heuristic was
// `Math.abs(next.length-prev.length) > 1 || (next.length > 0 && prev === '')`;
// the `prev === ''` clause treated the first keystroke from an empty field as a
// jump, so if that first char exact-matched a (1-char) known option it
// committed on keystroke one. Directional `>1` growth removes the false
// positive. The only case it gives up — clicking a 1-char option from an empty
// field — is negligible and ambiguous with typing anyway.
export function shouldAutoCommit(prev: string, next: string, isKnownOption: boolean): boolean {
  return isKnownOption && next.length - prev.length > 1;
}

/**
 * ServicePicker — drop-in replacement for the old `<Combobox options={services}>`
 * pattern. Fetches matching service names from /api/service-names with a
 * debounced query so it works at any scale (10k+ services).
 *
 * Why not just preload all names client-side?
 *   /api/services is top-N capped for the dashboard view, which used to
 *   silently truncate every service-name dropdown that scraped its
 *   response. This component asks the dedicated /api/service-names
 *   endpoint instead — uncapped, MV-backed, supports `*` / `?`
 *   wildcards (e.g. `pay*`, `*pay*`, `p?y`).
 *
 * The page count badge ("showing 50 of 1234 — type to refine") helps
 * users understand they're seeing a subset and need to type to narrow.
 */
export function ServicePicker({
  value, onChange, placeholder, width, onEnter, shortcutSearch,
}: {
  value: string;
  onChange: (v: string) => void;
  placeholder?: string;
  width?: number | string;
  // shortcutSearch (v0.9.951, E5/Ö31) — bu picker sayfanın `/` KISAYOL
  // hedefi mi? GlobalShortcuts'ın açık opt-in işareti; verilmezse
  // fallback (ilk görünür metin kutusu) sürer ve o fallback /traces'te
  // yanlış kutuya düşüyordu.
  shortcutSearch?: boolean;
  // onEnter fires when the operator either presses Enter or
  // picks an option from the datalist. When triggered by a
  // datalist pick the freshly-selected value is passed as the
  // argument so the parent can commit without waiting for
  // setState() to settle — the previous setTimeout-based
  // commit raced React's state update on multi-step pages
  // (Traces.tsx → draft → filter), leaving the actual fetch
  // running with the prior service. Keyboard Enter passes
  // undefined; the parent should read the latest input value
  // from its own state.
  onEnter?: (value?: string) => void;
}) {
  const [opts, setOpts] = useState<string[]>([]);
  const [total, setTotal] = useState(0);
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  // Remembers what the user typed character-by-character. When
  // onChange fires with a value that jumps to an exact match of
  // a known option, we infer it came from a datalist click
  // (browsers fire an input event with the full option value,
  // no dedicated `select` event). Lets us auto-commit on pick
  // without re-firing on every keystroke through a name like
  // "orders" that's a prefix of "orders-api".
  const lastValueRef = useRef(value);
  // Holds the freshest options list so the click-detection
  // logic sees current names even when the state hasn't
  // re-rendered yet (the useEffect that updates opts runs
  // after the synchronous onChange).
  const optsRef = useRef<string[]>([]);

  // Debounced server fetch keyed off the typed value. Empty value → load
  // top-200 (alphabetical). Updates the datalist options so the browser's
  // native dropdown reflects whatever the user is filtering for.
  useEffect(() => {
    if (debounceRef.current) clearTimeout(debounceRef.current);
    debounceRef.current = setTimeout(() => {
      api.serviceNames(value, 200)
        .then(r => {
          setOpts(r.names);
          optsRef.current = r.names;
          setTotal(r.total);
        })
        .catch(() => { setOpts([]); optsRef.current = []; setTotal(0); });
    }, 180);
    return () => { if (debounceRef.current) clearTimeout(debounceRef.current); };
  }, [value]);

  const handleChange = (next: string) => {
    const prev = lastValueRef.current;
    lastValueRef.current = next;
    onChange(next);
    // Datalist-pick heuristic: a single onChange event jumped
    // the value to an exact match of a known option AND the
    // jump wasn't a single character (= not the user typing).
    // Multi-char jumps almost always come from a click on the
    // dropdown row. Schedule onEnter for the next tick so the
    // parent has applied the state from onChange first.
    if (shouldAutoCommit(prev, next, optsRef.current.includes(next)) && onEnter) {
      // Pass the picked value through so the parent can
      // commit immediately, sidestepping React's setState
      // batching (parent's draft hasn't propagated yet when
      // this fires in the next microtask).
      setTimeout(() => onEnter(next), 0);
    }
  };

  const truncated = total > opts.length;

  return (
    // v0.9.1024 — native <datalist> → ev Combobox'ı. Sunucu arama
    // katmanı (debounce + /api/service-names + kesinti sayacı) AYNEN
    // duruyor; değişen yalnız açılır listeyi KİMİN çizdiği.
    // `serverFiltered` şart: `opts` zaten `value` için sunucudan gelen
    // cevap ve joker karakterleri (`pay*`) destekliyor — istemci
    // tarafında bir kez daha süzülseydi joker sorgularda liste
    // BOŞALIRDI (alt-dize süzgeci "pay*" dizesini option'ların içinde
    // arar).
    <Combobox
      value={value}
      onChange={handleChange}
      options={opts}
      serverFiltered
      placeholder={placeholder}
      width={width}
      onEnter={() => onEnter?.(undefined)}
      // v0.9.951 (E5/Ö31) — `/` kısayolunun AÇIK hedefi. İşaret
      // olmadan GlobalShortcuts "ilk görünür metin kutusu"na düşüyor
      // ve /traces'te o kutu "Trace ID…" — yani `/` basan operatör
      // servis aramak isterken trace-id kutusuna düşüyordu.
      // Mekanizma v0.5.454'te tam bu vaka için yazılmıştı ama işaret
      // hiçbir sayfaya konmamıştı.
      shortcutSearch={shortcutSearch}
      footer={truncated
        ? `… +${total - opts.length} more — refine search`
        : undefined}
      title={
        truncated
          ? `Showing ${opts.length} of ${total} services — type to refine. Wildcards: pay*, *pay*, p?y`
          : 'Type to filter. Wildcards: pay*, *pay*, p?y'
      }
    />
  );
}
