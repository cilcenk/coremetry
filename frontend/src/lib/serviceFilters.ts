import type { Service } from './types';

export interface LocalDisplayFilters {
  errorsOnly: boolean;
  minSpans: number; // NaN = no filter
  minP99: number;   // NaN = no filter
}

// passesLocalDisplayFilters — the ONLY filters applied client-side to the
// already server-filtered + paginated Services page: errors-only plus the
// numeric min-spans / min-p99 refinements.
//
// v0.7.29 — Operator-reported: "filtering shows 'no services'; only the Search
// button brings them." The page used to ALSO filter the loaded 50-row page
// locally by the typed service-name DRAFT. Because that page is just the
// server's top-50, any service outside it failed the local match and the list
// showed "no services" until the operator committed the server search via
// Search/Enter. Name + team filtering is now exclusively server-side
// (committedFilter → ?name across ALL services; team dropdowns → server-
// resolved allowlist), so it is correct across every page and never empties the
// list mid-type. This helper therefore deliberately does NOT look at the
// service name — that's the regression guard.
export function passesLocalDisplayFilters(
  s: Pick<Service, 'errorCount' | 'errorRate' | 'spanCount' | 'p99DurationMs'>,
  f: LocalDisplayFilters,
): boolean {
  if (f.errorsOnly && !(s.errorCount > 0 || s.errorRate > 0)) return false;
  if (!isNaN(f.minSpans) && s.spanCount < f.minSpans) return false;
  if (!isNaN(f.minP99) && s.p99DurationMs < f.minP99) return false;
  return true;
}
