// ui/DataTable — v0.10.246 DataTable dilim 1 (audit §9, §12 dilim 1).
//
// Tek giriş noktası. Bugün mevcut yapıştırıcıyı YENİDEN İHRAÇ eder;
// DataTable.tsx / VirtualTable.tsx'in fiziksel taşınması son dilimdir
// (79 içe aktaran dosya + yolla okuyan kapı testleri). Yeni sayfalar
// buradan içe aktarır; eski yollar taşınana dek çalışır (shim değil —
// aynı modüller).

export { useDataTable, DataTableHead, DataTableColgroup, ColResizeHandle, ResetLayoutButton, resolveInitialSort } from '../../DataTable';
export type { DataTable } from '../../DataTable';
export { VirtualTable } from '../VirtualTable';
export type { VirtualTableProps } from '../VirtualTable';
export type { DataTableColumn, SortState, SortDir } from '@/lib/dataTable';
export { columnLayoutSig, visibleColumns, nextSort, parseSortParam, formatSortParam } from '@/lib/dataTable';
export type { ColumnDef, CellContext, ColumnModel, ColumnSource, ColumnSpec, ColumnModelBinding, SelectionBinding, ServerBinding } from './types';
export {
  defaultColumnModel, reconcileColumnModel, visibleColumnIds, toggleHidden, moveColumnTo,
  parseColsParam, modelFromVisible, resolveColumnModel, serializeColumnModel, parseColumnModel, orderColumnsByModel,
} from '@/lib/columnModel';
export { EMPTY_SELECTION, toggleRow, rangeSelect, selectAll, pruneSelection } from '@/lib/rowSelection';
export type { SelectionState } from '@/lib/rowSelection';
