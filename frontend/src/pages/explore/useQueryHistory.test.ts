import { describe, it, expect } from 'vitest';
import {
  mergeHistory,
  parseHistory,
  MAX_HISTORY,
  type QueryHistoryEntry,
} from './useQueryHistory';

// explore-v2 Phase-1 — pins the recent-queries ("Son sorgular") ring
// semantics. The ring is the entry-screen's "jump back to what I was
// looking at" affordance; if merge/dedupe/cap or the corrupt-JSON
// tolerance regresses, the question-card screen either loses history
// silently or throws on a poisoned localStorage value and white-screens
// the page. Pure-function table tests so the math is guarded without a DOM.

const e = (desc: string, state: unknown = '?x', tm = 1): QueryHistoryEntry =>
  ({ desc, state, tm });

describe('mergeHistory', () => {
  it('prepends a fresh entry (newest first)', () => {
    const out = mergeHistory([e('a')], e('b'));
    expect(out.map(x => x.desc)).toEqual(['b', 'a']);
  });

  it('dedupes by desc — re-running a query bumps it to the front', () => {
    const start = [e('a'), e('b'), e('c')];
    const out = mergeHistory(start, e('b', '?new', 99));
    expect(out.map(x => x.desc)).toEqual(['b', 'a', 'c']);
    // bumped entry carries the new payload + tm, not the stale one.
    expect(out[0].state).toBe('?new');
    expect(out[0].tm).toBe(99);
  });

  it(`caps at MAX_HISTORY (${MAX_HISTORY})`, () => {
    let acc: QueryHistoryEntry[] = [];
    for (const d of ['a', 'b', 'c', 'd', 'e', 'f']) acc = mergeHistory(acc, e(d));
    expect(acc).toHaveLength(MAX_HISTORY);
    // newest MAX_HISTORY survive, oldest dropped.
    expect(acc.map(x => x.desc)).toEqual(['f', 'e', 'd', 'c']);
  });

  it('skips empty descriptions (no blank rows in the list)', () => {
    const start = [e('a')];
    expect(mergeHistory(start, e(''))).toEqual(start);
  });

  it('dedupe + cap together: re-running keeps length within the cap', () => {
    const start = [e('a'), e('b'), e('c'), e('d')]; // already full
    const out = mergeHistory(start, e('c', '?z', 5));
    expect(out).toHaveLength(MAX_HISTORY);
    expect(out.map(x => x.desc)).toEqual(['c', 'a', 'b', 'd']);
  });
});

describe('parseHistory', () => {
  const cases: { name: string; raw: string | null; want: string[] }[] = [
    { name: 'null input → empty', raw: null, want: [] },
    { name: 'empty string → empty', raw: '', want: [] },
    { name: 'corrupt JSON → empty (tolerant, never throws)', raw: '{not json', want: [] },
    { name: 'non-array JSON → empty', raw: '{"desc":"a","tm":1}', want: [] },
    { name: 'array of valid entries', raw: JSON.stringify([e('a'), e('b')]), want: ['a', 'b'] },
    {
      name: 'drops rows missing required fields',
      raw: JSON.stringify([{ desc: 'a', state: '?x', tm: 1 }, { state: '?y' }, { desc: 'c', tm: 3 }]),
      want: ['a', 'c'],
    },
    {
      name: 'caps an over-long persisted array',
      raw: JSON.stringify([e('a'), e('b'), e('c'), e('d'), e('e'), e('f')]),
      want: ['a', 'b', 'c', 'd'],
    },
    {
      // Phase-1 review finding: mergeHistory refuses to WRITE an empty
      // desc, but a hand-edited/poisoned blob row used to pass the read
      // path and render a blank "Son sorgular" row.
      name: 'drops rows with empty desc',
      raw: JSON.stringify([{ desc: '', state: '?x', tm: 1 }, e('a')]),
      want: ['a'],
    },
  ];
  for (const c of cases) {
    it(c.name, () => {
      expect(parseHistory(c.raw).map(x => x.desc)).toEqual(c.want);
    });
  }

  it('round-trips a merged ring through JSON', () => {
    let acc: QueryHistoryEntry[] = [];
    for (const d of ['a', 'b', 'c']) acc = mergeHistory(acc, e(d));
    const roundTripped = parseHistory(JSON.stringify(acc));
    expect(roundTripped).toEqual(acc);
  });
});
