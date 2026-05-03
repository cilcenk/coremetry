import type { FilterExpr, TimeRange } from './types';

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

/** Build a URLSearchParams, omitting empty/default values. */
export function buildQuery(entries: Array<[string, string | number | undefined | null | false]>): string {
  const u = new URLSearchParams();
  for (const [k, v] of entries) {
    if (v === undefined || v === null || v === '' || v === false) continue;
    u.set(k, String(v));
  }
  return u.toString();
}
