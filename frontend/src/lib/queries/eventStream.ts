import { useEffect } from 'react';
import { useQueryClient, type QueryClient } from '@tanstack/react-query';
import { eventInvalidations, catchupInvalidations, type EventKind } from './eventInvalidations';

// useEventStream — live-update bus subscriber (v0.8.529 single-SSE).
//
// THE PROBLEM (operator-reported): each browser window opened its own
// EventSource to /api/events. A browser caps HTTP/1.1 connections at ~6
// PER HOST, shared across ALL tabs/windows of the origin. So N open
// windows held N permanent SSE connections; at ~6 windows every slot was
// consumed by idle-but-open streams and ordinary API requests queued
// behind them → severe slowness with multiple windows, fine with one.
// (Monolithic mode = one pod, so session stickiness was NEVER the cause.)
//
// THE FIX: exactly ONE tab (the leader) holds the EventSource for the
// whole origin. It fans events out to sibling tabs over a BroadcastChannel;
// followers hold NO SSE connection. N windows → 1 connection total, so SSE
// no longer eats the HTTP/1.1 connection budget regardless of window
// count. Leadership uses the Web Locks API: the lock is auto-released when
// the leader tab closes, and a waiting tab immediately acquires it and
// becomes the new leader (seamless handoff).
//
// Degrades safely: if Web Locks or BroadcastChannel are unavailable (old
// browser), every tab opens its own EventSource — the pre-v0.8.529
// behaviour. Correct everywhere, just without the connection saving.
//
// Mount once at the app shell level.

const LEADER_LOCK = 'coremetry-sse-leader';
const CHANNEL = 'coremetry-events';

function applyInvalidations(qc: QueryClient, kind: EventKind | string) {
  for (const key of eventInvalidations(kind)) {
    qc.invalidateQueries({ queryKey: key });
  }
}
function applyCatchup(qc: QueryClient) {
  for (const key of catchupInvalidations()) {
    qc.invalidateQueries({ queryKey: key });
  }
}

const EVENT_KINDS: EventKind[] = ['problem.open', 'problem.resolve', 'anomaly.open', 'anomaly.clear'];

export function useEventStream(enabled: boolean) {
  const qc = useQueryClient();

  useEffect(() => {
    if (!enabled || typeof EventSource === 'undefined') return;

    const hasLocks = typeof navigator !== 'undefined'
      && !!navigator.locks && typeof navigator.locks.request === 'function';
    const hasChannel = typeof BroadcastChannel !== 'undefined';

    // ── Fallback: no coordination primitives → per-tab EventSource
    // (the pre-v0.8.529 behaviour, unchanged). ──────────────────────────
    if (!hasLocks || !hasChannel) {
      return openOwnStream(qc);
    }

    // ── Coordinated single-SSE path. ────────────────────────────────────
    const bc = new BroadcastChannel(CHANNEL);
    // Followers (and the leader's own siblings) apply invalidations from
    // the broadcast. The leader also posts to this same channel; a channel
    // does NOT echo to the posting context, so the leader never
    // double-applies.
    bc.onmessage = (e: MessageEvent<{ kind: string }>) => {
      if (e.data?.kind) applyInvalidations(qc, e.data.kind);
    };

    const ac = new AbortController();
    let closeStream: (() => void) | null = null;
    let releaseLeadership: (() => void) | null = null;

    // Acquire leadership. The callback runs ONLY for the tab that holds
    // the lock; it returns a promise we resolve on cleanup, holding the
    // lock (and thus leadership) for this tab's lifetime. When resolved
    // — or when the tab closes — the lock frees and a waiting tab's
    // request resolves, electing the next leader.
    navigator.locks.request(LEADER_LOCK, { signal: ac.signal }, () => {
      closeStream = openLeaderStream(qc, bc);
      return new Promise<void>((resolve) => { releaseLeadership = resolve; });
    }).catch(() => {
      // AbortError when we cancel a still-pending (follower) request on
      // cleanup — expected, not an error.
    });

    return () => {
      closeStream?.();          // leader: stop the EventSource
      releaseLeadership?.();    // leader: return from callback → free lock → re-elect
      ac.abort();               // follower: cancel the pending lock request
      bc.close();
    };
  }, [enabled, qc]);
}

// openLeaderStream opens the single EventSource and, per event, applies
// invalidations locally AND broadcasts the kind to sibling tabs. Returns
// a closer. On (re)connect it runs the catch-up refetch: the first open
// after becoming leader may follow a handoff gap where the previous
// leader's stream was briefly down.
function openLeaderStream(qc: QueryClient, bc: BroadcastChannel): () => void {
  const es = new EventSource('/api/events', { withCredentials: true });
  let connectedOnce = false;

  const onOpen = () => {
    // First open right after winning leadership: catch up in case events
    // fired during the (usually sub-ms) handoff from the previous leader.
    // Subsequent opens are auto-reconnects — same catch-up need.
    if (connectedOnce) applyCatchup(qc);
    else { applyCatchup(qc); connectedOnce = true; }
  };
  const handlers = EVENT_KINDS.map((kind) => {
    const h = () => { applyInvalidations(qc, kind); bc.postMessage({ kind }); };
    es.addEventListener(kind, h);
    return [kind, h] as const;
  });
  es.addEventListener('open', onOpen);
  es.addEventListener('error', () => { /* auto-reconnect; catch-up rides onOpen */ });

  return () => {
    es.removeEventListener('open', onOpen);
    for (const [kind, h] of handlers) es.removeEventListener(kind, h);
    es.close();
  };
}

// openOwnStream — the degraded per-tab path (no Web Locks / BroadcastChannel).
// Byte-for-byte the pre-v0.8.529 semantics: own EventSource, own catch-up.
function openOwnStream(qc: QueryClient): () => void {
  const es = new EventSource('/api/events', { withCredentials: true });
  let connectedOnce = false;
  const onOpen = () => { if (connectedOnce) applyCatchup(qc); connectedOnce = true; };
  const handlers = EVENT_KINDS.map((kind) => {
    const h = () => applyInvalidations(qc, kind);
    es.addEventListener(kind, h);
    return [kind, h] as const;
  });
  es.addEventListener('open', onOpen);
  es.addEventListener('error', () => {});
  return () => {
    es.removeEventListener('open', onOpen);
    for (const [kind, h] of handlers) es.removeEventListener(kind, h);
    es.close();
  };
}
