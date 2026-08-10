// Lightweight keyboard-shortcut subsystem. No external lib —
// the surface is a global registry + a single document-level
// keydown listener that dispatches by key combination.
//
// Why home-grown? react-hotkeys / mousetrap / hotkeys-js all
// bundle 8-20 KB and bring opinionated context-management
// (HotkeysProvider, scopes, sequences) we don't need. The
// implementation here is ~150 lines and covers the four
// patterns the app actually uses:
//
//   • Single-key shortcuts (?, /)
//   • Combo shortcuts (Ctrl+K, Cmd+K)
//   • Two-key sequences (g s, g t, g l) — Vim/Datadog style
//   • Per-page registrations via useShortcuts() hook
//
// Two ergonomic invariants that all callers can rely on:
//
//   1. Shortcuts fire ONLY when no input/textarea/contentEditable
//      is focused. Typing "g" into a search box never warps
//      the page. The escape hatch (`evenInInputs: true`) is
//      explicit per binding for the few shortcuts that should
//      work everywhere (Esc, ?).
//
//   2. The latest registration of a given key wins. Page-level
//      bindings stack on top of global ones, removed cleanly
//      on unmount via the returned cleanup function.

import { useEffect, useRef } from 'react';
import { getActiveNavScope, noteInteraction, pickOwner } from './navScope';

export interface Shortcut {
  // Combo — single key like '?', '/', 'k', or modifier-prefixed
  // like 'mod+k' (mod = Cmd on mac, Ctrl elsewhere). Two-key
  // sequences like 'g s' fire when 'g' then 's' are pressed
  // within the sequence window (1.2s default).
  keys: string;
  // Human-readable label for the help modal. Required so the
  // help screen can list every binding.
  label: string;
  // Optional grouping for the help modal. 'Navigation', 'Lists',
  // 'Editor' etc. so the help screen organises the bindings.
  group?: string;
  // Handler. Receives the original event so the caller can
  // call preventDefault() if they want the browser default
  // suppressed (e.g. '/' to focus search overrides the
  // browser's quick-find).
  handler: (e: KeyboardEvent) => void;
  // When true the binding fires even if an input/textarea is
  // focused. Default false — prevents typing-eats-shortcut
  // bugs.
  evenInInputs?: boolean;
  // v0.9.928 — sahiplenme kapsamı. Aynı tuşa birden fazla yüzey
  // kaydolduğunda (iki gezinilebilir tablo aynı ekranda) kazananı
  // SON ETKİLEŞİM belirler; `scope` o etkileşimin damgasıyla
  // (`data-table-id`) eşleşen değerdir. Kapsamsız binding'ler
  // (global 'g s', '?', 'mod+k') arbitrajın dışında: onlar için
  // yığının tepesi kuralı aynen sürüyor.
  scope?: string;
}

// v0.9.926 — kayıt defteri YIĞIN tutuyor, tek değer değil.
//
// Öncesinde `Map<string, Shortcut>`ti ve `registry.set(sc.keys, sc)`
// ikinci kaydı birincinin ÜZERİNE yazıyordu. İki nav'lı tablo aynı anda
// mount olduğunda (ör. bir sayfada hem liste hem yan tablo) j/k'nın
// hangisine gideceği MOUNT SIRASINA kalıyordu — ve daha kötüsü, ikinci
// tablo unmount olduğunda kaldırma koruması (`registry.get === sc`)
// tuttuğu için binding TAMAMEN siliniyordu: geriye kalan tablonun
// klavye gezinmesi ÖLÜYORDU ve bir daha geri gelmiyordu.
//
// Yığınla: en son kaydolan sahip (bugünkü davranış, artık AÇIKÇA
// yazılı), ama unmount olduğunda bir öncekine DÜŞÜYOR. Kimse sessizce
// kaybolmuyor.
//
// v0.9.928 — o gün AÇIK BIRAKILAN ürün kararı (iki tablo aynı anda
// GÖRÜNÜRken j/k hangisine gider?) operatör tarafından cevaplandı:
// **SON ETKİLEŞİM** alan tablo, son mount olan DEĞİL. Arbitraj
// `lib/navScope.ts`te saf bir fonksiyon (`pickOwner`); burası yalnız
// onu çağırır ve etkileşimleri besler.
type Registry = Map<string, Shortcut[]>;

const registry: Registry = new Map();

/**
 * Bir tuş için ŞU AN sahip olan binding: aktif kapsama ait kayıt varsa o,
 * yoksa yığının tepesi (v0.9.928 arbitrajı — bkz. lib/navScope.ts).
 */
function owner(keys: string): Shortcut | undefined {
  const stack = registry.get(keys);
  if (!stack) return undefined;
  return pickOwner(stack, getActiveNavScope());
}
let listenerInstalled = false;
let pendingPrefix: string | null = null;
let prefixTimer: number | null = null;
const SEQUENCE_WINDOW_MS = 1200;

function isEditableTarget(t: EventTarget | null): boolean {
  if (!(t instanceof HTMLElement)) return false;
  const tag = t.tagName;
  if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return true;
  if (t.isContentEditable) return true;
  return false;
}

// isActivationTarget — v0.9.860 sınıfı, v0.9.863 (UX denetimi K13).
//
// Bug: /services, /traces, /inbox, /logs gibi klavye-nav'lı her sayfada Tab
// ile bir butona gelip Enter'a basınca HİÇBİR ŞEY olmuyordu; j/k ile bir satır
// seçiliyse Enter butonu değil SATIRI açıyordu. Klavyeyle aksiyon akışı bu
// sayfalarda fiilen kırıktı.
//
// Kök neden: useTableNav Enter'ı GLOBAL registry'e kaydediyor ve dispatch
// eşleşen her kısayolda KOŞULSUZ preventDefault() çağırıyor. Tarayıcı,
// odaklı bir butonda Enter'ı ancak varsayılan davranış iptal edilmezse click'e
// çevirir — yani sayfanın satır kısayolu, butonun kendi aktivasyonunu yutuyordu.
//
// Neden isEditableTarget'a BUTTON eklemek DEĞİL: o bayrak TÜM kısayolları
// kapatır. Bir toolbar butonuna tıkladıktan sonra odak o butonda kalır; blanket
// muafiyet j/k gezinmesini o andan itibaren öldürürdü — bir bug'ı daha
// sinsi bir bug'la değişmek olurdu. Susturulan yalnızca AKTİVASYON tuşları,
// yalnızca aktive edilebilir bir eleman odaktayken.
const ACTIVATION_KEYS = new Set(['Enter', ' ']);

export function isActivationTarget(t: EventTarget | null, combo: string): boolean {
  if (!ACTIVATION_KEYS.has(combo)) return false;
  if (!(t instanceof HTMLElement)) return false;
  const el = t.closest('button, a[href], [role="button"], summary');
  if (!el) return false;
  // Space bir LİNK'i aktive etmez (sayfayı kaydırır), o yüzden bir kısayolu
  // yalnız link odaklı diye susturmak kullanıcıdan bir şey çalmak olurdu.
  if (combo === ' ' && el.tagName === 'A') return false;
  // Devre dışı buton hiçbir şey aktive etmez — kısayol çalışsın.
  if (el instanceof HTMLButtonElement && el.disabled) return false;
  return true;
}

// Normalise a KeyboardEvent into the same string format we
// register: 'mod+k' / 'g s' / 'a'. Multi-key sequences are
// resolved via pendingPrefix, not here.
export function comboFromEvent(e: KeyboardEvent): string {
  const parts: string[] = [];
  // mod = Cmd on Mac, Ctrl elsewhere — matches what Datadog
  // and most editors use.
  if (e.metaKey || e.ctrlKey) parts.push('mod');
  if (e.altKey) parts.push('alt');
  // e.key can be undefined on synthetic / programmatic KeyboardEvents
  // (password managers, IME composition, autofill dispatch). Normalise to ''
  // so .length / .toLowerCase() never throw (v0.8.x crash fix).
  const key = e.key ?? '';
  // v0.9.949 (UX denetimi E1 / Ö27) — Shift KATLANMASI yalnız A-Z'de.
  //
  // Eski kural (`key.length > 1`) TÜM tek karakterlerde shift'i yutuyordu.
  // Sonuç: Shift+G → 'g' üretiyordu, yani `G` (son satıra atla) HİÇBİR
  // listede çalışmıyordu — üstelik 'g' iki-tuşlu dizi ÖNEKİ olduğu için
  // Shift+G'den sonra 1.2 sn içinde `s` basan kendini /services'te
  // buluyordu. Yardım ekranı ise "Jump to last row (G)" diyordu.
  //
  // NAİF DÜZELTME (`if (e.shiftKey)`) `?` KISAYOLUNU ÖLDÜRÜR: Shift+/ →
  // e.key '?' gelir ve kural onu 'shift+?' yapardı; yardım modalının
  // kısayolu sessizce kaybolurdu. Noktalama zaten SHIFT'İN ÜRÜNÜ — orada
  // shift bilgisi tuşun kendisindedir, ikinci kez kodlanamaz.
  //
  // Harfler ise farklı: 'A' ile 'a' AYNI tuş ve tek fark shift. Kural
  // tam olarak bu kümeye daraltıldı.
  const shiftFolds = key.length > 1 || /^[A-Za-z]$/.test(key);
  if (e.shiftKey && shiftFolds) parts.push('shift');
  // Use lowercase letter for printable keys; dedicated names
  // for special keys.
  const k = key.length === 1 ? key.toLowerCase() : key;
  parts.push(k);
  return parts.join('+');
}

function installListener(): void {
  if (listenerInstalled) return;
  listenerInstalled = true;
  // v0.9.928 — etkileşim takibi. Tık ve odak YAKALAMA fazında dinleniyor:
  // bir satırın kendi onClick'i stopPropagation çağırsa bile (ProblemsSection'ın
  // hücreleri tam olarak bunu yapıyor) sahiplik yine de güncellensin.
  document.addEventListener('pointerdown', (e) => { noteInteraction(e.target); }, true);
  document.addEventListener('focusin', (e) => { noteInteraction(e.target); }, true);
  document.addEventListener('keydown', (e) => {
    // Klavye de bir etkileşim: Tab ile bir tablonun satırına gelip Enter'a
    // basmak o tabloyu sahiplendirir. Hedef hiçbir kapsamın içinde değilse
    // (j/k belgeye gelir, target = body) sahiplik DEĞİŞMEZ — yapışkanlık.
    noteInteraction(e.target);
    const inEditable = isEditableTarget(e.target);

    // Two-key sequence path. If we have a pending prefix, the
    // current event might complete a registered sequence. Only
    // letters/numbers participate — modifier presses don't
    // reset the prefix.
    if (pendingPrefix && e.key && e.key.length === 1 && !e.metaKey && !e.ctrlKey && !e.altKey) {
      const seq = `${pendingPrefix} ${e.key.toLowerCase()}`;
      const sc = owner(seq);
      if (sc && (!inEditable || sc.evenInInputs)) {
        e.preventDefault();
        sc.handler(e);
        clearPrefix();
        return;
      }
      // Sequence didn't resolve — cancel the prefix so the
      // user isn't trapped in an infinite "still waiting"
      // state.
      clearPrefix();
    }

    const combo = comboFromEvent(e);

    // Two-key sequence start? Only when no modifiers are held
    // and no editable target is focused.
    if (!e.metaKey && !e.ctrlKey && !e.altKey && !inEditable && combo === 'g') {
      // Probe whether ANY 'g <x>' binding exists; only set the
      // prefix if so, to avoid stealing 'g' from someone who
      // wants to type it.
      for (const k of registry.keys()) {
        if (k.startsWith('g ')) {
          pendingPrefix = 'g';
          prefixTimer = window.setTimeout(clearPrefix, SEQUENCE_WINDOW_MS);
          // Don't return — let the singular-'g' binding (if
          // any) also fire. None exists today, but we keep the
          // semantics open.
          break;
        }
      }
    }

    // Single-combo path.
    const sc = owner(combo);
    // v0.9.863 (UX denetimi K13) — odaklı buton/link kendi aktivasyon tuşunu
    // GERİ ALIR: aksi hâlde preventDefault() tarayıcının click sentezini iptal
    // ediyor ve Enter hiçbir şey yapmıyordu.
    if (sc && (!inEditable || sc.evenInInputs) && !isActivationTarget(e.target, combo)) {
      e.preventDefault();
      sc.handler(e);
    }
  });
}

function clearPrefix(): void {
  pendingPrefix = null;
  if (prefixTimer !== null) {
    clearTimeout(prefixTimer);
    prefixTimer = null;
  }
}

// useShortcuts registers a list of shortcuts for the lifetime
// of the calling component. Return value is unused — the hook
// installs/removes listeners via the registry on mount/unmount.
//
// The deps argument lets the caller control re-registration
// — e.g. when a binding's handler closes over component state
// that changes. Default behaviour mirrors useEffect: empty
// deps = register once.
export function useShortcuts(shortcuts: Shortcut[], deps: unknown[] = []): void {
  const stableRef = useRef(shortcuts);
  stableRef.current = shortcuts;

  useEffect(() => {
    installListener();
    for (const sc of shortcuts) {
      const stack = registry.get(sc.keys) ?? [];
      stack.push(sc);
      registry.set(sc.keys, stack);
    }
    return () => {
      for (const sc of shortcuts) {
        // KENDİ kaydımızı çıkarıyoruz, nerede olursa olsun. Eskiden
        // "yalnız tepedeysek sil" idi; o kural, tepedeyken silince
        // ALTTAKİNİ de yok ediyordu (Map tek değer tutuyordu).
        const stack = registry.get(sc.keys);
        if (!stack) continue;
        const i = stack.lastIndexOf(sc);
        if (i >= 0) stack.splice(i, 1);
        if (stack.length === 0) registry.delete(sc.keys);
      }
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps);
}

// listShortcuts returns the current global registry — used
// by the Help modal so the shortcut list there always
// reflects what's actually bound right now.
export function listShortcuts(): Shortcut[] {
  // Yardım ekranı SAHİP olan binding'leri listeliyor — altta kalan
  // gölgelenmiş kayıtlar gösterilirse operatöre çalışmayan bir kısayol
  // vaat edilmiş olur. v0.9.928: sahiplik artık arbitrajın cevabı, o
  // yüzden yardım ekranı da `pickOwner`dan geçiyor — aksi hâlde iki
  // tablolu bir sayfada yardım listesi j/k'yı YANLIŞ tabloya atfederdi.
  const active = getActiveNavScope();
  return Array.from(registry.values())
    .map(stack => pickOwner(stack, active))
    .filter((sc): sc is Shortcut => !!sc);
}
