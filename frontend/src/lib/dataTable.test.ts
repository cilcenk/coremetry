import { describe, it, expect } from 'vitest';
import { nextSort, sortRows, type DataTableColumn, type SortState } from './dataTable';

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
  it('puts null/undefined first ascending, last descending', () => {
    expect(sortRows(rows, COLS.p99, 'asc')[0].p99).toBeNull();
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
