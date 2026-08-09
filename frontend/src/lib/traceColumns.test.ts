import { describe, it, expect } from 'vitest';
import { traceColumnOrder, FIXED_COLS, DEFAULT_TRACE_COLUMNS } from './traceColumns';

// traceColumns.test.ts — v0.9.841. Pins the /traces column order the
// operator asked for on 2026-08-09:
//
//   Time · Service · Operation · <attributes> · Duration · Spans · Status
//
// Worth a test because the header and the body cells both derive from
// this array. If it ever emitted a column twice, or dropped one, the
// two would still agree with EACH OTHER while disagreeing with reality
// — a table printing the wrong value under the right heading, which no
// type checks and no eye catches on a page of 50 rows.

describe('traceColumnOrder', () => {
  const cases: Array<[string, string[], string[]]> = [
    [
      'no extras — the six fixed columns in canonical order',
      [],
      ['time', 'service', 'operation', 'duration', 'spans', 'status'],
    ],
    [
      'the operator default set lands between Operation and Duration',
      ['openshift.cluster.name', 'channel_code', 'function_code', 'http.status_code'],
      [
        'time', 'service', 'operation',
        'openshift.cluster.name', 'channel_code', 'function_code', 'http.status_code',
        'duration', 'spans', 'status',
      ],
    ],
    [
      'one extra',
      ['pod'],
      ['time', 'service', 'operation', 'pod', 'duration', 'spans', 'status'],
    ],
    [
      'extras keep the order they were added in',
      ['z', 'a', 'm'],
      ['time', 'service', 'operation', 'z', 'a', 'm', 'duration', 'spans', 'status'],
    ],
  ];
  for (const [name, extras, want] of cases) {
    it(name, () => {
      expect(traceColumnOrder(extras)).toEqual(want);
    });
  }

  it('never leaks a fixed column into the attribute slot', () => {
    // The filter must exclude ALL THREE leading columns, not just time.
    // Getting that wrong duplicates service/operation — the regression
    // this whole helper exists to make visible.
    const ids = traceColumnOrder([]);
    expect(new Set(ids).size).toBe(ids.length);
    for (const c of FIXED_COLS) {
      expect(ids.filter(x => x === c)).toHaveLength(1);
    }
  });

  it('emits every fixed column exactly once, extras or not', () => {
    const ids = traceColumnOrder(DEFAULT_TRACE_COLUMNS);
    for (const c of FIXED_COLS) {
      expect(ids.filter(x => x === c)).toHaveLength(1);
    }
    expect(ids).toHaveLength(FIXED_COLS.length + DEFAULT_TRACE_COLUMNS.length);
  });

  it('does not mutate the caller array', () => {
    const extras = ['pod'];
    traceColumnOrder(extras);
    expect(extras).toEqual(['pod']);
  });
});

describe('DEFAULT_TRACE_COLUMNS', () => {
  it('is the operator-requested set, in the requested order', () => {
    expect(DEFAULT_TRACE_COLUMNS).toEqual([
      'openshift.cluster.name', 'channel_code', 'function_code', 'http.status_code',
    ]);
  });

  it('fits under the 8-column ceiling with room to add', () => {
    expect(DEFAULT_TRACE_COLUMNS.length).toBeLessThan(8);
  });
});
