// traceExtrasMerge — pure helpers for the /traces attribute-column
// enrichment flow (FAZ 2, docs/audit/traces-attribute-columns.md §6B).
//
// The page's list fetch is always NARROW (no extras); when attribute
// columns are selected, a second light call fetches values for exactly the
// visible trace ids. These helpers keep that loop convergent: after one
// merge, every requested key exists on every row ('' fallback), so
// missingExtraKeys returns [] and the effect never refetches.
import type { TraceRow } from './types';

// missingExtraKeys returns the requested keys that at least one row has not
// been enriched with yet. Key PRESENCE (not truthiness) is the marker —
// a fetched-but-absent attribute is stored as '' and must not refetch.
export function missingExtraKeys(rows: TraceRow[], requested: string[]): string[] {
  return requested.filter(k => rows.some(r => !r.extras || !(k in r.extras)));
}

// mergeTraceExtras stamps a phase-2 extras response onto a page of rows.
// Every requested key lands on every REQUESTED row: the server's value when
// present, '' otherwise. requestedIds scopes the stamp (v0.9.195 review-fix):
// a stale response landing after the page was replaced must NOT mark the new
// page's rows as fetched-empty — rows outside the original request keep
// their keys absent, so the enrichment effect refetches them next pass.
// Rows that don't change keep their object identity so React row renders
// are stable.
export function mergeTraceExtras(
  rows: TraceRow[],
  requested: string[],
  extras: Record<string, Record<string, string>>,
  requestedIds: ReadonlySet<string>,
): TraceRow[] {
  return rows.map(r => {
    if (!requestedIds.has(r.traceId)) return r;
    const got = extras[r.traceId];
    let changed = false;
    const next: Record<string, string> = { ...r.extras };
    for (const k of requested) {
      const v = got?.[k] ?? next[k] ?? '';
      if (next[k] !== v || !(k in next)) {
        next[k] = v;
        changed = true;
      }
    }
    return changed ? { ...r, extras: next } : r;
  });
}
