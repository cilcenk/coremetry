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
}

type Registry = Map<string, Shortcut>;

const registry: Registry = new Map();
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
  if (e.shiftKey && key.length > 1) parts.push('shift');
  // Use lowercase letter for printable keys; dedicated names
  // for special keys.
  const k = key.length === 1 ? key.toLowerCase() : key;
  parts.push(k);
  return parts.join('+');
}

function installListener(): void {
  if (listenerInstalled) return;
  listenerInstalled = true;
  document.addEventListener('keydown', (e) => {
    const inEditable = isEditableTarget(e.target);

    // Two-key sequence path. If we have a pending prefix, the
    // current event might complete a registered sequence. Only
    // letters/numbers participate — modifier presses don't
    // reset the prefix.
    if (pendingPrefix && e.key && e.key.length === 1 && !e.metaKey && !e.ctrlKey && !e.altKey) {
      const seq = `${pendingPrefix} ${e.key.toLowerCase()}`;
      const sc = registry.get(seq);
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
    const sc = registry.get(combo);
    if (sc && (!inEditable || sc.evenInInputs)) {
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
      registry.set(sc.keys, sc);
    }
    return () => {
      for (const sc of shortcuts) {
        // Only remove if WE registered it — a later registration
        // overwriting ours wins on the way down too.
        if (registry.get(sc.keys) === sc) {
          registry.delete(sc.keys);
        }
      }
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps);
}

// listShortcuts returns the current global registry — used
// by the Help modal so the shortcut list there always
// reflects what's actually bound right now.
export function listShortcuts(): Shortcut[] {
  return Array.from(registry.values());
}
