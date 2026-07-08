import { describe, it, expect } from 'vitest';
import { encodeStmtParam, decodeStmtParam, densifyTrend } from './stmtParam';

// stmtParam.test.ts — v0.8.378 (Stage-2 slice D2). Pins the `?stmt=` URL
// codec for the /slow-queries statement detail drawer: decimal-string
// hash discipline (uint64 > 2^53 must survive), boundary-safe optional
// system field, and null-on-garbage so a hostile deep-link keeps the
// drawer closed instead of crashing or firing a 400-bound fetch.

describe('stmtParam codec', () => {
  it('round-trips a hash-only ref', () => {
    const ref = { hash: '12345678901234567890', system: '' };
    expect(decodeStmtParam(encodeStmtParam(ref))).toEqual(ref);
  });

  it('round-trips hash + system', () => {
    const ref = { hash: '42', system: 'postgresql' };
    expect(decodeStmtParam(encodeStmtParam(ref))).toEqual(ref);
  });

  it('preserves full uint64 precision as a string (no Number coercion)', () => {
    // 2^64 - 1: past 2^53 a JS number would silently round this.
    const ref = { hash: '18446744073709551615', system: '' };
    expect(decodeStmtParam(encodeStmtParam(ref))!.hash).toBe('18446744073709551615');
  });

  it('a literal | inside system cannot forge a field boundary', () => {
    const ref = { hash: '7', system: 'weird|engine' };
    expect(decodeStmtParam(encodeStmtParam(ref))).toEqual(ref);
  });

  it('rejects malformed input instead of throwing', () => {
    expect(decodeStmtParam(null)).toBeNull();
    expect(decodeStmtParam('')).toBeNull();
    expect(decodeStmtParam('abc')).toBeNull();            // non-digit hash
    expect(decodeStmtParam('12ab')).toBeNull();           // mixed
    expect(decodeStmtParam('-5')).toBeNull();             // sign
    expect(decodeStmtParam('1e10')).toBeNull();           // scientific
    expect(decodeStmtParam('0')).toBeNull();              // "no statement" sentinel
    expect(decodeStmtParam('000')).toBeNull();            // padded sentinel
    expect(decodeStmtParam('42|')).toBeNull();            // empty system field
    expect(decodeStmtParam('|postgresql')).toBeNull();    // missing hash
    expect(decodeStmtParam('42|pg|extra')).toBeNull();    // extra fields
    expect(decodeStmtParam('42|%E0%A4%A')).toBeNull();    // bad escape
    expect(decodeStmtParam('123456789012345678901')).toBeNull(); // 21 digits > uint64 width
  });
});

describe('densifyTrend', () => {
  const P = (tsSec: number, calls: number) => ({
    tsNs: tsSec * 1e9, calls, errors: calls > 1 ? 1 : 0, avgMs: 10, p95Ms: 25,
  });

  it('expands sparse buckets onto the dense grid with zero gaps', () => {
    // Window 11:03:27→12:00 snaps to 11:00; 300s buckets → 12 slots.
    const fromNs = Date.UTC(2026, 6, 8, 11, 3, 27) * 1e6;
    const toNs = Date.UTC(2026, 6, 8, 12, 0, 0) * 1e6;
    const t0 = Date.UTC(2026, 6, 8, 11, 0, 0) / 1000;
    const d = densifyTrend([P(t0, 5), P(t0 + 600, 3)], fromNs, toNs, 300);
    expect(d.calls).toHaveLength(12);
    expect(d.calls[0]).toBe(5);
    expect(d.calls[1]).toBe(0); // gap → zero
    expect(d.calls[2]).toBe(3);
    expect(d.errors[0]).toBe(1);
    expect(d.p95Ms[2]).toBe(25);
  });

  it('drops out-of-window points instead of writing out of bounds', () => {
    const fromNs = 1000 * 300 * 1e9;
    const toNs = fromNs + 600 * 1e9;
    const d = densifyTrend([P(1000 * 300 - 300, 9), P(1000 * 300 + 9000, 9)], fromNs, toNs, 300);
    expect(d.calls.every(v => v === 0)).toBe(true);
  });

  it('degenerate inputs return empty arrays', () => {
    expect(densifyTrend([], 0, 0, 300).calls).toHaveLength(0);
    expect(densifyTrend([P(0, 1)], 5e9, 4e9, 300).calls).toHaveLength(0); // inverted
    expect(densifyTrend([P(0, 1)], 0, 5e9, 0).calls).toHaveLength(0);     // zero width
  });

  it('caps the grid at 400 buckets (the backend LIMIT mirror)', () => {
    const fromNs = 0;
    const toNs = 90 * 24 * 3600 * 1e9; // 90d at 300s would be 25 920
    const d = densifyTrend([P(0, 1)], fromNs, toNs, 300);
    expect(d.calls).toHaveLength(400);
  });
});
