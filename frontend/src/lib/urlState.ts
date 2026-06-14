import type { FilterExpr, FilterGroup, TimeRange } from './types';

// ─────────────────────────────────────────────────────────────────────────────
// Helpers for serialising Explore-style page state to/from the URL query
// string. Stable, human-readable where possible.
// ─────────────────────────────────────────────────────────────────────────────

/** Encode a TimeRange. Preset → `1h`. Custom → `custom:<fromMs>-<toMs>`. */
export function encodeRange(r: TimeRange): string {
  if (r.preset === 'custom' && r.fromMs && r.toMs) {
    return `custom:${r.fromMs}-${r.toMs}`;
  }
  return r.preset;
}

export function decodeRange(s: string | null | undefined, fallback: TimeRange): TimeRange {
  if (!s) return fallback;
  if (s.startsWith('custom:')) {
    const [from, to] = s.slice('custom:'.length).split('-').map(n => parseInt(n, 10));
    if (from > 0 && to > from) return { preset: 'custom', fromMs: from, toMs: to };
    return fallback;
  }
  return { preset: s };
}

/** Encode FilterExpr[] as compact JSON. */
export function encodeFilters(f: FilterExpr[]): string {
  return f.length ? JSON.stringify(f) : '';
}

export function decodeFilters(s: string | null | undefined): FilterExpr[] {
  if (!s) return [];
  try {
    const v = JSON.parse(s);
    return Array.isArray(v) ? (v as FilterExpr[]) : [];
  } catch {
    return [];
  }
}

// ── Grouped AND/OR builder codec (v0.8.x trace-query gap-2) ───────────────────
// FilterGroup is the additive, default-off upgrade. A group is "flat-AND" —
// indistinguishable from the legacy FilterExpr[] path — when its join is AND
// and it has no nested groups. encodeFilterGroup returns '' for a flat-AND
// group so the URL keeps using the legacy `filters=` param (back-compat:
// existing saved views / shared URLs are byte-identical); only a genuine OR /
// nested group serialises to the `filterGroup=` param.

/** True when the group adds nothing beyond a legacy flat-AND chip row. */
export function isFlatAndGroup(g: FilterGroup | null | undefined): boolean {
  if (!g) return true;
  if (g.groups && g.groups.length > 0) return false;
  return (g.join ?? 'AND') === 'AND';
}

/**
 * Encode a FilterGroup for the `filterGroup=` URL param. Returns '' when the
 * group is flat-AND (the flat `filters=` param carries it instead) or empty,
 * so the grouped param only appears for real OR / nested queries.
 */
export function encodeFilterGroup(g: FilterGroup | null | undefined): string {
  if (!g) return '';
  if (isFlatAndGroup(g)) return '';
  // Strip empty leaf/group noise so the URL stays compact + stable.
  const filters = (g.filters ?? []).filter(f => f.k && f.k.trim());
  const groups = (g.groups ?? [])
    .map(sub => ({ join: sub.join ?? 'AND', filters: (sub.filters ?? []).filter(f => f.k && f.k.trim()) }))
    .filter(sub => sub.filters.length > 0);
  if (filters.length === 0 && groups.length === 0) return '';
  const out: FilterGroup = { join: g.join ?? 'AND', filters };
  if (groups.length > 0) out.groups = groups;
  return JSON.stringify(out);
}

/** Decode the `filterGroup=` URL param; null when absent / malformed. */
export function decodeFilterGroup(s: string | null | undefined): FilterGroup | null {
  if (!s) return null;
  try {
    const v = JSON.parse(s);
    if (!v || typeof v !== 'object' || !Array.isArray(v.filters)) return null;
    const g: FilterGroup = {
      join: v.join === 'OR' ? 'OR' : 'AND',
      filters: v.filters as FilterExpr[],
    };
    if (Array.isArray(v.groups) && v.groups.length > 0) {
      g.groups = (v.groups as unknown[])
        .map(sub => {
          const o = sub as { join?: string; filters?: unknown };
          return {
            join: o.join === 'OR' ? 'OR' : 'AND',
            filters: Array.isArray(o.filters) ? (o.filters as FilterExpr[]) : [],
          } as FilterGroup;
        })
        .filter(sub => sub.filters.length > 0);
    }
    return g;
  } catch {
    return null;
  }
}

/** Build a URLSearchParams, omitting empty/default values. */
export function buildQuery(entries: Array<[string, string | number | undefined | null | false]>): string {
  const u = new URLSearchParams();
  for (const [k, v] of entries) {
    if (v === undefined || v === null || v === '' || v === false) continue;
    u.set(k, String(v));
  }
  return u.toString();
}
