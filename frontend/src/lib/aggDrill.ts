import type { FilterExpr } from '@/lib/types';

// aggDrill — v0.9.856 (UX denetimi K11). The aggregate→list drill's filter
// arithmetic, extracted so it is testable without a page.
//
// The bug: /traces "group by attribute" grouped on e.g. CHANNEL_CODE, and
// clicking the "X100" row switched to the list view carrying only the
// SERVICE. The attribute VALUE — the entire question the operator asked —
// was dropped. The list then showed every trace of that service, the row
// count disagreed with the group's count, and the operator read the result
// as "these are X100's traces". A silently WIDENED question is worse than an
// empty one: nothing on screen says the filter is missing.
//
// The list side already supported attribute filters (advFilters → `filters=`),
// so the drill only ever had to write one.

// upsertAttrFilter — scope an existing filter set to `key = value`.
//
// REPLACES any existing predicate on the same key rather than appending.
// Filters are ANDed, so appending `CHANNEL_CODE = X200` next to a leftover
// `CHANNEL_CODE = X100` produces a contradiction that matches nothing — an
// empty list that reads, again, as "no such traces". The drill's intent is
// always "scope to THIS value", so the newest predicate on that key wins.
//
// Returns the input untouched when the key or value is empty: a groupless
// drill must not write `"" = ""` into the URL.
export function upsertAttrFilter(
  filters: FilterExpr[], key: string, value: string,
): FilterExpr[] {
  const k = key.trim();
  if (!k || !value) return filters;
  const kept = filters.filter(f => f.k.trim() !== k);
  return [...kept, { k, op: '=', v: [value] }];
}
