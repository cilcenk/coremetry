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
  // Pin this column to the RIGHT edge of the scroll container
  // (v0.8.573 — Endpoints "Traces"). Wide tables overflow .table-wrap's
  // horizontal scroll and the scrollbar sits below the rows, so a
  // trailing action column is effectively invisible on laptop widths.
  // DataTableHead emits the .sticky-right th; the caller must mirror
  // the class on the matching body <td> (and give it an OPAQUE
  // background when the row carries an inline tint — sticky cells
  // float over scrolled content). Only meaningful on the LAST visible
  // column: multiple pinned columns would need per-column right
  // offsets, which nothing needs yet.
  stickyRight?: boolean;
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

// compareValues — type-aware comparator for two PRESENT (non-null)
// values, returning the pre-direction ordering. Numbers compare
// numerically; everything else via locale string compare. Null handling
// lives in sortRows (nulls are direction-independent — always last).
function compareValues(a: number | string, b: number | string): number {
  if (typeof a === 'number' && typeof b === 'number') return a - b;
  return String(a).localeCompare(String(b));
}

// sortRows — STABLE client-side sort by a column's accessor, returning
// a NEW array (never mutates input). Two invariants:
//  • Stability — re-sorting by a column with many ties (e.g. all the
//    same db_system) preserves the server's original order within each
//    group (tiebreak on original index).
//  • Nulls last — null/undefined accessor values sink to the BOTTOM
//    regardless of direction (missing data shouldn't jump to the top
//    when sorting ascending; matches the SLO list's intent).
// Returns the input unchanged when the column is absent or non-sortable.
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
      const a = x.v;
      const b = y.v;
      if (a == null && b == null) return x.i - y.i;
      if (a == null) return 1;  // x is null → sinks below y
      if (b == null) return -1; // y is null → x stays above
      const c = compareValues(a, b) * mul;
      return c !== 0 ? c : x.i - y.i; // stable tiebreak on original index
    })
    .map(x => x.r);
}

// ---------------------------------------------------------------------------
// v0.8.251 — serverSort support. The hook (components/DataTable.tsx) gained
// an optional serverSort mode for server-paged tables (Services first): the
// sort STATE machinery — URL `s_<storageKey>` param, localStorage
// persistence, header click semantics — is shared with client mode, but the
// ordering itself is the backend's ORDER BY. The pure halves live below so
// both modes stay unit-tested in the node vitest harness; nextSort/sortRows
// above are untouched (contract unchanged) — mode selection COMPOSES them.

// parseSortParam — decode the URL sort param "<colId>.<dir>" → SortState.
// Returns null for a missing / malformed value so the caller falls back to
// localStorage. colId may itself contain dots, so split on the LAST one.
// (Moved here from components/DataTable.tsx in v0.8.251 so the URL codec
// round-trip is pinned by unit tests.)
export function parseSortParam(s: string | null): SortState | null {
  if (!s) return null;
  const i = s.lastIndexOf('.');
  if (i <= 0) return null;
  const dir = s.slice(i + 1);
  if (dir !== 'asc' && dir !== 'desc') return null;
  return { id: s.slice(0, i), dir: dir as SortDir };
}

// formatSortParam — SortState → "<colId>.<dir>" URL value; null when no
// column is active (the hook deletes the param instead of writing it).
// Inverse of parseSortParam for every non-empty id, including ids that
// themselves contain dots.
export function formatSortParam(s: SortState): string | null {
  return s.id ? `${s.id}.${s.dir}` : null;
}

// resolveToggle — the pure half of the hook's toggleSort: look up the
// clicked column and produce the next sort state, or null when the column
// is unknown / not sortable (the hook no-ops and onSortChange never fires).
// Mode-independent: in serverSort mode this exact value is what the hook
// reports via onSortChange / the returned `sort`, so the page re-fetches
// with the new ORDER BY.
export function resolveToggle<T>(
  columns: DataTableColumn<T>[],
  cur: SortState,
  id: string,
): SortState | null {
  const col = columns.find(c => c.id === id);
  if (!col || !col.sortValue) return null;
  return nextSort(cur, col);
}

// computeSortedRows — the row pipeline behind the hook's sortedRows memo.
// serverSort=true returns `rows` VERBATIM (reference-equal): the backend
// already applied its ORDER BY, and any client-side reorder — or even a
// defensive copy — would contradict the server page and defeat memoized
// children. Client mode is the existing sortRows path, contract unchanged.
export function computeSortedRows<T>(
  rows: T[],
  columns: DataTableColumn<T>[],
  sort: SortState,
  serverSort: boolean,
): T[] {
  if (serverSort) return rows;
  const col = sort.id ? columns.find(c => c.id === sort.id) : undefined;
  return sortRows(rows, col, sort.dir);
}
