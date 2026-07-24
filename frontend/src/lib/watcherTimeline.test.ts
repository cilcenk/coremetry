import { describe, it, expect } from 'vitest';
import { buildWatcherTimeline, summarizeWatcherHistory } from './watcherTimeline';
import type { Problem, NotificationLogEntry } from './types';

// v0.9.196 — /watchers history drawer merge/ordering rules.

const S = 1e9; // 1s in ns

function problem(over: Partial<Problem>): Problem {
  return {
    id: 'p-1', ruleId: 'w1', ruleName: 'digital-core error spike',
    severity: 'warning', service: '', metric: 'watcher',
    value: 7, threshold: 5, status: 'open',
    description: '', startedAt: 100 * S,
    ...over,
  };
}

function notif(over: Partial<NotificationLogEntry>): NotificationLogEntry {
  return {
    id: 'nl-1', sentAt: 101 * S, channelKind: 'email', channelName: 'oncall',
    target: 'it-alerts@example.com', subject: '[WARNING] — spike',
    bodyPreview: '', relatedKind: 'watcher', relatedId: 'p-1',
    ok: true, error: '',
    ...over,
  };
}

describe('buildWatcherTimeline', () => {
  it('emits fire + resolve for a resolved problem, fire only for an open one', () => {
    const resolved = problem({ id: 'p-r', status: 'resolved', startedAt: 100 * S, resolvedAt: 400 * S });
    const open = problem({ id: 'p-o', startedAt: 500 * S });

    const tl = buildWatcherTimeline([resolved, open], []);

    expect(tl.map(e => e.kind)).toEqual(['fire', 'resolve', 'fire']);
    expect(tl[0]).toMatchObject({ kind: 'fire', ts: 500 * S });
    expect(tl[1]).toMatchObject({ kind: 'resolve', ts: 400 * S, openForSec: 300 });
    expect(tl[2]).toMatchObject({ kind: 'fire', ts: 100 * S });
  });

  it('merges notifications by timestamp, newest first', () => {
    const p = problem({ startedAt: 100 * S });
    const n1 = notif({ id: 'nl-a', sentAt: 101 * S });
    const n2 = notif({ id: 'nl-b', sentAt: 102 * S, ok: false, error: '502' });

    const tl = buildWatcherTimeline([p], [n1, n2]);

    expect(tl.map(e => e.kind)).toEqual(['notify', 'notify', 'fire']);
    expect(tl[0]).toMatchObject({ kind: 'notify', ts: 102 * S });
    expect(tl[1]).toMatchObject({ kind: 'notify', ts: 101 * S });
    expect(tl[2]).toMatchObject({ kind: 'fire', ts: 100 * S });
  });

  it('breaks same-timestamp ties causally: resolve above notify above fire', () => {
    const p = problem({ status: 'resolved', startedAt: 100 * S, resolvedAt: 100 * S });
    const n = notif({ sentAt: 100 * S });

    const tl = buildWatcherTimeline([p], [n]);

    expect(tl.map(e => e.kind)).toEqual(['resolve', 'notify', 'fire']);
  });

  it('empty inputs produce an empty timeline', () => {
    expect(buildWatcherTimeline([], [])).toEqual([]);
  });
});

describe('summarizeWatcherHistory', () => {
  const now = 1000 * 3600 * S; // arbitrary "now"
  const h = 3600 * S;

  it('counts only the trailing 24h; lastFire is unconstrained', () => {
    const problems = [
      problem({ id: 'p-old', startedAt: now - 30 * h }),  // outside 24h
      problem({ id: 'p-1', startedAt: now - 2 * h }),
      problem({ id: 'p-2', startedAt: now - 1 * h }),
    ];
    const notifications = [
      notif({ id: 'nl-old', sentAt: now - 30 * h, ok: false }), // outside — not counted anywhere
      notif({ id: 'nl-1', sentAt: now - 2 * h }),
      notif({ id: 'nl-2', sentAt: now - 1 * h, ok: false, error: '502' }),
    ];

    const s = summarizeWatcherHistory(problems, notifications, now);

    expect(s.fires24h).toBe(2);
    expect(s.notifs24h).toBe(2);
    expect(s.notifFails24h).toBe(1);
    expect(s.lastFire).toBe(now - 1 * h);
  });

  it('never-fired watcher summarises to zeros', () => {
    expect(summarizeWatcherHistory([], [], now)).toEqual({
      fires24h: 0, notifs24h: 0, notifFails24h: 0, lastFire: 0,
    });
  });

  // v0.9.196 review-fix pin — lastFire pencereye BAĞLI DEĞİL: tek fire
  // 24h dışındaysa fires24h=0 ama lastFire yine o eski zamandır (satır
  // "Last fire: 3d ago · Fires(24h): 0" diyebilmeli).
  it('lastFire survives outside the 24h window while fires24h is 0', () => {
    const old = now - 72 * h;
    const s = summarizeWatcherHistory([problem({ id: 'p-old', startedAt: old })], [], now);
    expect(s.fires24h).toBe(0);
    expect(s.lastFire).toBe(old);
  });
});
