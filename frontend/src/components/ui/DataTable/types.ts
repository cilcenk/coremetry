// ui/DataTable/types.ts — v0.10.246 DataTable dilim 1 (audit §9).
//
// ColumnDef, lib/dataTable.ts DataTableColumn'u AYNEN genişletir — saf
// çekirdek (sıralama, genişlik mühürleme, fit) değişmez; eklenenler
// renderer/erişimci/kimlik bilgileri. useDataTable seçenekleri ADDİTİF:
// hiçbiri verilmediğinde bugünkü 109 çağrı yeri bayt-bayt aynı davranır.

import type { ReactNode } from 'react';
import type { DataTableColumn, SortState } from '@/lib/dataTable';
import type { ColumnModel } from '@/lib/columnModel';

export type { ColumnModel, ColumnSource, ColumnSpec } from '@/lib/columnModel';

export interface CellContext<T> {
  row: T;
  rowIndex: number;
  column: ColumnDef<T>;
  /** Sayfa/pencere düzeyi bağlam (ör. DurationCell visibleMax). */
  meta?: Record<string, unknown>;
}

export interface ColumnDef<T> extends DataTableColumn<T> {
  /** Ham değer (kopya/CSV); sortValue'dan bağımsız. */
  accessor?: (row: T) => unknown;
  /** Hücre renderer'ı; yoksa TextCell. */
  cell?: (ctx: CellContext<T>) => ReactNode;
  /** false = kimlik kolonu (Traces: time, operation) — gizlenemez. */
  hideable?: boolean;
  reorderable?: boolean;
  /** Sunucu ORDER BY anahtarı (istemci sıralaması serverSort ile yasak). */
  serverSortKey?: string;
  /** Renderer kendi <a>'sını basar; satır-link sarmalamaz. */
  ownLink?: boolean;
}

export interface ColumnModelBinding {
  value: ColumnModel | null;
  onChange: (next: ColumnModel) => void;
}

export interface SelectionBinding<T> {
  mode: 'single' | 'multi';
  getRowId: (row: T) => string;
  value?: ReadonlySet<string>;
  onChange?: (ids: ReadonlySet<string>) => void;
}

export interface ServerBinding {
  page: number;
  pageSize: number;
  hasMore: boolean;
  onPage: (p: number) => void;
  onSort?: (s: SortState) => void;
}
