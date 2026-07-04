import { describe, it, expect } from 'vitest';
import {
  compileSearch, toggleFilter, encodeFiltersParam, parseFiltersParam,
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
