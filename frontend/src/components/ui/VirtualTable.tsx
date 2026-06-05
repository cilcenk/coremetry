import { useRef, type ReactNode } from 'react';
import { useVirtualizer } from '@tanstack/react-virtual';
import { DataTableColgroup, DataTableHead, type DataTable } from '../DataTable';

// VirtualTable — windowed rendering for a useDataTable table (v0.8.6 Phase 0).
//
// VirtualList windows a flat <div> list; this windows a real <table> so the
// shared sort/resize header (DataTableColgroup + DataTableHead) and
// table-layout:fixed column sizing keep working. It uses the spacer-row
// technique: one <tr> of height=offsetTop, the visible rows, one <tr> of
// height=offsetBottom — so only ~(visible + overscan) rows touch the DOM while
// the scrollbar still spans the full set. At 50k rows this holds 60fps where a
// plain sortedRows.map() (even with content-visibility) stutters.
//
// Rows are assumed UNIFORM height (the Coremetry house row is 36px) — fixed
// estimateSize avoids the measure-every-row layout thrash. The scroll container
// owns the height; the sticky header is styled via the .vt-scroll CSS rule.
//
// renderRow returns the <td> CELLS only; VirtualTable wraps them in the <tr>
// (applying useDataTable's rowProps for keyboard-nav selection + your optional
// rowClassName for severity tints).

export interface VirtualTableProps<T> {
  dt: DataTable<T>;
  // Pixel height of the scroll viewport. A number or any CSS length.
  height: number | string;
  // Fixed row height in px. Match the real row height (default 36).
  rowHeight?: number;
  // Extra rows rendered above/below the viewport so fast scrolls don't blank.
  overscan?: number;
  // Leading non-data column widths (expand chevron / checkbox), mirrors
  // DataTableColgroup's `leading`.
  leading?: number[];
  // Leading header cell(s) for those columns.
  leadingHead?: ReactNode;
  // Render the <td> cells for one row.
  renderRow: (row: T, index: number) => ReactNode;
  // Stable per-row key — REQUIRED for correct reconciliation when rows reorder
  // (sort) or prepend (live tail). Defaults to the row index otherwise.
  getRowKey?: (row: T, index: number) => string | number;
  // Optional per-row class (e.g. 'log-error' severity tint) merged with the
  // rowProps className.
  rowClassName?: (row: T, index: number) => string | undefined;
  onRowClick?: (row: T, index: number) => void;
  className?: string;
  emptyMessage?: ReactNode;
}

export function VirtualTable<T>({
  dt, height, rowHeight = 36, overscan = 12,
  leading, leadingHead, renderRow, getRowKey, rowClassName, onRowClick,
  className, emptyMessage,
}: VirtualTableProps<T>) {
  const parentRef = useRef<HTMLDivElement>(null);
  const rows = dt.sortedRows;

  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => parentRef.current,
    estimateSize: () => rowHeight,
    overscan,
    getItemKey: getRowKey ? (i) => getRowKey(rows[i], i) : undefined,
  });

  const items = virtualizer.getVirtualItems();
  const totalSize = virtualizer.getTotalSize();
  const padTop = items.length ? items[0].start : 0;
  const padBottom = items.length ? totalSize - items[items.length - 1].end : 0;
  const colCount = (leading?.length ?? 0) + dt.columns.filter(c => !c.headerHidden).length;

  return (
    <div
      ref={parentRef}
      className={['vt-scroll', className].filter(Boolean).join(' ')}
      style={{ height, overflow: 'auto', position: 'relative', contain: 'strict' }}>
      <table style={{ tableLayout: 'fixed', width: '100%' }}>
        <DataTableColgroup dt={dt} leading={leading} />
        <DataTableHead dt={dt} leading={leadingHead} />
        <tbody>
          {rows.length === 0 ? (
            <tr><td colSpan={colCount} className="vt-empty">{emptyMessage ?? 'No rows.'}</td></tr>
          ) : (
            <>
              {padTop > 0 && <tr aria-hidden style={{ height: padTop }} />}
              {items.map(vi => {
                const row = rows[vi.index];
                const { className: rpClass, ...rpRest } = dt.rowProps(vi.index);
                const cls = [rpClass, rowClassName?.(row, vi.index)].filter(Boolean).join(' ') || undefined;
                return (
                  <tr
                    key={vi.key}
                    {...rpRest}
                    className={cls}
                    style={{ height: rowHeight }}
                    onClick={onRowClick ? () => onRowClick(row, vi.index) : undefined}>
                    {renderRow(row, vi.index)}
                  </tr>
                );
              })}
              {padBottom > 0 && <tr aria-hidden style={{ height: padBottom }} />}
            </>
          )}
        </tbody>
      </table>
    </div>
  );
}
