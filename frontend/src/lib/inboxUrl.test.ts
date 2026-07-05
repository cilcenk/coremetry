import { describe, it, expect } from 'vitest';
import { decodeCsvSet, encodeCsvSet } from './inboxUrl';

// v0.8.291 — /inbox facets move to the URL. Pin the codec: absent = default,
// invalid tokens dropped, order canonicalised to `allowed`, default selection
// serialises to null (param omitted so a default view's link stays clean).
const PRIO = ['P1', 'P2', 'P3'] as const;
const PRIO_DFLT = ['P1', 'P2'] as const;
const KIND = ['problem', 'exception', 'anomaly'] as const;

describe('decodeCsvSet', () => {
  it('null / empty falls back to default', () => {
    expect(decodeCsvSet(null, PRIO, PRIO_DFLT)).toEqual(['P1', 'P2']);
    expect(decodeCsvSet('', PRIO, PRIO_DFLT)).toEqual(['P1', 'P2']);
  });
  it('parses a valid subset', () => {
    expect(decodeCsvSet('P1', PRIO, PRIO_DFLT)).toEqual(['P1']);
    expect(decodeCsvSet('P2,P3', PRIO, PRIO_DFLT)).toEqual(['P2', 'P3']);
  });
  it('drops invalid tokens + dedupes + trims', () => {
    expect(decodeCsvSet(' P1 , nope , P1 , P3 ', PRIO, PRIO_DFLT)).toEqual(['P1', 'P3']);
  });
  it('all-invalid falls back to default (never empty)', () => {
    expect(decodeCsvSet('garbage', PRIO, PRIO_DFLT)).toEqual(['P1', 'P2']);
  });
  it('works for kind with an all-selected default', () => {
    expect(decodeCsvSet(null, KIND, KIND)).toEqual(['problem', 'exception', 'anomaly']);
    expect(decodeCsvSet('anomaly', KIND, KIND)).toEqual(['anomaly']);
  });
});

describe('encodeCsvSet', () => {
  it('returns null when selection equals default (param omitted)', () => {
    expect(encodeCsvSet(['P1', 'P2'], PRIO, PRIO_DFLT)).toBeNull();
    expect(encodeCsvSet(['P2', 'P1'], PRIO, PRIO_DFLT)).toBeNull(); // order-independent
    expect(encodeCsvSet(['anomaly', 'problem', 'exception'], KIND, KIND)).toBeNull();
  });
  it('serialises a non-default selection in allowed order', () => {
    expect(encodeCsvSet(['P3', 'P1'], PRIO, PRIO_DFLT)).toBe('P1,P3');
    expect(encodeCsvSet(['P1'], PRIO, PRIO_DFLT)).toBe('P1');
  });
  it('round-trips through decode', () => {
    const enc = encodeCsvSet(['P1', 'P3'], PRIO, PRIO_DFLT);
    expect(decodeCsvSet(enc, PRIO, PRIO_DFLT)).toEqual(['P1', 'P3']);
  });
});
