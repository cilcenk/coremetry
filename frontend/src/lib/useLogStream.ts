import { useEffect, useState } from 'react';
import type { LogRow } from '@/lib/types';

// useLogStream live-tails /logs over SSE (v0.7.15), replacing the old 2s React
// Query poll. The server runs ONE backend query per filter and fans new rows
// out to every subscriber (pod-local tailer), so the browser holds an idle
// stream instead of driving ~30 CH/ES queries/min — the ≥10s polling budget no
// longer applies. Auth rides the cookie (withCredentials), same as the
// /api/events stream. Pauses on document.hidden (closes the stream, reopens on
// visible) so a backgrounded tab costs nothing.
//
// Rows arrive newest-last from the server; we prepend into a capped buffer so
// the newest line sits at the top, matching the static /logs ordering.
const MAX_LIVE_ROWS = 1000;

export interface LogStreamFilter {
  service?: string;
  cluster?: string;
  search?: string;
  severity?: number;
}

export function useLogStream(
  enabled: boolean,
  filter: LogStreamFilter,
): { rows: LogRow[]; connected: boolean } {
  const [rows, setRows] = useState<LogRow[]>([]);
  const [connected, setConnected] = useState(false);

  // Only re-subscribe when a filter dimension actually changes (the object
  // identity changes every render).
  const key = [filter.service, filter.cluster, filter.search, filter.severity]
    .map(v => v ?? '').join('\x1f');

  useEffect(() => {
    if (!enabled || typeof EventSource === 'undefined') {
      setRows([]);
      setConnected(false);
      return;
    }
    let es: EventSource | null = null;

    const open = () => {
      if (es || document.hidden) return;
      const qs = new URLSearchParams();
      if (filter.service)  qs.set('service', filter.service);
      if (filter.cluster)  qs.set('cluster', filter.cluster);
      if (filter.search)   qs.set('search', filter.search);
      if (filter.severity) qs.set('severity', String(filter.severity));
      es = new EventSource(`/api/logs/stream?${qs.toString()}`, { withCredentials: true });
      es.addEventListener('open', () => setConnected(true));
      es.addEventListener('error', () => setConnected(false)); // EventSource auto-reconnects
      es.addEventListener('log', (e: MessageEvent) => {
        try {
          const row = JSON.parse(e.data) as LogRow;
          setRows(prev => {
            const next = [row, ...prev];
            return next.length > MAX_LIVE_ROWS ? next.slice(0, MAX_LIVE_ROWS) : next;
          });
        } catch {
          /* skip a malformed frame */
        }
      });
    };
    const close = () => {
      es?.close();
      es = null;
      setConnected(false);
    };

    // Pause on tab-hide (document.hidden polling rule — no idle traffic).
    const onVis = () => { if (document.hidden) close(); else open(); };
    document.addEventListener('visibilitychange', onVis);

    setRows([]); // fresh buffer on (re)subscribe
    open();
    return () => {
      document.removeEventListener('visibilitychange', onVis);
      close();
    };
    // filter.* are read through the stable `key`; depending on the object would
    // re-subscribe every render.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [enabled, key]);

  return { rows, connected };
}
