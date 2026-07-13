import type { QueryKey } from '@tanstack/react-query';
import { keys } from './keys';

// eventInvalidations — v0.8.529: the pure event→queryKeys map shared by
// the SSE leader and the BroadcastChannel followers. Kept separate +
// vitest-pinned so the single-SSE refactor can't drift the two paths
// (leader applies these locally AND the same kind is broadcast to
// sibling tabs, which apply the identical set).
//
// A kind not in the map returns [] — an unknown event is a no-op, never
// a blanket refetch.
export type EventKind =
  | 'problem.open' | 'problem.resolve'
  | 'anomaly.open' | 'anomaly.clear';

export function eventInvalidations(kind: EventKind | string): QueryKey[] {
  switch (kind) {
    case 'problem.open':
    case 'problem.resolve':
      return [keys.problems.all, keys.anomalies.metrics, keys.incidents.all];
    case 'anomaly.open':
    case 'anomaly.clear':
      return [keys.anomalies.all];
    default:
      return [];
  }
}

// catchupInvalidations — the superset refetched when a (re)connect could
// have missed live events: a fresh SSE leader taking over after the
// previous leader tab closed, or EventSource auto-reconnect after a
// backend blip. Union of every event handler's keys.
export function catchupInvalidations(): QueryKey[] {
  return [
    keys.problems.all,
    keys.anomalies.all,
    keys.anomalies.metrics,
    keys.incidents.all,
  ];
}
