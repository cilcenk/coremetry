import { describe, it, expect } from 'vitest';
import {
  nextSort, sortRows,
  parseSortParam, formatSortParam, resolveToggle, computeSortedRows,
  type DataTableColumn, type SortState,
} from './dataTable';

// v0.7.53 — pins the shared sortable-table primitive's click semantics
// + stable sort. The project principle is "every table sortable +
// resizable"; if this math regresses, every adopting table mis-sorts.

interface Row { svc: string; calls: number; p99: number | null }

const COLS: Record<string, DataTableColumn<Row>> = {
  svc: { id: 'svc', label: 'Service', sortValue: r => r.svc, naturalDir: 'asc' },
  calls: { id: 'calls', label: 'Calls', sortValue: r => r.calls, numeric: true }, // default naturalDir desc
  p99: { id: 'p99', label: 'P99', sortValue: r => r.p99, numeric: true },
  noSort: { id: 'noSort', label: 'X' }, // no sortValue
};

describe('nextSort', () => {
  it('selects a new column at its natural direction', () => {
    const cur: SortState = { id: 'calls', dir: 'desc' };
    expect(nextSort(cur, COLS.svc)).toEqual({ id: 'svc', dir: 'asc' });
  });
  it('defaults a column with no naturalDir to desc on first click', () => {
    const cur: SortState = { id: null, dir: 'desc' };
    expect(nextSort(cur, COLS.calls)).toEqual({ id: 'calls', dir: 'desc' });
  });
  it('flips direction when the active column is re-clicked', () => {
    expect(nextSort({ id: 'calls', dir: 'desc' }, COLS.calls)).toEqual({ id: 'calls', dir: 'asc' });
    expect(nextSort({ id: 'calls', dir: 'asc' }, COLS.calls)).toEqual({ id: 'calls', dir: 'desc' });
  });
});

describe('sortRows', () => {
  const rows: Row[] = [
    { svc: 'b', calls: 10, p99: 5 },
    { svc: 'a', calls: 30, p99: null },
    { svc: 'c', calls: 10, p99: 2 },
  ];

  it('sorts numbers descending', () => {
    expect(sortRows(rows, COLS.calls, 'desc').map(r => r.calls)).toEqual([30, 10, 10]);
  });
  it('sorts numbers ascending', () => {
    expect(sortRows(rows, COLS.calls, 'asc').map(r => r.calls)).toEqual([10, 10, 30]);
  });
  it('sorts strings with locale compare', () => {
    expect(sortRows(rows, COLS.svc, 'asc').map(r => r.svc)).toEqual(['a', 'b', 'c']);
  });
  it('is STABLE on ties — preserves original order within equal keys', () => {
    // both 'b' and 'c' have calls=10 and appear in that input order; a stable
    // desc sort keeps b before c (b is index 0, c is index 2).
    expect(sortRows(rows, COLS.calls, 'desc').map(r => r.svc)).toEqual(['a', 'b', 'c']);
  });
  it('sinks null/undefined to the BOTTOM regardless of direction', () => {
    // missing data shouldn't jump to the top when sorting ascending
    expect(sortRows(rows, COLS.p99, 'asc').at(-1)!.p99).toBeNull();
    expect(sortRows(rows, COLS.p99, 'desc').at(-1)!.p99).toBeNull();
  });
  it('returns the input unchanged for a non-sortable column', () => {
    expect(sortRows(rows, COLS.noSort, 'asc')).toBe(rows);
  });
  it('never mutates the input array', () => {
    const snapshot = rows.map(r => r.svc);
    sortRows(rows, COLS.calls, 'asc');
    expect(rows.map(r => r.svc)).toEqual(snapshot);
  });
});

// v0.8.251 — serverSort mode. The hook gained an opt-in mode for
// server-paged tables (Services first) where the backend owns the ORDER BY;
// these pin the pure halves the hook composes: the `s_<storageKey>` URL
// codec, the toggle→callback value, and the rows-verbatim pipeline.

describe('parseSortParam / formatSortParam — URL `s_<storageKey>` codec (v0.8.251)', () => {
  it('round-trips a plain column id', () => {
    const s: SortState = { id: 'calls', dir: 'desc' };
    expect(parseSortParam(formatSortParam(s))).toEqual(s);
  });
  it('round-trips an id that itself contains dots (parse splits on the LAST one)', () => {
    const s: SortState = { id: 'http.status_code', dir: 'asc' };
    expect(formatSortParam(s)).toBe('http.status_code.asc');
    expect(parseSortParam(formatSortParam(s))).toEqual(s);
  });
  it('formats a null id as null so the hook deletes the param instead of writing it', () => {
    expect(formatSortParam({ id: null, dir: 'desc' })).toBeNull();
  });
  it('rejects missing / malformed values (caller falls back to localStorage)', () => {
    expect(parseSortParam(null)).toBeNull();
    expect(parseSortParam('')).toBeNull();
    expect(parseSortParam('nodot')).toBeNull();
    expect(parseSortParam('.asc')).toBeNull();        // empty id
    expect(parseSortParam('calls.up')).toBeNull();    // bogus direction
  });
});

describe('resolveToggle — the sort every header click applies / onSortChange reports (v0.8.251)', () => {
  it('selects a newly clicked column at its natural direction', () => {
    expect(resolveToggle(Object.values(COLS), { id: 'calls', dir: 'desc' }, 'svc'))
      .toEqual({ id: 'svc', dir: 'asc' });
  });
  it('flips the active column', () => {
    expect(resolveToggle(Object.values(COLS), { id: 'calls', dir: 'desc' }, 'calls'))
      .toEqual({ id: 'calls', dir: 'asc' });
  });
  it('returns null for a non-sortable or unknown column — the hook no-ops and onSortChange never fires', () => {
    expect(resolveToggle(Object.values(COLS), { id: 'calls', dir: 'desc' }, 'noSort')).toBeNull();
    expect(resolveToggle(Object.values(COLS), { id: 'calls', dir: 'desc' }, 'ghost')).toBeNull();
  });
});

describe('computeSortedRows — sortedRows pipeline (v0.8.251)', () => {
  const rows: Row[] = [
    { svc: 'b', calls: 10, p99: 5 },
    { svc: 'a', calls: 30, p99: null },
  ];
  it('serverSort returns the rows array VERBATIM — reference-equal, server order untouched', () => {
    const out = computeSortedRows(rows, Object.values(COLS), { id: 'calls', dir: 'asc' }, true);
    expect(out).toBe(rows); // identity, not just deep-equality
    expect(out.map(r => r.svc)).toEqual(['b', 'a']); // an asc sort WOULD have swapped these
  });
  it('client mode still routes through sortRows (new array, actually sorted)', () => {
    const out = computeSortedRows(rows, Object.values(COLS), { id: 'calls', dir: 'asc' }, false);
    expect(out).not.toBe(rows);
    expect(out.map(r => r.calls)).toEqual([10, 30]);
  });
  it('client mode with no active column returns rows unchanged (sortRows contract)', () => {
    expect(computeSortedRows(rows, Object.values(COLS), { id: null, dir: 'desc' }, false)).toBe(rows);
  });
});
