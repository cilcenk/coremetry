import { describe, it, expect } from 'vitest';
import { eventInvalidations, catchupInvalidations } from './eventInvalidations';
import { keys } from './keys';

// v0.8.529 — single-SSE refactor: leader ve follower yolları AYNI
// event→key eşlemesini kullanmalı. Bu pure eşlemeyi sabitler.
describe('eventInvalidations', () => {
  // v0.9.317 — inbox eklendi. Uzunluk pinleri BİLEREK korunuyor: bu
  // testin işi listenin BÜYÜMEDİĞİNİ garanti etmek, çünkü her ek anahtar
  // her olayda bir refetch daha demek. Sayı arttıysa gerekçesi olmalı.
  it('problem olayları problems + anomaly-metrics + incidents + inbox geçersiz kılar', () => {
    for (const kind of ['problem.open', 'problem.resolve'] as const) {
      const got = eventInvalidations(kind);
      expect(got).toContainEqual(keys.problems.all);
      expect(got).toContainEqual(keys.anomalies.metrics);
      expect(got).toContainEqual(keys.incidents.all);
      expect(got).toContainEqual(keys.inbox.all);
      expect(got).toHaveLength(4);
    }
  });
  it('anomaly olayları anomalies.all + inbox', () => {
    for (const kind of ['anomaly.open', 'anomaly.clear'] as const) {
      expect(eventInvalidations(kind)).toEqual([keys.anomalies.all, keys.inbox.all]);
    }
  });
  it('rollout yalnız listeyi geçersiz kılar (stats 60 s TTL korunur)', () => {
    expect(eventInvalidations('rollout')).toEqual([keys.rollouts.listAll]);
  });
  it('bilinmeyen kind → boş (blanket refetch YOK)', () => {
    expect(eventInvalidations('garbage.kind')).toEqual([]);
    expect(eventInvalidations('')).toEqual([]);
  });
  it('catchup her handler anahtarının birleşimi', () => {
    const c = catchupInvalidations();
    expect(c).toContainEqual(keys.problems.all);
    expect(c).toContainEqual(keys.anomalies.all);
    expect(c).toContainEqual(keys.anomalies.metrics);
    expect(c).toContainEqual(keys.incidents.all);
  });
});

// v0.9.317 — the Inbox was absent from every invalidation list.
//
// It aggregates exactly the sources these events describe (Problem /
// Exception group / Anomaly event), yet its keys were never registered
// in keys.ts — they lived inline in inbox.ts — so no handler could name
// them. The result: the surface designed to be the daily landing page
// was the LAST to learn about anything, waiting out a 30s poll while
// the per-source pages it aggregates refreshed in under a second.
//
// This is the first of the five capability gaps that make a
// navigation merge premature: you cannot funnel operators into a
// surface that is staler than the ones it replaces.
describe('inbox liveness', () => {
  const hasInbox = (ks: readonly (readonly unknown[])[]) =>
    ks.some(k => Array.isArray(k) && k[0] === 'inbox');

  it('refreshes on a problem opening or resolving', () => {
    expect(hasInbox(eventInvalidations('problem.open'))).toBe(true);
    expect(hasInbox(eventInvalidations('problem.resolve'))).toBe(true);
  });

  it('refreshes on an anomaly opening or clearing', () => {
    expect(hasInbox(eventInvalidations('anomaly.open'))).toBe(true);
    expect(hasInbox(eventInvalidations('anomaly.clear'))).toBe(true);
  });

  it('is in the reconnect catch-up set', () => {
    // A tab that missed events while disconnected must not keep showing
    // a stale queue — the catch-up set is the only thing that repairs it.
    expect(hasInbox(catchupInvalidations())).toBe(true);
  });

  it('still returns nothing for an unknown event', () => {
    // The no-blanket-refetch contract survives: an unknown kind must not
    // start refreshing the inbox just because it now appears in the map.
    expect(eventInvalidations('something.else')).toEqual([]);
  });

  it('invalidates the whole inbox namespace, list AND badge', () => {
    // ['inbox'] is a PREFIX, so it covers both ['inbox','list',…] and
    // ['inbox','count',…]. Naming only one would leave the sidebar
    // badge disagreeing with the table under it.
    const ks = eventInvalidations('problem.open');
    const inboxKey = ks.find(k => Array.isArray(k) && k[0] === 'inbox');
    expect(inboxKey).toEqual(['inbox']);
  });
});
