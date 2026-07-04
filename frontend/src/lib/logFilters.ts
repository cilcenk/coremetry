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

// extractHighlightTerms — the free-text query's bare terms and
// quoted phrases, for client-side <mark> highlighting in the
// message cell (Discover revamp 6/7). Field clauses (key:value,
// quoted or bare) are excluded — highlighting "error" because the
// operator typed level:error would light up unrelated text.
// Operators AND/OR/NOT and parens/wildcard punctuation are
// stripped. Purely client-side by design — never the ES highlight
// API (spec: "ES highlight API'sine girme").
export function extractHighlightTerms(query: string): string[] {
  const out: string[] = [];
  const re = /(-?[\w.@-]+)\s*:\s*("(?:[^"\\]|\\.)*"|\S+)|"((?:[^"\\]|\\.)*)"|(\S+)/g;
  let m: RegExpExecArray | null;
  while ((m = re.exec(query))) {
    if (m[1] !== undefined) continue; // field clause — skip key AND value
    if (m[3] !== undefined) {
      const phrase = m[3].replace(/\\(.)/g, '$1').trim();
      if (phrase.length >= 2) out.push(phrase);
      continue;
    }
    const w = m[4] ?? '';
    const up = w.toUpperCase();
    if (up === 'AND' || up === 'OR' || up === 'NOT') continue;
    const clean = w.replace(/^[(*]+/, '').replace(/[)*]+$/, '');
    if (clean.length >= 2) out.push(clean);
  }
  return [...new Set(out)];
}

// highlightSegments — split text into {text, hl} runs by case-
// insensitive term matches (earliest match wins; longest term wins
// at the same position; non-overlapping). Pure so the tokenizer is
// unit-testable; the component maps hl runs to <mark>. Scanning is
// capped so a pathological 200 KB single-line body doesn't pin the
// main thread — the tail past the cap renders unhighlighted.
const HIGHLIGHT_SCAN_CAP = 4000;
export function highlightSegments(
  text: string, terms: string[],
): { text: string; hl: boolean }[] {
  if (!text || terms.length === 0) return [{ text, hl: false }];
  const head = text.slice(0, HIGHLIGHT_SCAN_CAP);
  const tail = text.slice(HIGHLIGHT_SCAN_CAP);
  const lower = head.toLowerCase();
  const lterms = terms.map(t => t.toLowerCase()).filter(t => t.length > 0);
  const segs: { text: string; hl: boolean }[] = [];
  let i = 0;
  while (i < head.length) {
    let mIdx = -1;
    let mLen = 0;
    for (const t of lterms) {
      const idx = lower.indexOf(t, i);
      if (idx === -1) continue;
      if (mIdx === -1 || idx < mIdx || (idx === mIdx && t.length > mLen)) {
        mIdx = idx;
        mLen = t.length;
      }
    }
    if (mIdx === -1) {
      segs.push({ text: head.slice(i), hl: false });
      break;
    }
    if (mIdx > i) segs.push({ text: head.slice(i, mIdx), hl: false });
    segs.push({ text: head.slice(mIdx, mIdx + mLen), hl: true });
    i = mIdx + mLen;
  }
  if (tail) segs.push({ text: tail, hl: false });
  if (segs.length === 0) return [{ text, hl: false }];
  return segs;
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
