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
  // v0.9.1219 (dilim 1) — is-one-of: value yerine values (≥2 değer),
  // `key:("a" OR "b")` olarak derlenir; negated "hiçbiri" demek.
  values?: string[];
  // v0.9.1217 (Kibana paritesi, dilim 5) — varlık filtresi: değer değil
  // alanın KENDİSİ aranıyor (`_exists_:key`). value boş kalır; negated
  // "alan YOK" anlamına gelir. NOT: ES query_string'te birebir çalışır;
  // CH lokal backend'i field:value semantiği taşımadığından (LIKE
  // substring) mevcut alan-pill'leriyle AYNI sınıfta düşer — yeni
  // ayrışma değil (docs/plans/kibana-logs-parity-2026-08-21.md).
  exists?: boolean;
  // v0.9.1222 — aralık operatörü: `key:>=v` / `key:<=v` (Lucene range).
  // Tek sınır; "between" = iki pill (gte+lte), Kibana'nın is-between'inin
  // dürüst karşılığı. Negasyon editörde sunulmaz (NOT >= yerine <= kullan)
  // ama compile URL'den gelen negated'ı yine de sayar.
  op?: 'gte' | 'lte';
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
    .map(f => {
      const neg = f.negated ? 'NOT ' : '';
      if (f.exists) return `${neg}_exists_:${f.key}`;
      if (f.op) {
        // Tırnaklı değer ES range sözdiziminde geçerli (`key:>="v"`) ve
        // sayısal alanlarda coerce edilir; phraseQuote boşluk/özel
        // karakterli değerleri de güvene alır.
        return `${neg}${f.key}:${f.op === 'gte' ? '>=' : '<='}${phraseQuote(f.value)}`;
      }
      if (f.values && f.values.length > 1) {
        return `${neg}${f.key}:(${f.values.map(phraseQuote).join(' OR ')})`;
      }
      return `${neg}${f.key}:${phraseQuote(f.values?.[0] ?? f.value)}`;
    });
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
  // v0.9.1222 — aralık pill'leri eşitlik kimlik-uzayının DIŞINDA: aynı
  // key+value'lu bir ⊕ tıkı `key:>=v` pill'ini flip'lememeli.
  const idx = filters.findIndex(f => !f.exists && !f.op && f.key === key && f.value === value);
  if (idx === -1) return [...filters, { key, value, negated, disabled: false }];
  if (filters[idx].negated === negated) return filters.filter((_, i) => i !== idx);
  return filters.map((f, i) => (i === idx ? { ...f, negated, disabled: false } : f));
}

// toggleExistsFilter — varlık pill'inin toggle'ı; kimlik = key + exists
// (değer pill'lerinden ayrı uzay: `k:"v"` ile `_exists_:k` birlikte
// yaşayabilir). Polarite semantiği toggleFilter ile aynı.
export function toggleExistsFilter(
  filters: LogFilter[], key: string, negated: boolean,
): LogFilter[] {
  const idx = filters.findIndex(f => f.exists && f.key === key);
  if (idx === -1) return [...filters, { key, value: '', negated, disabled: false, exists: true }];
  if (filters[idx].negated === negated) return filters.filter((_, i) => i !== idx);
  return filters.map((f, i) => (i === idx ? { ...f, negated, disabled: false } : f));
}

// URL form: compact JSON tuples [key, value, negated, disabled]
// with 0/1 flags — keeps ?filters= short enough for Copy link and
// SavedViewsBar (both persist the raw query string).
export function encodeFiltersParam(filters: LogFilter[]): string {
  if (filters.length === 0) return '';
  // v0.9.1217 — 5. eleman exists bayrağı; 0 iken hiç yazılmaz ki eski
  // linkler/kayıtlı görünümler bayt-bayt aynı kalsın.
  // 6. eleman values dizisi (yalnız is-one-of'ta) — eski biçim aynen.
  // 7. eleman aralık operatörü ('gte'|'lte', v0.9.1222) — yalnız aralık
  // pill'inde yazılır; eski biçimler bayt-bayt aynı kalır.
  return JSON.stringify(filters.map(f => {
    const base: unknown[] = [f.key, f.value, f.negated ? 1 : 0, f.disabled ? 1 : 0];
    if (f.exists) return [...base, 1];
    if (f.values && f.values.length > 1) return [...base, 0, f.values];
    if (f.op) return [...base, 0, 0, f.op];
    return base;
  }));
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
      out.push({
        key: e[0], value: e[1], negated: !!e[2], disabled: !!e[3],
        ...(e[4] ? { exists: true } : {}),
        ...(Array.isArray(e[5]) && e[5].length > 1 ? { values: e[5].map(String) } : {}),
        ...(e[6] === 'gte' || e[6] === 'lte' ? { op: e[6] } : {}),
      });
    }
    return out;
  } catch {
    return [];
  }
}

// replaceFilterAt — pill EDIT popover'ının commit'i (v0.9.1219): i.
// pill'i yenisiyle değiştirir; boş anahtar pill'i düşürür. SAF.
export function replaceFilterAt(filters: LogFilter[], i: number, next: LogFilter): LogFilter[] {
  if (i < 0 || i >= filters.length) return filters;
  if (!next.key.trim()) return filters.filter((_, j) => j !== i);
  return filters.map((f, j) => (j === i ? next : f));
}

// mergePatternQuery — v0.10.502 (log arama denetimi B6): Desenler
// panelinin ⊕/⊖ eylemi türetilmiş sorguyu MEVCUT serbest metne ekler
// ("Ara" eskisi gibi değiştirir — davranış korunur, iki yeni eylem
// eklendi). exclude → `NOT (…)`. Zaten aynı parça varsa (aynı kip)
// yinelenmez; parantez, `a OR b` gibi metinlerin AND önceliğini korur.
export function mergePatternQuery(existing: string, pattern: string, exclude: boolean): string {
  const q = pattern.trim();
  if (!q) return existing;
  const piece = exclude ? `NOT (${q})` : `(${q})`;
  const base = existing.trim();
  if (!base) return piece;
  if (base.includes(piece)) return base;
  const left = /\bOR\b/i.test(base) && !/^\(.*\)$/.test(base) ? `(${base})` : base;
  return `${left} AND ${piece}`;
}
