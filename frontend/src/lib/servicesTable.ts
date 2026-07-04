import type { Service } from './types';
import type { DataTableColumn, SortState } from './dataTable';

// servicesTable — pure column defs + URL-sort helpers for pages/Services.tsx
// (v0.8.251). /services was the primitive's resize-only adopter (v0.7.54)
// with a hand-rolled SortKey/NATURAL_DIR/SortTh header on the side; the page
// now runs useDataTable in serverSort mode instead, and everything the node
// vitest harness should pin — column ids that double as the backend's ?sort=
// keys, the old-link bridge, the ORDER BY sanitizer — lives here, out of the
// page file.

// SERVICE_COLS — column defs for the shared DataTable primitive. The page
// runs serverSort mode (clicking a header re-fetches a sorted page off
// service_summary_5m — CH does the ORDER BY before LIMIT/OFFSET, so the page
// reflects the GLOBAL rank), so each `sortValue` accessor is never invoked
// for ordering: it marks the column click-sortable and carries the naturalDir
// click semantics. Ids mirror the backend's ?sort= keys 1:1 — the same keys
// the pre-v0.8.251 SortKey union enumerated.
//
// Natural directions (unchanged from the old NATURAL_DIR map): name is
// alphabetical so 'asc'; apdex is a satisfaction score so 'asc' surfaces the
// WORST services first; the volume/latency columns keep the default 'desc'
// (biggest first).
export const SERVICE_COLS: DataTableColumn<Service>[] = [
  { id: 'name',      label: 'Service',    width: 280, sortValue: s => s.name, naturalDir: 'asc' },
  { id: 'spanCount', label: 'Spans',      width: 130, align: 'right', sortValue: s => s.spanCount },
  { id: 'errorRate', label: 'Error rate', width: 130, align: 'right', sortValue: s => s.errorRate },
  { id: 'avg',       label: 'Avg',        width: 120, align: 'right', sortValue: s => s.avgDurationMs },
  { id: 'p99',       label: 'P99',        width: 120, align: 'right', sortValue: s => s.p99DurationMs },
  { id: 'apdex',     label: 'Apdex',      width: 100, align: 'right', sortValue: s => s.apdex, naturalDir: 'asc' },
];

// The landing sort — error-rate first, because the operator's eye goes to
// what's failing. Same default the page has shipped since v0.3.0.
export const DEFAULT_SERVICES_SORT: SortState = { id: 'errorRate', dir: 'desc' };

// sanitizeServicesSort — dt.sort → the ?sort=/&dir= pair /api/services
// accepts. The hook's state survives in localStorage + the URL, so a stale
// entry (an id from an older column schema, or a hand-edited link) can carry
// an id that isn't a Services column; falling back to the default pair keeps
// an unknown ORDER BY key from ever reaching the backend. Falls back as a
// PAIR — an unknown id's dir is just as untrusted as the id.
export function sanitizeServicesSort(sort: SortState): { sort: string; dir: 'asc' | 'desc' } {
  if (sort.id && SERVICE_COLS.some(c => c.id === sort.id)) {
    return { sort: sort.id, dir: sort.dir };
  }
  return { sort: DEFAULT_SERVICES_SORT.id as string, dir: DEFAULT_SERVICES_SORT.dir };
}

// decodeLegacyServicesSort — back-compat bridge for pre-v0.8.251 links.
// Before the page adopted the primitive's `s_services` URL param, the
// deep-link shape for a sorted /services view was the backend's own
// `?sort=<col>&dir=<asc|desc>` pair. Decode that into a SortState the hook
// seeds from via urlSortFallback: it ranks BELOW `s_services` (new schema
// wins when both are present) but ABOVE the viewer's localStorage — a shared
// link's intent beats the recipient's personal default. Returns null when
// the legacy param is absent or names an unknown column; a missing /
// malformed dir falls back to the column's natural direction, matching what
// a header click on that column would have produced. READ-only: new writes
// always use `s_services`, so the old params age out of circulating links.
export function decodeLegacyServicesSort(search: string): SortState | null {
  const p = new URLSearchParams(search);
  const id = p.get('sort');
  if (!id) return null;
  const col = SERVICE_COLS.find(c => c.id === id);
  if (!col) return null;
  const dir = p.get('dir');
  return { id, dir: dir === 'asc' || dir === 'desc' ? dir : (col.naturalDir ?? 'desc') };
}
