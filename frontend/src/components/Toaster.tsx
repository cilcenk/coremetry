import { useEffect, useState } from 'react';
import { subscribeToasts, type ToastEntry } from '@/lib/toast';

// Toaster (v0.5.455) — singleton render surface for toast
// notifications. Mounted once at AppShell so toast.X() from
// anywhere in the app lands here without wiring per-page.
//
// Positioning: top-right, fixed, z-index above all other
// overlays (Modal, CommandPalette, TimeRangePicker dialog).
// Stack grows downward with newest at top.
//
// Dismissal: auto after entry.durationMs, OR click-to-dismiss.
// The auto-timer uses window.setTimeout — if the entry has
// already been removed (e.g. operator clicked), the second
// filter pass is a no-op.

export function Toaster() {
  const [entries, setEntries] = useState<ToastEntry[]>([]);

  useEffect(() => {
    return subscribeToasts(t => {
      setEntries(es => [t, ...es]);
      window.setTimeout(() => {
        setEntries(es => es.filter(x => x.id !== t.id));
      }, t.durationMs);
    });
  }, []);

  const dismiss = (id: string) => {
    setEntries(es => es.filter(x => x.id !== id));
  };

  if (entries.length === 0) return null;

  return (
    // aria-live="polite" so screen readers announce the toast
    // without interrupting the current focus. role="status"
    // is the right semantic role for non-critical updates.
    <div className="toaster" role="status" aria-live="polite">
      {entries.map(e => (
        <div key={e.id} className={`toast toast-${e.kind}`}
          onClick={() => dismiss(e.id)}
          title="Click to dismiss">
          <span className="toast-icon" aria-hidden>
            {e.kind === 'success' ? '✓' : e.kind === 'error' ? '✕' : e.kind === 'warning' ? '⚠' : 'ℹ'}
          </span>
          <span className="toast-msg">{e.message}</span>
        </div>
      ))}
    </div>
  );
}
