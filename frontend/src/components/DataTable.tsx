import {
  useCallback, useEffect, useMemo, useState,
  type MouseEvent as ReactMouseEvent, type ReactNode,
} from 'react';
import {
  nextSort, sortRows,
  type DataTableColumn, type SortDir, type SortState,
} from '@/lib/dataTable';

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
// Sort is CLIENT-SIDE (for the common "fetched array of ≤ a few k
// rows" table). Server-paged tables (Services/Traces) keep their
// server sort but can still adopt the resize half. Sort + width state
// persist to localStorage under the storageKey, so the operator's
// layout survives reloads — same contract as the Traces v0.7.47 cols.

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
}

function loadJSON<T>(key: string, fallback: T): T {
  try {
    const s = localStorage.getItem(key);
    return s ? (JSON.parse(s) as T) : fallback;
  } catch {
    return fallback;
  }
}

export function useDataTable<T>({ storageKey, columns, rows, initialSort }: {
  storageKey: string;
  columns: DataTableColumn<T>[];
  rows: T[];
  initialSort?: SortState;
}): DataTable<T> {
  const sortLSKey = `dt.${storageKey}.sort`;
  const widthLSKey = `dt.${storageKey}.widths`;
  const [sort, setSort] = useState<SortState>(() =>
    loadJSON(sortLSKey, initialSort ?? { id: null, dir: 'desc' }));
  const [colWidths, setColWidths] = useState<Record<string, number>>(() =>
    loadJSON(widthLSKey, {}));

  useEffect(() => {
    try { localStorage.setItem(sortLSKey, JSON.stringify(sort)); } catch { /* private mode */ }
  }, [sort, sortLSKey]);
  useEffect(() => {
    try { localStorage.setItem(widthLSKey, JSON.stringify(colWidths)); } catch { /* private mode */ }
  }, [colWidths, widthLSKey]);

  const toggleSort = useCallback((id: string) => {
    const col = columns.find(c => c.id === id);
    if (!col || !col.sortValue) return;
    setSort(cur => nextSort(cur, col));
  }, [columns]);

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

  const sortedRows = useMemo(() => {
    const col = sort.id ? columns.find(c => c.id === sort.id) : undefined;
    return sortRows(rows, col, sort.dir);
  }, [rows, sort, columns]);

  return { columns, sortedRows, sort, toggleSort, setSort, colWidths, startResize, resetLayout };
}

// DataTableColgroup — emits the <colgroup> that makes table-layout:fixed
// respect (and resize) per-column widths. `leading` is the px width of any
// leading non-data columns (expand chevron, checkbox) rendered before the
// managed columns.
export function DataTableColgroup<T>({ dt, leading }: { dt: DataTable<T>; leading?: number[] }) {
  return (
    <colgroup>
      {(leading ?? []).map((w, i) => <col key={`lead-${i}`} style={{ width: w }} />)}
      {dt.columns.filter(c => !c.headerHidden).map(c => (
        <col key={c.id} style={{ width: dt.colWidths[c.id] ?? c.width ?? DEFAULT_W }} />
      ))}
    </colgroup>
  );
}

// DataTableHead — the full <thead><tr> built from the column defs: each
// sortable column is clickable (▲▼↕ glyph + aria-sort, matching the
// house .sortable/.sorted CSS) and every column gets a right-edge resize
// handle. `leading` slots in any non-data header cells (e.g. expand col).
export function DataTableHead<T>({ dt, leading }: { dt: DataTable<T>; leading?: ReactNode }) {
  return (
    <thead>
      <tr>
        {leading}
        {dt.columns.filter(c => !c.headerHidden).map(c => {
          const sortable = !!c.sortValue;
          const active = dt.sort.id === c.id;
          const align = c.align ?? (c.numeric ? 'right' : 'left');
          const cls = [c.numeric ? 'num' : '', sortable ? 'sortable' : '', active ? 'sorted' : '']
            .filter(Boolean).join(' ');
          return (
            <th key={c.id}
                className={cls || undefined}
                onClick={sortable ? () => dt.toggleSort(c.id) : undefined}
                aria-sort={active ? (dt.sort.dir === 'asc' ? 'ascending' : 'descending') : (sortable ? 'none' : undefined)}
                style={{
                  textAlign: align, position: 'relative',
                  overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                  userSelect: 'none',
                }}>
              {c.label}
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
      </tr>
    </thead>
  );
}
