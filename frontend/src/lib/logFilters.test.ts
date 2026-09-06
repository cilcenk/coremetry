import { describe, it, expect } from 'vitest';
import {
  compileSearch, toggleFilter, encodeFiltersParam, parseFiltersParam,
  extractHighlightTerms, highlightSegments,
  type LogFilter,
} from './logFilters';
import { mergePatternQuery } from './logFilters';

const f = (key: string, value: string, negated = false, disabled = false): LogFilter =>
  ({ key, value, negated, disabled });

describe('compileSearch', () => {
  it('empty inputs → empty string', () => {
    expect(compileSearch([], '')).toBe('');
    expect(compileSearch([], '   ')).toBe('');
  });

  it('quotes values and joins with AND', () => {
    expect(compileSearch([f('service.name', 'checkout'), f('level', 'error', true)], ''))
      .toBe('service.name:"checkout" AND NOT level:"error"');
  });

  it('escapes quotes/backslashes inside values (v0.5.230 class)', () => {
    expect(compileSearch([f('msg', 'say "hi" \\ bye')], ''))
      .toBe('msg:"say \\"hi\\" \\\\ bye"');
  });

  it('skips disabled pills', () => {
    expect(compileSearch([f('a', '1', false, true), f('b', '2')], '')).toBe('b:"2"');
  });

  it('appends free text and parenthesises top-level OR', () => {
    expect(compileSearch([f('a', '1')], 'timeout')).toBe('a:"1" AND timeout');
    expect(compileSearch([f('a', '1')], 'x OR y')).toBe('a:"1" AND (x OR y)');
    // No pills → query passes through verbatim (no needless parens).
    expect(compileSearch([], 'x OR y')).toBe('x OR y');
  });
});

describe('toggleFilter', () => {
  it('adds when absent', () => {
    expect(toggleFilter([], 'a', '1', false)).toEqual([f('a', '1')]);
  });

  it('same polarity toggles off', () => {
    expect(toggleFilter([f('a', '1')], 'a', '1', false)).toEqual([]);
    expect(toggleFilter([f('a', '1', true)], 'a', '1', true)).toEqual([]);
  });

  it('opposite polarity flips in place and re-enables', () => {
    expect(toggleFilter([f('a', '1', false, true)], 'a', '1', true))
      .toEqual([f('a', '1', true, false)]);
  });

  it('does not touch unrelated pills', () => {
    const others = [f('b', '2'), f('c', '3', true)];
    expect(toggleFilter([...others, f('a', '1')], 'a', '1', false)).toEqual(others);
  });
});

// Discover revamp 6/7 — client-side <mark> highlighting. Field
// clauses must NOT leak into the highlight set (level:error would
// light unrelated "error" text), and segmentation must be case-
// insensitive + non-overlapping.
describe('extractHighlightTerms', () => {
  it('collects bare terms and quoted phrases, skips operators', () => {
    expect(extractHighlightTerms('timeout AND "connection refused" OR retry'))
      .toEqual(['timeout', 'connection refused', 'retry']);
  });

  it('excludes field clauses — bare and quoted values', () => {
    expect(extractHighlightTerms('level:error AND service.name:"checkout" payment'))
      .toEqual(['payment']);
    expect(extractHighlightTerms('trace.id:c9ea* NOT k8s.namespace:prod'))
      .toEqual([]);
  });

  it('strips parens/wildcards, dedups, drops single chars', () => {
    expect(extractHighlightTerms('(oom OR oom) killed* x'))
      .toEqual(['oom', 'killed']);
  });

  it('empty query → no terms', () => {
    expect(extractHighlightTerms('')).toEqual([]);
    expect(extractHighlightTerms('   ')).toEqual([]);
  });
});

describe('highlightSegments', () => {
  it('no terms → single unhighlighted run', () => {
    expect(highlightSegments('hello', [])).toEqual([{ text: 'hello', hl: false }]);
  });

  it('case-insensitive, non-overlapping runs in order', () => {
    expect(highlightSegments('Payment TIMEOUT during payment', ['payment', 'timeout']))
      .toEqual([
        { text: 'Payment', hl: true },
        { text: ' ', hl: false },
        { text: 'TIMEOUT', hl: true },
        { text: ' during ', hl: false },
        { text: 'payment', hl: true },
      ]);
  });

  it('longest term wins at the same position', () => {
    expect(highlightSegments('connection refused', ['connection refused', 'connection']))
      .toEqual([{ text: 'connection refused', hl: true }]);
  });

  it('round-trips the input text exactly', () => {
    const body = 'a payment failed: timeout x2';
    const joined = highlightSegments(body, ['payment', 'timeout']).map(s => s.text).join('');
    expect(joined).toBe(body);
  });
});

describe('encode/parse round-trip', () => {
  it('round-trips all flag combinations', () => {
    const pills = [f('a', '1'), f('b', '2', true), f('c', '3', false, true), f('d', '4', true, true)];
    expect(parseFiltersParam(encodeFiltersParam(pills))).toEqual(pills);
  });

  it('empty list encodes to empty string (param omitted from URL)', () => {
    expect(encodeFiltersParam([])).toBe('');
  });

  it('tolerates garbage input', () => {
    expect(parseFiltersParam(null)).toEqual([]);
    expect(parseFiltersParam('')).toEqual([]);
    expect(parseFiltersParam('not-json')).toEqual([]);
    expect(parseFiltersParam('{"a":1}')).toEqual([]);
    expect(parseFiltersParam('[["ok","1"],["bad"],[42,"x"]]')).toEqual([f('ok', '1')]);
  });
});

// ── v0.9.1217 (Kibana paritesi, dilim 5) — exists pill'i ────────────
import { toggleExistsFilter } from './logFilters';

describe('exists filtresi', () => {
  it('compile: _exists_:key + NOT hâli; değer pill\'leriyle yan yana', () => {
    const fs = [
      { key: 'attributes.CHANNEL_CODE', value: '', negated: false, disabled: false, exists: true },
      { key: 'level', value: 'error', negated: false, disabled: false },
      { key: 'trace_id', value: '', negated: true, disabled: false, exists: true },
    ];
    expect(compileSearch(fs, '')).toBe(
      '_exists_:attributes.CHANNEL_CODE AND level:"error" AND NOT _exists_:trace_id');
  });

  it('toggle: kimlik key+exists — değer pill\'inden ayrı uzay', () => {
    let fs = toggleExistsFilter([], 'k', false);
    expect(fs).toHaveLength(1);
    expect(fs[0].exists).toBe(true);
    // Aynı polarite → kaldır; zıt → çevir.
    expect(toggleExistsFilter(fs, 'k', false)).toHaveLength(0);
    expect(toggleExistsFilter(fs, 'k', true)[0].negated).toBe(true);
    // Değer pill'i exists toggle'ından etkilenmez.
    const mixed = [{ key: 'k', value: 'v', negated: false, disabled: false }];
    expect(toggleExistsFilter(mixed, 'k', false)).toHaveLength(2);
  });

  it('URL gidiş-dönüşü: 5. eleman yalnız exists\'te; eski biçim aynen çözülür', () => {
    const fs = [
      { key: 'a', value: 'x', negated: false, disabled: false },
      { key: 'b', value: '', negated: true, disabled: false, exists: true },
    ];
    const enc = encodeFiltersParam(fs);
    expect(enc).toBe('[["a","x",0,0],["b","",1,0,1]]');
    expect(parseFiltersParam(enc)).toEqual([
      { key: 'a', value: 'x', negated: false, disabled: false },
      { key: 'b', value: '', negated: true, disabled: false, exists: true },
    ]);
    // Eski 4-elemanlı biçim (kayıtlı görünümler) değişmeden çözülür.
    expect(parseFiltersParam('[["a","x",1,0]]')).toEqual([
      { key: 'a', value: 'x', negated: true, disabled: false },
    ]);
  });
});

// ── v0.9.1219 (dilim 1) — is-one-of + pill edit modeli ──────────────
import { replaceFilterAt } from './logFilters';

describe('is-one-of + edit', () => {
  it('compile: key:("a" OR "b") + NOT hâli', () => {
    const fs = [
      { key: 'level', value: '', negated: false, disabled: false, values: ['error', 'fatal'] },
      { key: 'svc', value: '', negated: true, disabled: false, values: ['a', 'b'] },
    ];
    expect(compileSearch(fs, '')).toBe('level:("error" OR "fatal") AND NOT svc:("a" OR "b")');
  });

  it('tek elemanlı values düz eşitliğe düşer', () => {
    const fs = [{ key: 'k', value: '', negated: false, disabled: false, values: ['x'] }];
    expect(compileSearch(fs, '')).toBe('k:"x"');
  });

  it('URL gidiş-dönüşü: 6. eleman yalnız çok-değerde; eski biçimler aynen', () => {
    const fs = [{ key: 'level', value: '', negated: false, disabled: false, values: ['e', 'f'] }];
    const enc = encodeFiltersParam(fs);
    expect(enc).toBe('[["level","",0,0,0,["e","f"]]]');
    expect(parseFiltersParam(enc)).toEqual(fs);
    expect(parseFiltersParam('[["a","x",0,0]]')).toEqual([
      { key: 'a', value: 'x', negated: false, disabled: false },
    ]);
  });

  it('replaceFilterAt: yerinde değişim; boş anahtar düşürür; aralık dışı no-op', () => {
    const fs = [{ key: 'a', value: '1', negated: false, disabled: false }];
    const next = { key: 'b', value: '2', negated: true, disabled: false };
    expect(replaceFilterAt(fs, 0, next)).toEqual([next]);
    expect(replaceFilterAt(fs, 0, { ...next, key: ' ' })).toEqual([]);
    expect(replaceFilterAt(fs, 5, next)).toEqual(fs);
  });
});

// v0.9.1222 — aralık operatörü (Kibana parite artığı): `key:>=v` / `key:<=v`.
describe('range op (gte/lte)', () => {
  const gte = (key: string, value: string): LogFilter =>
    ({ key, value, negated: false, disabled: false, op: 'gte' });

  it('compiles gte/lte with quoted value (ES coerces numerics)', () => {
    expect(compileSearch([gte('duration_ms', '250')], ''))
      .toBe('duration_ms:>="250"');
    expect(compileSearch([{ key: 'ts', value: '2026-08-01', negated: false, disabled: false, op: 'lte' }], ''))
      .toBe('ts:<="2026-08-01"');
  });

  it('negated-from-URL still compiles (editor never sets it)', () => {
    expect(compileSearch([{ ...gte('n', '5'), negated: true }], ''))
      .toBe('NOT n:>="5"');
  });

  it('URL roundtrip: 7th tuple element only for range pills', () => {
    const pills: LogFilter[] = [
      { key: 'a', value: 'x', negated: false, disabled: false },
      gte('lat', '100'),
    ];
    const enc = encodeFiltersParam(pills);
    expect(enc).toContain('"gte"');
    expect(JSON.parse(enc)[0]).toHaveLength(4); // eşitlik pill'i eski biçim
    expect(parseFiltersParam(enc)).toEqual(pills);
  });

  it('toggleFilter never flips a range pill sharing key+value', () => {
    const start = [gte('n', '5')];
    const after = toggleFilter(start, 'n', '5', false);
    expect(after).toHaveLength(2); // yeni eşitlik pill'i eklendi, aralık duruyor
    expect(after[0].op).toBe('gte');
    expect(after[1].op).toBeUndefined();
  });

  it('disabled range pill is excluded from compile', () => {
    expect(compileSearch([{ ...gte('n', '5'), disabled: true }], 'x')).toBe('x');
  });
});


// v0.10.502 (B6) — Desenler ⊕/⊖: türetilmiş sorgu mevcut metne eklenir,
// metin ezilmez; ⊖ NOT; yineleme yok; OR'lu tabanın önceliği korunur.
describe('mergePatternQuery', () => {
  it('boş tabana düz / NOT parça', () => {
    expect(mergePatternQuery('', '"a" AND "b"', false)).toBe('("a" AND "b")');
    expect(mergePatternQuery('  ', '"a"', true)).toBe('NOT ("a")');
  });
  it('mevcut metni korur ve AND ile ekler', () => {
    expect(mergePatternQuery('level:error', '"timeout"', false)).toBe('level:error AND ("timeout")');
    expect(mergePatternQuery('level:error', '"timeout"', true)).toBe('level:error AND NOT ("timeout")');
  });
  it('OR içeren tabanı parantezler', () => {
    expect(mergePatternQuery('a OR b', '"x"', false)).toBe('(a OR b) AND ("x")');
    expect(mergePatternQuery('(a OR b)', '"x"', true)).toBe('(a OR b) AND NOT ("x")');
  });
  it('aynı parçayı yinelemez; boş deseni yok sayar', () => {
    expect(mergePatternQuery('level:error AND ("x")', '"x"', false)).toBe('level:error AND ("x")');
    expect(mergePatternQuery('level:error', '   ', true)).toBe('level:error');
  });
});
