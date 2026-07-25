import { describe, it, expect } from 'vitest';
import {
  inboxActionsForKind,
  exceptionStateActions,
  rootCauseAnchor,
  resolveSelectedItem,
  buildAnomalySilenceBody,
} from './inboxDrawer';
import type { InboxItem, InboxKind } from './types';

// v0.8.292 — /inbox row-click opens an in-place triage drawer (Option B slice
// 3). Pin the pure seams the drawer stands on: (1) which inline actions a kind
// exposes, (2) the RootCauseRibbon anchor per kind, (3) resolving ?item=<id>
// against the loaded list (soft-fallback when absent), (4) the
// createAnomalySilence body built from an anomaly item — mirrors lib/actions.ts
// (fingerprint = the anomaly's native id, kind/pattern from the sub-object,
// service from the item).

function mk(kind: InboxKind, over: Partial<InboxItem> = {}): InboxItem {
  const base: InboxItem = {
    id: `${kind}:native-1`,
    kind,
    source: kind,
    priority: 'P1',
    priorityReason: 'test',
    severity: 'high',
    service: 'checkout',
    title: 't',
    description: 'd',
    startedAt: 0,
    lastSeen: 0,
    status: 'open',
  };
  if (kind === 'problem') base.problem = { id: 'prob-9', ruleId: 'r1', metric: 'lat', value: 2, threshold: 1 };
  if (kind === 'exception') base.exception = { fingerprint: 'fp-9', type: 'NPE', message: 'boom', occurrences: 5 };
  if (kind === 'anomaly') base.anomaly = { id: 'anom-9', kind: 'log_pattern', pattern: 'timeout x', peakRatio: 4, currentRatio: 2 };
  return { ...base, ...over };
}

describe('inboxActionsForKind', () => {
  it('problem exposes acknowledge + assign + root cause + open', () => {
    expect(inboxActionsForKind('problem')).toEqual({
      acknowledge: true, assign: true, mute: false, rootCause: true, openSource: true, setState: false,
    });
  });
  it('anomaly exposes mute + root cause + open (no ack/assign)', () => {
    expect(inboxActionsForKind('anomaly')).toEqual({
      acknowledge: false, assign: false, mute: true, rootCause: true, openSource: true, setState: false,
    });
  });
  // v0.9.254 — exceptions gained setState. Before this they exposed ONLY the
  // escape hatch, so a group silenced with Ignore could not be un-ignored from
  // the unified inbox at all.
  it('exception exposes setState + open source (still no rc endpoint)', () => {
    expect(inboxActionsForKind('exception')).toEqual({
      acknowledge: false, assign: false, mute: false, rootCause: false, openSource: true, setState: true,
    });
  });
});

// v0.9.254 — the transition set is the guard against the irreversible-ignore
// hole. The one case that MUST hold forever: a group in `ignored` is offered a
// way back.
describe('exceptionStateActions', () => {
  it('ignored offers un-ignore — the whole point', () => {
    const acts = exceptionStateActions('ignored');
    expect(acts).toHaveLength(1);
    expect(acts[0]).toEqual({ to: 'new', label: 'Un-ignore' });
  });
  it('resolved offers reopen', () => {
    expect(exceptionStateActions('resolved')).toEqual([{ to: 'new', label: 'Reopen' }]);
  });
  it('new offers acknowledge / resolve / ignore', () => {
    expect(exceptionStateActions('new').map(a => a.to)).toEqual(['acknowledged', 'resolved', 'ignored']);
  });
  it('regressed behaves like new', () => {
    expect(exceptionStateActions('regressed').map(a => a.to))
      .toEqual(exceptionStateActions('new').map(a => a.to));
  });
  it('acknowledged drops the redundant acknowledge', () => {
    expect(exceptionStateActions('acknowledged').map(a => a.to)).toEqual(['resolved', 'ignored']);
  });
  it('never offers a transition to the state it is already in', () => {
    for (const st of ['new', 'acknowledged', 'resolved', 'ignored', 'regressed']) {
      expect(exceptionStateActions(st).map(a => a.to)).not.toContain(st);
    }
  });
  it('unknown state falls back to a usable set, not an empty one', () => {
    // Being unable to act on a row is worse than one redundant button.
    expect(exceptionStateActions(undefined).length).toBeGreaterThan(0);
    expect(exceptionStateActions('weird').length).toBeGreaterThan(0);
  });
});

describe('rootCauseAnchor', () => {
  it('problem → problem anchor on the native problem id (not the composite inbox id)', () => {
    expect(rootCauseAnchor(mk('problem'))).toEqual({ anchor: 'problem', id: 'prob-9' });
  });
  it('anomaly → anomaly anchor on the native anomaly id', () => {
    expect(rootCauseAnchor(mk('anomaly'))).toEqual({ anchor: 'anomaly', id: 'anom-9' });
  });
  it('exception → null (no root-cause fan-out endpoint)', () => {
    expect(rootCauseAnchor(mk('exception'))).toBeNull();
  });
  it('null when the sub-object is missing even for a rc-capable kind', () => {
    const p = mk('problem'); delete p.problem;
    expect(rootCauseAnchor(p)).toBeNull();
  });
});

describe('resolveSelectedItem', () => {
  const list = [mk('problem', { id: 'problem:a' }), mk('anomaly', { id: 'anomaly:b' })];
  it('finds by composite id', () => {
    expect(resolveSelectedItem(list, 'anomaly:b')?.id).toBe('anomaly:b');
  });
  it('undefined when id absent (no ?item=)', () => {
    expect(resolveSelectedItem(list, null)).toBeUndefined();
  });
  it('undefined when id not in the current list (→ soft fallback)', () => {
    expect(resolveSelectedItem(list, 'problem:missing')).toBeUndefined();
  });
  it('undefined while the list is loading/errored (undefined/null)', () => {
    expect(resolveSelectedItem(undefined, 'anomaly:b')).toBeUndefined();
    expect(resolveSelectedItem(null, 'anomaly:b')).toBeUndefined();
  });
});

describe('buildAnomalySilenceBody', () => {
  it('mirrors lib/actions.ts: fingerprint=native anomaly id, kind/pattern from sub-object, service from item', () => {
    expect(buildAnomalySilenceBody(mk('anomaly'), 3600)).toEqual({
      fingerprint: 'anom-9',
      kind: 'log_pattern',
      pattern: 'timeout x',
      service: 'checkout',
      durationSec: 3600,
    });
  });
  it('null for a non-anomaly kind (guards the mute button)', () => {
    expect(buildAnomalySilenceBody(mk('problem'), 3600)).toBeNull();
    expect(buildAnomalySilenceBody(mk('exception'), 3600)).toBeNull();
  });
  it('null when the anomaly sub-object is missing', () => {
    const a = mk('anomaly'); delete a.anomaly;
    expect(buildAnomalySilenceBody(a, 3600)).toBeNull();
  });
});
