// dataTable — the pure, testable core of Coremetry's shared
// sortable + resizable table primitive (v0.7.53). The project
// principle: EVERY data table is column-sortable and
// column-resizable. The React glue lives in
// components/DataTable.tsx; the sort math + click semantics live
// here so they're unit-tested in isolation (CLAUDE.md #11), the
// same way tableColumns.ts backs the Traces reorder feature.

export type SortDir = 'asc' | 'desc';

// DataTableColumn describes one column for the shared primitive.
// A column with no `sortValue` is not clickable-to-sort (e.g. an
// expand-chevron or an actions column); it still participates in
// the fixed layout + resize.
export interface DataTableColumn<T> {
  id: string;
  label: string;
  // Accessor used for client-side sorting. Omit → column isn't sortable.
  sortValue?: (row: T) => number | string | null | undefined;
  // Direction applied when this column is FIRST clicked. Defaults to
  // 'desc' (numeric tables want biggest-first); set 'asc' for names.
  naturalDir?: SortDir;
  align?: 'left' | 'right';
  // Apply the .num class (right-align + tabular-nums). Implies right
  // align unless `align` overrides.
  numeric?: boolean;
  // Default column width (px) for the fixed-layout colgroup + the
  // resize starting point.
  width?: number;
  // Resize floor (px).
  minWidth?: number;
  // Sortable-only dimension that is NOT rendered as a header/column —
  // e.g. a composite "impact" score a preset button sorts by. Excluded
  // from <DataTableHead>/<DataTableColgroup>; still resolvable by
  // sortRows + setSort. Keeps body cells aligned to visible headers.
  headerHidden?: boolean;
}

export interface SortState {
  id: string | null;
  dir: SortDir;
}

// nextSort encodes the click semantics shared by every Coremetry
// table (matches the long-standing Services.tsx toggleSort): clicking
// the active column flips direction; clicking a new column selects it
// at its natural starting direction.
export function nextSort<T>(cur: SortState, col: DataTableColumn<T>): SortState {
  if (cur.id === col.id) {
    return { id: col.id, dir: cur.dir === 'desc' ? 'asc' : 'desc' };
  }
  return { id: col.id, dir: col.naturalDir ?? 'desc' };
}

// compareValues — type-aware, null-tolerant comparator returning the
// pre-direction ordering. null/undefined always sort BEFORE present
// values (so with `desc` they land last); numbers compare numerically,
// everything else via locale string compare.
function compareValues(
  a: number | string | null | undefined,
  b: number | string | null | undefined,
): number {
  const an = a == null;
  const bn = b == null;
  if (an && bn) return 0;
  if (an) return -1;
  if (bn) return 1;
  if (typeof a === 'number' && typeof b === 'number') return a - b;
  return String(a).localeCompare(String(b));
}

// sortRows — STABLE client-side sort by a column's accessor, returning
// a NEW array (never mutates input). Stability matters: re-sorting by a
// column with many ties (e.g. all the same db_system) must preserve the
// server's original ordering within each group. Returns the input
// unchanged when the column is absent or non-sortable.
export function sortRows<T>(
  rows: T[],
  col: DataTableColumn<T> | undefined,
  dir: SortDir,
): T[] {
  if (!col || !col.sortValue) return rows;
  const acc = col.sortValue;
  const mul = dir === 'asc' ? 1 : -1;
  return rows
    .map((r, i) => ({ r, i, v: acc(r) }))
    .sort((x, y) => {
      const c = compareValues(x.v, y.v);
      return c !== 0 ? c * mul : x.i - y.i; // stable tiebreak on original index
    })
    .map(x => x.r);
}
