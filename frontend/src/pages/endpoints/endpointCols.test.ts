import { describe, expect, it } from 'vitest';
import { formatColsParam, parseColsParam } from './endpointCols';

// v0.8.574 — /endpoints kolon göster/gizle codec'i. The URL contract
// matches Logs `?cols=`: absent param = default (all visible), and the
// param round-trips through parse→format for every non-default subset.

const ALL = ['service', 'path', 'method', 'calls', 'traces'] as const;

describe('parseColsParam', () => {
  it('null and empty mean all columns visible', () => {
    expect(parseColsParam(null, ALL)).toEqual(new Set(ALL));
    expect(parseColsParam('', ALL)).toEqual(new Set(ALL));
  });

  it('parses a subset and drops unknown ids', () => {
    expect(parseColsParam('service,calls,nope', ALL)).toEqual(new Set(['service', 'calls']));
  });

  it('tolerates whitespace around ids', () => {
    expect(parseColsParam(' service , path ', ALL)).toEqual(new Set(['service', 'path']));
  });

  it('falls back to all when nothing valid survives — never a column-less table', () => {
    expect(parseColsParam('bogus,junk', ALL)).toEqual(new Set(ALL));
  });
});

describe('formatColsParam', () => {
  it('all visible → empty string (caller deletes the param)', () => {
    expect(formatColsParam(new Set(ALL), ALL)).toBe('');
  });

  it('subset emits canonical column order regardless of insertion order', () => {
    expect(formatColsParam(new Set(['calls', 'service']), ALL)).toBe('service,calls');
  });

  it('round-trips every non-default subset', () => {
    const subset = new Set(['path', 'traces']);
    expect(parseColsParam(formatColsParam(subset, ALL), ALL)).toEqual(subset);
  });

  it('ignores ids not in the schema', () => {
    expect(formatColsParam(new Set(['service', 'ghost']), ALL)).toBe('service');
  });
});
