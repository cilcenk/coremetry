import type { Problem, NotificationLogEntry } from './types';

// watcherTimeline — pure builders for the /watchers history drawer
// (v0.9.196). The API hands back one rule's problems + their
// notification rows; the drawer renders ONE merged timeline
// (fire → notifications → resolve, newest first) plus a 24h summary
// strip. Kept out of the page component so the merge/ordering rules
// are vitest-covered without a DOM.

export type WatcherTimelineEntry =
  | { kind: 'fire'; ts: number; problem: Problem }
  | { kind: 'resolve'; ts: number; problem: Problem; openForSec: number }
  | { kind: 'notify'; ts: number; entry: NotificationLogEntry };

// Rank breaks same-nanosecond ties so the visual order stays causal
// when sorted newest-first: a resolve reads above the notifications
// it followed, which read above the fire that triggered them.
const RANK: Record<WatcherTimelineEntry['kind'], number> = {
  fire: 0,
  notify: 1,
  resolve: 2,
};

// buildWatcherTimeline merges problems (fire = startedAt, resolve =
// resolvedAt when present) with the notification rows into one list
// sorted newest-first. Open problems contribute only their fire entry.
export function buildWatcherTimeline(
  problems: Problem[],
  notifications: NotificationLogEntry[],
): WatcherTimelineEntry[] {
  const out: WatcherTimelineEntry[] = [];
  for (const p of problems) {
    out.push({ kind: 'fire', ts: p.startedAt, problem: p });
    if (p.resolvedAt) {
      out.push({
        kind: 'resolve',
        ts: p.resolvedAt,
        problem: p,
        openForSec: Math.max(0, (p.resolvedAt - p.startedAt) / 1e9),
      });
    }
  }
  for (const n of notifications) {
    out.push({ kind: 'notify', ts: n.sentAt, entry: n });
  }
  out.sort((a, b) => (b.ts - a.ts) || (RANK[b.kind] - RANK[a.kind]));
  return out;
}

// summarizeWatcherHistory — the drawer's header strip numbers over
// the trailing 24h window: fires, notifications sent, failed sends.
// lastFire is unconstrained by the window (0 = never fired).
export interface WatcherHistorySummary {
  fires24h: number;
  notifs24h: number;
  notifFails24h: number;
  lastFire: number; // unix ns; 0 = no fire in the history slice
}

export function summarizeWatcherHistory(
  problems: Problem[],
  notifications: NotificationLogEntry[],
  nowNs: number,
): WatcherHistorySummary {
  const cutoff = nowNs - 24 * 3600 * 1e9;
  let fires24h = 0;
  let lastFire = 0;
  for (const p of problems) {
    if (p.startedAt >= cutoff) fires24h++;
    if (p.startedAt > lastFire) lastFire = p.startedAt;
  }
  let notifs24h = 0;
  let notifFails24h = 0;
  for (const n of notifications) {
    if (n.sentAt < cutoff) continue;
    notifs24h++;
    if (!n.ok) notifFails24h++;
  }
  return { fires24h, notifs24h, notifFails24h, lastFire };
}
