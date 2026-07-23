// FAZ 2 (docs/audit/traces-attribute-columns.md §6B) — the enrichment
// merge must CONVERGE: after one merge every requested key exists on every
// row, so missingExtraKeys returns [] and the effect never loops — even
// when the server response omits a trace or a key entirely.
import { describe, expect, it } from 'vitest';
import { mergeTraceExtras, missingExtraKeys } from './traceExtrasMerge';
import type { TraceRow } from './types';

const row = (traceId: string, extras?: Record<string, string>): TraceRow => ({
  traceId, rootName: 'op', serviceName: 'svc',
  startTime: 1, durationMs: 2, spanCount: 3, hasError: false,
  ...(extras ? { extras } : {}),
});

describe('missingExtraKeys', () => {
  it('reports keys any row lacks, by presence not truthiness', () => {
    const rows = [row('a', { CHANNEL_CODE: '' }), row('b', { CHANNEL_CODE: 'WEB' })];
    // '' counts as fetched — must NOT be re-requested.
    expect(missingExtraKeys(rows, ['CHANNEL_CODE'])).toEqual([]);
    expect(missingExtraKeys(rows, ['CHANNEL_CODE', 'FUNCTION_CODE'])).toEqual(['FUNCTION_CODE']);
    expect(missingExtraKeys([row('c')], ['CHANNEL_CODE'])).toEqual(['CHANNEL_CODE']);
  });
});

describe('mergeTraceExtras', () => {
  const idsOf = (...ids: string[]) => new Set(ids);

  it('stamps values and marks omitted traces/keys as fetched ("")', () => {
    const rows = [row('a'), row('b')];
    const out = mergeTraceExtras(rows, ['CHANNEL_CODE', 'FUNCTION_CODE'], {
      a: { CHANNEL_CODE: 'WEB', FUNCTION_CODE: '' },
      // 'b' omitted by the server entirely.
    }, idsOf('a', 'b'));
    expect(out[0].extras).toEqual({ CHANNEL_CODE: 'WEB', FUNCTION_CODE: '' });
    expect(out[1].extras).toEqual({ CHANNEL_CODE: '', FUNCTION_CODE: '' });
    // Convergence: nothing left to fetch → the effect stops.
    expect(missingExtraKeys(out, ['CHANNEL_CODE', 'FUNCTION_CODE'])).toEqual([]);
  });

  it('preserves keys outside the requested set and row identity when unchanged', () => {
    const rows = [row('a', { OLD: 'x', CHANNEL_CODE: 'WEB' })];
    const out = mergeTraceExtras(rows, ['CHANNEL_CODE'], { a: { CHANNEL_CODE: 'WEB' } }, idsOf('a'));
    expect(out[0]).toBe(rows[0]); // unchanged → same object (stable renders)
    const out2 = mergeTraceExtras(rows, ['CHANNEL_CODE'], { a: { CHANNEL_CODE: 'MOBILE' } }, idsOf('a'));
    expect(out2[0]).not.toBe(rows[0]);
    expect(out2[0].extras).toEqual({ OLD: 'x', CHANNEL_CODE: 'MOBILE' });
    expect(rows[0].extras).toEqual({ OLD: 'x', CHANNEL_CODE: 'WEB' }); // input untouched
  });

  // v0.9.195 review-fix — stale-response guard: a response for a REPLACED
  // page (its ids are not in the request set) must not stamp the new rows
  // as fetched-empty; untouched rows stay refetchable next pass.
  it('never stamps rows outside the requested id set (stale response)', () => {
    const newPage = [row('c'), row('d')];
    const out = mergeTraceExtras(newPage, ['CHANNEL_CODE'], { a: { CHANNEL_CODE: 'WEB' } }, idsOf('a', 'b'));
    expect(out[0]).toBe(newPage[0]); // identity preserved, no '' stamp
    expect(out[1]).toBe(newPage[1]);
    expect(missingExtraKeys(out, ['CHANNEL_CODE'])).toEqual(['CHANNEL_CODE']); // still fetchable
  });
});
