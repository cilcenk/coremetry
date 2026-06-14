import { useEffect } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { keys } from './keys';

// useEventStream subscribes to /api/events (SSE) and invalidates
// the relevant React Query caches when events arrive. Mount once,
// at the app shell level — duplicate mounts would open duplicate
// connections, which is wasteful (one bus broadcasts to all
// browser tabs anyway, but each EventSource is a separate
// long-lived TCP/HTTP/2 stream).
//
// Auto-reconnect: native EventSource reopens the socket on a drop
// (onerror → onopen). But the bus is LIVE — it has no replay, so any
// problem.open / anomaly.fire that landed while we were disconnected
// (e.g. the api pod serving this stream rolled during a deploy) is
// missed. EventSource reconnecting does NOT refetch React Query, and
// these queries no longer poll (SSE replaced the 30s loop), so on every
// RE-connect we catch up by invalidating exactly what the event handlers
// invalidate. The first onopen is the initial connect (data already
// fresh from mount) — skip it via connectedOnce.
//
// Disable when running tests / SSR (typeof EventSource ===
// 'undefined') so this hook is safe to import unconditionally.
export function useEventStream(enabled: boolean) {
  const qc = useQueryClient();

  useEffect(() => {
    if (!enabled || typeof EventSource === 'undefined') return;

    const es = new EventSource('/api/events', { withCredentials: true });
    let connectedOnce = false;

    // Listener function: route by `event:` line, not by JSON
    // parsing every payload. The Go side emits events with
    // explicit `event: <kind>` headers; EventSource fires the
    // matching listener.
    const onProblemOpen    = () => {
      qc.invalidateQueries({ queryKey: keys.problems.all });
      qc.invalidateQueries({ queryKey: keys.anomalies.metrics });
      qc.invalidateQueries({ queryKey: keys.incidents.all });
    };
    const onProblemResolve = () => {
      qc.invalidateQueries({ queryKey: keys.problems.all });
      qc.invalidateQueries({ queryKey: keys.anomalies.metrics });
      qc.invalidateQueries({ queryKey: keys.incidents.all });
    };
    const onAnomalyOpen    = () => {
      qc.invalidateQueries({ queryKey: keys.anomalies.all });
    };
    const onAnomalyClear   = () => {
      qc.invalidateQueries({ queryKey: keys.anomalies.all });
    };

    // On every RE-open, refetch the live queries so events missed during the
    // disconnect (deploy pod-churn, network blip) land. First open = initial
    // connect, data already fresh → skip.
    const onOpen = () => {
      if (connectedOnce) {
        qc.invalidateQueries({ queryKey: keys.problems.all });
        qc.invalidateQueries({ queryKey: keys.anomalies.all });
        qc.invalidateQueries({ queryKey: keys.anomalies.metrics });
        qc.invalidateQueries({ queryKey: keys.incidents.all });
      }
      connectedOnce = true;
    };

    es.addEventListener('open',            onOpen);
    es.addEventListener('problem.open',    onProblemOpen);
    es.addEventListener('problem.resolve', onProblemResolve);
    es.addEventListener('anomaly.open',    onAnomalyOpen);
    es.addEventListener('anomaly.clear',   onAnomalyClear);

    es.addEventListener('error', () => {
      // EventSource will auto-reconnect; the catch-up refetch rides the
      // subsequent onOpen. Logging on every reconnect attempt is noisy on a
      // backend restart, so we just stay silent.
    });

    return () => {
      es.removeEventListener('open',            onOpen);
      es.removeEventListener('problem.open',    onProblemOpen);
      es.removeEventListener('problem.resolve', onProblemResolve);
      es.removeEventListener('anomaly.open',    onAnomalyOpen);
      es.removeEventListener('anomaly.clear',   onAnomalyClear);
      es.close();
    };
  }, [enabled, qc]);
}
