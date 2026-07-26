import { describe, it, expect } from 'vitest';
import { accumulatePage } from './logAccumulate';

// v0.9.292 — "Load more" accumulated without any ceiling: twenty clicks
// meant two thousand live rows, and while content-visibility keeps
// painting cheap, the React tree and the per-row highlight transform
// both grow linearly with the buffer. The live tail has had LIVE_CAP
// since it shipped; the static list never got one.
//
// Two ways this can silently lose an operator's rows — dedup and the
// cap — so both are pinned, and the cap reports what it dropped.

const rows = (...ids: number[]) => ids.map(id => ({ id, body: `row-${id}` }));

describe('accumulatePage', () => {
  it('appends a page below the rows already loaded', () => {
    const r = accumulatePage(rows(1, 2), rows(3, 4), 100);
    expect(r.rows.map(x => x.id)).toEqual([1, 2, 3, 4]);
    expect(r.dropped).toBe(0);
  });

  it('drops rows the previous page already carried', () => {
    // The keyset cursor re-reads its boundary row inclusively, so the
    // first row of a page is routinely a repeat (v0.7.22).
    const r = accumulatePage(rows(1, 2, 3), rows(3, 4, 5), 100);
    expect(r.rows.map(x => x.id)).toEqual([1, 2, 3, 4, 5]);
  });

  it('keeps the newly loaded rows when the buffer overflows', () => {
    // The operator clicking "Load more" is reading DOWNWARD, so the
    // window slides forward: the front is what leaves.
    const r = accumulatePage(rows(1, 2, 3, 4), rows(5, 6), 4);
    expect(r.rows.map(x => x.id)).toEqual([3, 4, 5, 6]);
    expect(r.dropped).toBe(2);
  });

  it('reports exactly how many rows left the buffer', () => {
    const r = accumulatePage(rows(1, 2, 3, 4, 5), rows(6, 7, 8), 4);
    expect(r.dropped).toBe(4);
    expect(r.rows).toHaveLength(4);
    expect(r.rows.map(x => x.id)).toEqual([5, 6, 7, 8]);
  });

  it('does not drop anything while under the cap', () => {
    const r = accumulatePage(rows(1, 2), rows(3), 10);
    expect(r.dropped).toBe(0);
    expect(r.rows).toHaveLength(3);
  });

  it('does not drop anything when landing exactly on the cap', () => {
    const r = accumulatePage(rows(1, 2), rows(3, 4), 4);
    expect(r.dropped).toBe(0);
    expect(r.rows.map(x => x.id)).toEqual([1, 2, 3, 4]);
  });

  it('counts only rows that survived dedup against the cap', () => {
    // 3 held + a page of 3 of which 2 are repeats = 4 rows, cap 4:
    // nothing should be dropped. Counting the raw page length here
    // would evict a row the operator can still see.
    const r = accumulatePage(rows(1, 2, 3), rows(2, 3, 4), 4);
    expect(r.rows.map(x => x.id)).toEqual([1, 2, 3, 4]);
    expect(r.dropped).toBe(0);
  });

  it('treats a non-positive cap as "no ceiling", never as "keep nothing"', () => {
    // An accidental 0 must not silently empty the page.
    expect(accumulatePage(rows(1, 2), rows(3), 0).rows).toHaveLength(3);
    expect(accumulatePage(rows(1, 2), rows(3), -1).dropped).toBe(0);
  });

  it('handles an empty page without disturbing the buffer', () => {
    const r = accumulatePage(rows(1, 2), [], 100);
    expect(r.rows.map(x => x.id)).toEqual([1, 2]);
    expect(r.dropped).toBe(0);
  });

  it('stays bounded across many appends — the actual regression', () => {
    let acc = rows();
    let total = 0;
    for (let page = 0; page < 40; page++) {
      const next = rows(...Array.from({ length: 100 }, (_, i) => page * 100 + i));
      const r = accumulatePage(acc, next, 2000);
      acc = r.rows;
      total += r.dropped;
      expect(acc.length).toBeLessThanOrEqual(2000);
    }
    // 4000 rows fetched, 2000 held, so 2000 must have been reported —
    // the number the UI shows. Nothing vanishes uncounted.
    expect(acc).toHaveLength(2000);
    expect(total).toBe(2000);
    // And what's held is the tail, i.e. what the operator paged to.
    expect(acc[acc.length - 1].id).toBe(3999);
  });
});
