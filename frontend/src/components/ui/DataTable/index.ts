// ui/DataTable — v0.10.246 DataTable dilim 1 (audit §9, §12 dilim 1).
//
// Tek giriş noktası. v0.10.317 (dilim 7): DataTable.tsx / VirtualTable.tsx
// FİZİKSEL olarak bu klasörde; eski yollar YOK (shim yok — dataTableBarrel
// kapısı eski importu kırmızıya çevirir). Tüm sayfalar buradan içe aktarır.

export { useDataTable, DataTableHead, DataTableColgroup, ColResizeHandle, ResetLayoutButton, resolveInitialSort, headKeyDown, HEAD_RESIZE_STEP_PX } from './DataTable';
export type { DataTable, DataTableSelection, DataTableServer } from './DataTable';
export { VirtualTable } from './VirtualTable';
export type { VirtualTableProps } from './VirtualTable';
export type { DataTableColumn, SortState, SortDir } from '@/lib/dataTable';
export { columnLayoutSig, visibleColumns, nextSort, parseSortParam, formatSortParam } from '@/lib/dataTable';
export type { ColumnDef, CellContext, ColumnModel, ColumnSource, ColumnSpec, ColumnModelBinding, SelectionBinding, ServerBinding } from './types';
export {
  defaultColumnModel, reconcileColumnModel, visibleColumnIds, toggleHidden, moveColumnTo,
  parseColsParam, modelFromVisible, resolveColumnModel, serializeColumnModel, parseColumnModel, orderColumnsByModel,
} from '@/lib/columnModel';
export { EMPTY_SELECTION, toggleRow, rangeSelect, selectAll, pruneSelection } from '@/lib/rowSelection';
export type { SelectionState } from '@/lib/rowSelection';
