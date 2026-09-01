import { describe, it, expect } from 'vitest';
import { traceBackHref, TRACES_LIST_HREF } from './traceBackHref';

// traceBackHref — v0.10.219. İki şeyi çiviler: (1) liste URL'si (filtre +
// sayfa + range) breadcrumb'dan AYNEN geri döner; (2) state istemciden
// geldiği için yalnız uygulama-içi /traces yolu kabul edilir.

describe('traceBackHref', () => {
  const cases: Array<[string, unknown, string]> = [
    ['state yok → liste', null, TRACES_LIST_HREF],
    ['state undefined → liste', undefined, TRACES_LIST_HREF],
    ['state nesne ama from yok → liste', { other: 1 }, TRACES_LIST_HREF],
    ['from string değil → liste', { from: 42 }, TRACES_LIST_HREF],
    ['çıplak liste', { from: '/traces' }, '/traces'],
    ['filtre + sayfa + range korunur', { from: '/traces?service=payment&page=2&range=1h' }, '/traces?service=payment&page=2&range=1h'],
    ['alt yol kabul', { from: '/traces/foo' }, '/traces/foo'],
    ['önek benzeri başka rota reddedilir', { from: '/tracesfoo?x=1' }, TRACES_LIST_HREF],
    ['başka rota reddedilir', { from: '/logs?traceId=abc' }, TRACES_LIST_HREF],
    ['protokol-göreli reddedilir', { from: '//evil.example/traces' }, TRACES_LIST_HREF],
    ['mutlak URL reddedilir', { from: 'https://evil.example/traces' }, TRACES_LIST_HREF],
  ];
  for (const [name, state, want] of cases) {
    it(name, () => { expect(traceBackHref(state)).toBe(want); });
  }
});
