import { describe, it, expect } from 'vitest';
import {
  compileSearch, toggleFilter, encodeFiltersParam, parseFiltersParam,
  extractHighlightTerms, highlightSegments,
  type LogFilter,
} from './logFilters';

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
