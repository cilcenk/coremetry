import { describe, it, expect } from 'vitest';
import { traceHref } from './traceHref';
import { decodeRange } from './urlState';

// traceHref — v0.9.1347.
//
// What these tests pin is NOT "the window is always present" (it is not, and
// the file comment explains at length why /trace is not that shape). They pin
// the two things that can actually go wrong:
//
//   1. The param contract. /trace reads id/span/tab/xn and NOTHING else
//      (pages/Trace.tsx:42-81). A builder that emits a fourth key produces a
//      link that looks authoritative and is ignored.
//   2. The window that IS emitted must be a PAGE range, and must never be a
//      token decodeRange would mishandle.
//
// ── UNIT NOTE, and a trap this file deliberately avoids ──────────────────
// Unix-NANOSECOND timestamps are ~1.7e18, well past Number.MAX_SAFE_INTEGER
// (~9.0e15). At that magnitude a double's ULP is 256ns, so `ns + 1` is
// LITERALLY the same number and a ±1ns assertion proves nothing at all. Every
// sub-millisecond value below is a half/four-tenths-of-a-millisecond offset —
// far above the ULP, and chosen so floor/ceil give a DIFFERENT answer than
// Math.round would.
const params = (href: string) => new URLSearchParams(href.slice(href.indexOf('?') + 1));

describe('traceHref — param contract', () => {
  it('a bare link is exactly the id', () => {
    expect(traceHref('abc123')).toBe('/trace?id=abc123');
  });

  it('emits only keys /trace actually reads', () => {
    const p = params(traceHref('t1', {
      span: 's1', tab: 'logs', groupSimilar: true, pageRange: { preset: '6h' },
    }));
    // The whole readable set of pages/Trace.tsx, and nothing beyond it.
    expect([...p.keys()].sort()).toEqual(['id', 'range', 'span', 'tab', 'xn']);
    expect(p.get('id')).toBe('t1');
    expect(p.get('span')).toBe('s1');
    expect(p.get('tab')).toBe('logs');
    expect(p.get('xn')).toBe('1');
  });

  it('keeps `id` first so converted sites produce the URL they produced before', () => {
    expect(traceHref('t1', { span: 's1' })).toBe('/trace?id=t1&span=s1');
  });

  it('omits every optional key when it is absent or false', () => {
    const p = params(traceHref('t1', { span: null, tab: null, groupSimilar: false }));
    expect([...p.keys()]).toEqual(['id']);
  });

  it('encodes an id that is not plain hex', () => {
    // ⌘K hands this builder whatever the operator typed.
    expect(traceHref('a b&c=d')).toBe('/trace?id=a+b%26c%3Dd');
    expect(params(traceHref('a b&c=d')).get('id')).toBe('a b&c=d');
  });
});

describe('traceHref — the window is a PAGE range', () => {
  it('carries a relative preset verbatim', () => {
    expect(params(traceHref('t1', { pageRange: { preset: '6h' } })).get('range')).toBe('6h');
  });

  it('carries an absolute page window and round-trips through decodeRange', () => {
    const r = { preset: 'custom', fromMs: 1_700_000_000_000, toMs: 1_700_000_900_000 };
    const enc = params(traceHref('t1', { pageRange: r })).get('range');
    expect(enc).toBe('custom:1700000000000-1700000900000');
    expect(decodeRange(enc, { preset: '30m' })).toEqual(r);
  });

  it('passes an already-encoded range string through untouched', () => {
    // Several call sites hold params.get('range'), not a TimeRange.
    expect(params(traceHref('t1', { pageRange: 'custom:1-2' })).get('range')).toBe('custom:1-2');
    expect(params(traceHref('t1', { pageRange: '24h' })).get('range')).toBe('24h');
  });

  it('emits NO range at all when the caller declines', () => {
    for (const v of [undefined, null, '']) {
      expect(params(traceHref('t1', { pageRange: v })).has('range')).toBe(false);
    }
  });

  // The bare-'custom' hole. encodeRange emits the literal string 'custom' for
  // a TimeRange whose preset is 'custom' but whose bounds are missing;
  // decodeRange hands back {preset:'custom'} with no bounds, and
  // timeRangeToNs then falls through to its 86400s default (utils.ts:17-25).
  // The operator sees a window that says "custom" and means "24h".
  it('drops a bare `custom` rather than pinning a silent 24h', () => {
    expect(params(traceHref('t1', { pageRange: { preset: 'custom' } })).has('range')).toBe(false);
    expect(params(traceHref('t1', { pageRange: 'custom' })).has('range')).toBe(false);
    // …but a custom range WITH bounds is still carried.
    expect(params(traceHref('t1', {
      pageRange: { preset: 'custom', fromMs: 1, toMs: 2 },
    })).get('range')).toBe('custom:1-2');
  });
});

describe('traceHref — an event window must not COMPILE as a page range', () => {
  // This is the point of the signature, so it is pinned as a type test rather
  // than left to prose. The habit this blocks: reaching for the pivotHref
  // shape and handing over the EXEMPLAR's own bounds. A trace lasts
  // milliseconds; navHref carries `custom:` windows onward (navHref.ts:47),
  // so the operator's NEXT hop would open on a two-second window and show
  // nothing. If this @ts-expect-error ever goes unused, the guard has been
  // widened and the regression is available again.
  it('rejects the { fromNs, toNs } shape', () => {
    // @ts-expect-error — event windows belong to logsHref / tracesPivotHref,
    // never to /trace's Topbar.
    const href = traceHref('t1', { pageRange: { fromNs: 1e18, toNs: 2e18 } });
    // The call still runs (types are erased); what matters is that it does
    // not typecheck, and that the junk object never becomes a range token.
    expect(params(href).has('range')).toBe(false);
  });
});
