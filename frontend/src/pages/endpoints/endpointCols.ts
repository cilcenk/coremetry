// endpointCols — pure codec for the /endpoints visible-column URL
// param (v0.8.574, audit seçenek 3). Mirrors the Logs `?cols=`
// contract (Logs.tsx colsParam): the param is OMITTED when the view
// equals the default (all columns visible) so plain URLs stay clean,
// and any garbage value falls back to the default instead of
// rendering an empty table. URL — not localStorage — per the house
// rule: column VISIBILITY changes what's on screen, so Copy link /
// SavedViewsBar must reproduce it (widths stay localStorage-only).

// parseColsParam — `?cols=` raw value → the set of visible column
// ids. null/'' → all visible (default). Unknown ids (stale links
// from a future/past column schema) are dropped; if nothing valid
// survives, fall back to all visible — a shared link must never
// produce a column-less table.
export function parseColsParam(raw: string | null, allIds: readonly string[]): Set<string> {
  if (!raw) return new Set(allIds);
  const known = new Set(allIds);
  const picked = raw.split(',').map(s => s.trim()).filter(id => known.has(id));
  return picked.length > 0 ? new Set(picked) : new Set(allIds);
}

// formatColsParam — visible set → `?cols=` value; '' when all
// columns are visible (caller deletes the param). Emits ids in
// canonical column order — not insertion order — so the same view
// always produces the same URL (shareable links dedupe cleanly).
export function formatColsParam(visible: Set<string>, allIds: readonly string[]): string {
  const inOrder = allIds.filter(id => visible.has(id));
  return inOrder.length === allIds.length ? '' : inOrder.join(',');
}
