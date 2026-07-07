import { describe, it, expect } from 'vitest';
import { encodeEndpointParam, decodeEndpointParam, trimHistogram } from './endpointParam';

// endpointParam.test.ts — v0.8.360 (Stage-2 slice E2). Pins the
// `?endpoint=` URL codec (round-trip incl. hostile characters — the
// field separator `|` inside a path must never forge a boundary) and
// the histogram trim helper.

describe('endpointParam codec', () => {
  it('round-trips a plain tuple', () => {
    const ref = { service: 'checkout', path: '/orders/8421', sig: false };
    expect(decodeEndpointParam(encodeEndpointParam(ref))).toEqual(ref);
  });

  it('round-trips a signature-mode tuple', () => {
    const ref = { service: 'checkout', path: '/orders/:id', sig: true };
    const raw = encodeEndpointParam(ref);
    expect(raw.endsWith('|sig')).toBe(true);
    expect(decodeEndpointParam(raw)).toEqual(ref);
  });

  it('a literal | inside path or service cannot forge a field boundary', () => {
    const ref = { service: 'svc|prod', path: '/a|b/c', sig: false };
    expect(decodeEndpointParam(encodeEndpointParam(ref))).toEqual(ref);
  });

  it('round-trips unicode + percent signs', () => {
    const ref = { service: 'sipariş-servisi', path: '/ürün/%25/detay', sig: true };
    expect(decodeEndpointParam(encodeEndpointParam(ref))).toEqual(ref);
  });

  it('rejects malformed input instead of throwing', () => {
    expect(decodeEndpointParam(null)).toBeNull();
    expect(decodeEndpointParam('')).toBeNull();
    expect(decodeEndpointParam('only-service')).toBeNull();
    expect(decodeEndpointParam('svc|')).toBeNull();
    expect(decodeEndpointParam('|/path')).toBeNull();
    expect(decodeEndpointParam('svc|path|garbage')).toBeNull();
    expect(decodeEndpointParam('svc|path|sig|extra')).toBeNull();
    expect(decodeEndpointParam('svc|%E0%A4%A')).toBeNull(); // bad escape
  });
});

describe('trimHistogram', () => {
  it('strips all-zero leading and trailing bins', () => {
    const { bins, counts } = trimHistogram(
      [1, 1.78, 3.16, 5.62, 10],
      [0, 4, 0, 7, 0],
    );
    expect(bins).toEqual([1.78, 3.16, 5.62]);
    expect(counts).toEqual([4, 0, 7]);
  });

  it('keeps a fully-populated grid intact', () => {
    const { bins, counts } = trimHistogram([1, 2], [3, 5]);
    expect(bins).toEqual([1, 2]);
    expect(counts).toEqual([3, 5]);
  });

  it('all-zero input yields empty arrays', () => {
    const { bins, counts } = trimHistogram([1, 2, 3], [0, 0, 0]);
    expect(bins).toEqual([]);
    expect(counts).toEqual([]);
  });

  it('tolerates mismatched lengths (min wins)', () => {
    const { bins, counts } = trimHistogram([1, 2, 3], [0, 9]);
    expect(bins).toEqual([2]);
    expect(counts).toEqual([9]);
  });
});
