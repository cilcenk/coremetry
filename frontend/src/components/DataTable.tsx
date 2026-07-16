import {
  useCallback, useEffect, useMemo, useState,
  type MouseEvent as ReactMouseEvent, type ReactNode, type RefObject,
} from 'react';
import { useSearchParams } from 'react-router-dom';
import { useTableNav, type TableNav } from '@/lib/useTableNav';
import { useShortcuts } from '@/lib/keyboard';
import {
  computeSortedRows, formatSortParam, parseSortParam, resolveToggle,
  type DataTableColumn, type SortState,
} from '@/lib/dataTable';
import { getItem, setItem, dtSortKey, dtWidthKey } from '@/lib/storage';

// DataTable — Coremetry's shared sortable + column-resizable table
// primitive (v0.7.53). Project principle: EVERY data table is
// column-sortable AND column-resizable. Adoption is three lines:
//
//   const dt = useDataTable({ storageKey: 'slowqueries', columns, rows,
//                             initialSort: { id: 'totalMs', dir: 'desc' } });
//   <table style={{ tableLayout: 'fixed', width: '100%' }}>
//     <DataTableColgroup dt={dt} leading={[36]} />
//     <DataTableHead dt={dt} leading={<th style={{ width: 36 }} />} />
//     <tbody>{dt.sortedRows.map(renderRow)}</tbody>
//   </table>
//
// Sort is CLIENT-SIDE by default (for the common "fetched array of
// ≤ a few k rows" table). Server-paged tables have two options:
// enable `serverSort` (v0.8.251 — the hook keeps the full sort UX +
// URL/localStorage state but the page forwards `sort` to its fetch;
// Services is the template) or keep their own headers and adopt only
// the resize half (Traces). Sort + width state persist to
// localStorage under the storageKey, so the operator's layout
// survives reloads — same contract as the Traces v0.7.47 cols.

const DEFAULT_W = 120;
const DEFAULT_MIN = 48;

export interface DataTable<T> {
  columns: DataTableColumn<T>[];
  sortedRows: T[];
  sort: SortState;
  toggleSort: (id: string) => void;
  setSort: (s: SortState) => void;
  colWidths: Record<string, number>;
  startResize: (id: string, e: ReactMouseEvent) => void;
  resetLayout: () => void;
  // Keyboard nav (UX#4). Always present; inert (selected = -1, no key
  // bindings) unless the caller supplied onOpen. Spread `rowProps(i)` on each
  // <tr> for data-row-idx + the .row-selected accent.
  nav: TableNav<T>;
  rowProps: (index: number) => { 'data-row-idx': number; className?: string };
}

export function useDataTable<T>({ storageKey, columns, rows, initialSort, serverSort, onSortChange, urlSortFallback, onOpen, searchRef }: {
  storageKey: string;
  columns: DataTableColumn<T>[];
  rows: T[];
  initialSort?: SortState;
  // serverSort (v0.8.251) — for server-paged tables (Services first): the
  // ORDER BY runs on the backend, so the hook keeps EVERY piece of the sort
  // UX — URL `s_<storageKey>` param, localStorage persistence, header click /
  // arrow semantics, all identical to client mode — but never reorders rows
  // itself: `sortedRows` is the `rows` prop verbatim. The page watches the
  // returned `sort` (or supplies onSortChange) and re-fetches with it. A
  // column still needs `sortValue` to be click-sortable; in this mode the
  // accessor is never invoked — it's the sortable marker + naturalDir carrier.
  serverSort?: boolean;
  // Fired with the new state on every sort change the hook applies — header
  // click, programmatic setSort, or an inbound URL (back/forward) restore.
  // Alternative to watching the returned `sort` in an effect dep; both see
  // the same state.
  onSortChange?: (s: SortState) => void;
  // Back-compat URL bridge: treated as an inbound URL sort when the
  // `s_<storageKey>` param is absent — ranks ABOVE localStorage (a shared
  // link's intent beats the viewer's personal default) but BELOW
  // `s_<storageKey>`. Services feeds decodeLegacyServicesSort() through this
  // so pre-v0.8.251 `?sort=&dir=` links keep landing on the sender's sort.
  urlSortFallback?: SortState | null;
  // When provided, the table gains app-wide keyboard nav: j/k move row
  // selection, Enter/o open the row (calls onOpen), gg/G jump, Esc clears,
  // and "/" focuses searchRef. Omit for a plain display table. (UX#4)
  onOpen?: (row: T, index: number) => void;
  searchRef?: RefObject<HTMLInputElement | null>;
}): DataTable<T> {
  const sortLSKey = dtSortKey(storageKey);
  const widthLSKey = dtWidthKey(storageKey);
  // Sort is shareable (UX#3): the URL param `s_<storageKey>` wins so a copied
  // link reproduces the exact sort; else the urlSortFallback bridge (an OLD
  // URL schema decoded by the page — still link intent, so it outranks the
  // viewer's own default); else the operator's personal localStorage default;
  // else initialSort. Namespaced by storageKey so two tables on one page
  // never collide. Writes hit BOTH the URL (replace — no history spam) and
  // localStorage. Widths stay localStorage-only (per-browser ergonomics, not
  // view state worth sharing).
  const [searchParams, setSearchParams] = useSearchParams();
  const urlKey = `s_${storageKey}`;
  const urlSort = searchParams.get(urlKey);
  const [sort, setSortState] = useState<SortState>(() =>
    parseSortParam(urlSort) ?? urlSortFallback ?? getItem(sortLSKey, initialSort ?? { id: null, dir: 'desc' }));
  const [colWidths, setColWidths] = useState<Record<string, number>>(() =>
    getItem(widthLSKey, {}));

  useEffect(() => {
    setItem(sortLSKey, sort);
  }, [sort, sortLSKey]);
  useEffect(() => {
    setItem(widthLSKey, colWidths);
  }, [colWidths, widthLSKey]);

  // Apply a sort to state + URL, then notify the page (serverSort pages
  // re-fetch off this — or off the returned `sort`, same thing).
  const setSort = useCallback((s: SortState) => {
    setSortState(s);
    setSearchParams(prev => {
      const next = new URLSearchParams(prev);
      const v = formatSortParam(s);
      if (v) next.set(urlKey, v);
      else next.delete(urlKey);
      return next;
    }, { replace: true });
    onSortChange?.(s);
  }, [setSearchParams, urlKey, onSortChange]);

  // Back/forward — or an inbound shared link — changes the URL sort: restore
  // it into state. Guarded to fire only on a genuine difference, which also
  // stops a loop with setSort's own write. onSortChange fires here too so a
  // serverSort page that relies on the callback re-fetches on back/forward.
  useEffect(() => {
    const fromUrl = parseSortParam(urlSort);
    if (fromUrl && (fromUrl.id !== sort.id || fromUrl.dir !== sort.dir)) {
      setSortState(fromUrl);
      onSortChange?.(fromUrl);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [urlSort]);

  const toggleSort = useCallback((id: string) => {
    // resolveToggle is the pure half (lib/dataTable.ts): null = column
    // unknown / not sortable → no-op, no state write, no callback.
    const next = resolveToggle(columns, sort, id);
    if (next) setSort(next);
  }, [columns, sort, setSort]);

  const startResize = useCallback((id: string, e: ReactMouseEvent) => {
    e.preventDefault();
    e.stopPropagation();
    const col = columns.find(c => c.id === id);
    const startX = e.clientX;
    const startW = colWidths[id] ?? col?.width ?? DEFAULT_W;
    const min = col?.minWidth ?? DEFAULT_MIN;
    const onMove = (ev: MouseEvent) => {
      const w = Math.max(min, startW + (ev.clientX - startX));
      setColWidths(s => ({ ...s, [id]: w }));
    };
    const onUp = () => {
      window.removeEventListener('mousemove', onMove);
      window.removeEventListener('mouseup', onUp);
    };
    window.addEventListener('mousemove', onMove);
    window.addEventListener('mouseup', onUp);
  }, [columns, colWidths]);

  const resetLayout = useCallback(() => setColWidths({}), []);

  // serverSort mode returns `rows` verbatim (reference-equal) — the
  // backend's ORDER BY already shaped the page; see computeSortedRows.
  const sortedRows = useMemo(
    () => computeSortedRows(rows, columns, sort, !!serverSort),
    [rows, sort, columns, serverSort],
  );

  // App-wide keyboard nav (UX#4). useTableNav owns the selected index + j/k/
  // gg/G/Enter/o/Esc bindings + auto-scroll; inert when onOpen is absent (no
  // bindings) so a plain display table doesn't capture the keys. "/" focuses
  // the page filter when both onOpen + searchRef are supplied.
  const nav = useTableNav<T>(sortedRows, { onOpen, enabled: !!onOpen, pageId: storageKey });
  useShortcuts(
    onOpen && searchRef
      ? [{ keys: '/', label: 'Focus filter', group: 'Lists', handler: () => searchRef.current?.focus() }]
      : [],
    [onOpen, searchRef, storageKey],
  );
  const rowProps = useCallback(
    (index: number) => ({
      'data-row-idx': index,
      className: nav.selected === index ? 'row-selected' : undefined,
    }),
    [nav],
  );

  return { columns, sortedRows, sort, toggleSort, setSort, colWidths, startResize, resetLayout, nav, rowProps };
}

// ColResizeHandle — drop-in resize grip for tables that keep their OWN
// <th>s (e.g. server-sorted Services, non-sortable LogTable) but still
// want the shared column-resize behaviour. The host <th> must be
// position:relative so the absolutely-positioned .col-resize-handle (see
// globals.css) anchors to its right edge. mousedown starts the drag;
// click is stopped so the handle can live inside a sort-on-click <th>
// without triggering a sort. (v0.7.54)
export function ColResizeHandle<T>({ dt, colId }: { dt: DataTable<T>; colId: string }) {
  return <span className="col-resize-handle" onMouseDown={e => dt.startResize(colId, e)} onClick={e => e.stopPropagation()} title="Drag to resize" />;
}

// DataTableColgroup — emits the <colgroup> that makes table-layout:fixed
// respect (and resize) per-column widths. `leading` is the px width of any
// leading non-data columns (expand chevron, checkbox) rendered before the
// managed columns; `trailing` is the same for non-data columns rendered
// after them (actions cell, "+ Add column" manager — v0.8.306).
export function DataTableColgroup<T>({ dt, leading, trailing }: { dt: DataTable<T>; leading?: number[]; trailing?: number[] }) {
  return (
    <colgroup>
      {(leading ?? []).map((w, i) => <col key={`lead-${i}`} style={{ width: w }} />)}
      {dt.columns.filter(c => !c.headerHidden).map(c => (
        <col key={c.id} style={{ width: dt.colWidths[c.id] ?? c.width ?? DEFAULT_W }} />
      ))}
      {(trailing ?? []).map((w, i) => <col key={`trail-${i}`} style={{ width: w }} />)}
    </colgroup>
  );
}

// DataTableHead — the full <thead><tr> built from the column defs: each
// sortable column is clickable (▲▼↕ glyph + aria-sort, matching the
// house .sortable/.sorted CSS) and every column gets a right-edge resize
// handle. `leading` slots in any non-data header cells (e.g. expand col);
// `trailing` slots them AFTER the managed columns — the caller owns that
// <th>, so a dropdown affordance (Explore's "+ Add column" manager) isn't
// clipped by the managed cells' overflow:hidden (v0.8.306). `renderLabel`
// lets a caller decorate a header label (e.g. LogTable's hover-×
// remove-column affordance) without touching the pure core's string
// label type.
export function DataTableHead<T>({ dt, leading, trailing, renderLabel }: {
  dt: DataTable<T>;
  leading?: ReactNode;
  trailing?: ReactNode;
  renderLabel?: (c: DataTable<T>['columns'][number]) => ReactNode;
}) {
  return (
    <thead>
      <tr>
        {leading}
        {dt.columns.filter(c => !c.headerHidden).map(c => {
          const sortable = !!c.sortValue;
          const active = dt.sort.id === c.id;
          const align = c.align ?? (c.numeric ? 'right' : 'left');
          const cls = [c.numeric ? 'num' : '', sortable ? 'sortable' : '', active ? 'sorted' : '', c.stickyRight ? 'sticky-right' : '']
            .filter(Boolean).join(' ');
          return (
            <th key={c.id}
                className={cls || undefined}
                onClick={sortable ? () => dt.toggleSort(c.id) : undefined}
                aria-sort={active ? (dt.sort.dir === 'asc' ? 'ascending' : 'descending') : (sortable ? 'none' : undefined)}
                style={{
                  // sticky-right columns take their position from the class
                  // (position:sticky + right:0); inline 'relative' would win
                  // over it. Both variants anchor the absolute resize handle.
                  textAlign: align, position: c.stickyRight ? undefined : 'relative',
                  overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                  userSelect: 'none',
                }}>
              {renderLabel ? renderLabel(c) : c.label}
              {sortable && (
                <span className="sort-arrow">{active ? (dt.sort.dir === 'desc' ? '▼' : '▲') : '↕'}</span>
              )}
              <span className="col-resize-handle"
                    onMouseDown={e => dt.startResize(c.id, e)}
                    onClick={e => e.stopPropagation()}
                    title="Drag to resize" />
            </th>
          );
        })}
        {trailing}
      </tr>
    </thead>
  );
}
