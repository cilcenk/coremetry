// logFilters — structured filter state for /logs (Kibana Discover
// pill model). Filters used to live embedded in the free-text KQL
// string and were toggled by regex surgery (toggleSearchClause);
// this module makes each field filter a first-class object so the
// pill bar can negate / disable / remove without string parsing.
//
// The backend contract is unchanged: pills + free text compile back
// into ONE KQL/Lucene string (compileSearch) right before the query
// goes out, so /api/logs, the histogram, live tail and the Kibana
// deep-link all see exactly what they saw before.

export interface LogFilter {
  key: string;
  value: string;
  negated: boolean;   // NOT key:value
  disabled: boolean;  // kept in the bar but excluded from the query
}

// Always wrap values in double quotes — Lucene treats many
// characters as operators (`-`, `/`, `:`, `*`, etc.) and a bare
// hostname like "my-host-7f-abc" is parsed as a boolean expression
// rather than a literal. Inside quotes only `\` and `"` are
// special. (v0.5.230 caught a host filter never matching.)
export function phraseQuote(s: string): string {
  return `"${s.replace(/\\/g, '\\\\').replace(/"/g, '\\"')}"`;
}

// Compile pills + free text into the single query string the
// backend understands. Disabled pills are skipped. The free-text
// part is parenthesised when it contains a top-level OR so the
// implicit AND-join can't re-associate it (`x:"1" AND a OR b`
// would parse as `(x:"1" AND a) OR b`).
export function compileSearch(filters: LogFilter[], query: string): string {
  const parts = filters
    .filter(f => !f.disabled)
    .map(f => `${f.negated ? 'NOT ' : ''}${f.key}:${phraseQuote(f.value)}`);
  const q = query.trim();
  if (q) parts.push(parts.length > 0 && /\bOR\b/i.test(q) ? `(${q})` : q);
  return parts.join(' AND ');
}

// Toggle semantics mirror the old regex version: same key+value
// with the same polarity → remove (exact ⊕→⊕ toggles off); same
// key+value with the other polarity → flip in place (⊕→⊖ doesn't
// pile up duplicates). A flip also re-enables a disabled pill —
// the operator just acted on it, so it must visibly take effect.
export function toggleFilter(
  filters: LogFilter[], key: string, value: string, negated: boolean,
): LogFilter[] {
  const idx = filters.findIndex(f => f.key === key && f.value === value);
  if (idx === -1) return [...filters, { key, value, negated, disabled: false }];
  if (filters[idx].negated === negated) return filters.filter((_, i) => i !== idx);
  return filters.map((f, i) => (i === idx ? { ...f, negated, disabled: false } : f));
}

// URL form: compact JSON tuples [key, value, negated, disabled]
// with 0/1 flags — keeps ?filters= short enough for Copy link and
// SavedViewsBar (both persist the raw query string).
export function encodeFiltersParam(filters: LogFilter[]): string {
  if (filters.length === 0) return '';
  return JSON.stringify(filters.map(f => [f.key, f.value, f.negated ? 1 : 0, f.disabled ? 1 : 0]));
}

export function parseFiltersParam(raw: string | null | undefined): LogFilter[] {
  if (!raw) return [];
  try {
    const arr = JSON.parse(raw);
    if (!Array.isArray(arr)) return [];
    const out: LogFilter[] = [];
    for (const e of arr) {
      if (!Array.isArray(e) || typeof e[0] !== 'string' || typeof e[1] !== 'string') continue;
      out.push({ key: e[0], value: e[1], negated: !!e[2], disabled: !!e[3] });
    }
    return out;
  } catch {
    return [];
  }
}
