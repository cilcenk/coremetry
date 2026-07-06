// Toast notification primitive (v0.5.455). Single global emitter,
// decoupled from React's tree. Any caller — components, lib
// utilities, fetch handlers — can fire toast.success/error/info;
// the Toaster component (mounted once at AppShell) subscribes and
// renders.
//
// Why module-level pubsub instead of React Context: a Context
// requires the firer to be inside the provider tree, which rules
// out non-component utilities (api client, action handlers,
// useEffect callbacks that close over stale providers). The
// module-level Set serializes calls naturally and adds no
// provider wrapping to the shell.
//
// Re-firing semantics: every emit creates a new ToastEntry with
// a monotonically-increasing id. Subscribers are responsible for
// scheduling their own dismissal — see Toaster.tsx. This skill
// lib doesn't own timers, so server-render / test setups can
// snapshot listener calls deterministically.

export type ToastKind = 'success' | 'error' | 'info' | 'warning';

export interface ToastEntry {
  id: string;
  kind: ToastKind;
  message: string;
  durationMs: number;
}

type Listener = (t: ToastEntry) => void;

const listeners = new Set<Listener>();
let nextId = 1;

function emit(kind: ToastKind, message: string, durationMs: number): void {
  // Empty messages would render a blank chip — guard against it
  // upstream rather than spending pixels on it.
  if (!message) return;
  const entry: ToastEntry = {
    id: String(nextId++),
    kind,
    message,
    durationMs,
  };
  // Iterate over a snapshot — a listener that unsubscribes itself
  // (e.g. on unmount during render) mustn't break the loop.
  for (const l of Array.from(listeners)) {
    try { l(entry); } catch { /* one listener throwing must not break others */ }
  }
}

export const toast = {
  // Success — green chip, 3s dwell. Use for confirmations the
  // operator is expected to acknowledge passively ("Saved view
  // X", "Acknowledged problem-1234").
  success: (message: string, durationMs = 3000) =>
    emit('success', message, durationMs),
  // Error — red chip, 6s dwell so the operator can read the
  // failure string. Include the underlying error.message; the
  // API client formats those as `HTTP 4xx: <body>` which is
  // already operator-readable.
  error: (message: string, durationMs = 6000) =>
    emit('error', message, durationMs),
  // Info — blue chip, 3s. Neutral notices that don't need
  // celebration or alarm ("Switched to dark mode").
  info: (message: string, durationMs = 3000) =>
    emit('info', message, durationMs),
  // Warning — gold chip, 5s (v0.8.303, quality bar U4). For
  // degraded-but-working outcomes ("saved, but the SMTP test
  // failed") that aren't errors yet shouldn't read as success.
  warning: (message: string, durationMs = 5000) =>
    emit('warning', message, durationMs),
};

export function subscribeToasts(fn: Listener): () => void {
  listeners.add(fn);
  return () => { listeners.delete(fn); };
}
