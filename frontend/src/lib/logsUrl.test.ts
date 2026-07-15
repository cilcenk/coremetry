import { describe, it, expect } from 'vitest';
import { logsUrlSig, writeLogsParams, readLogsParams, type LogsUrlFilter } from './logsUrl';

// v0.8.546 — /logs `severity` was a live filter that never round-tripped
// through the URL: writeUrl didn't write it, urlSig didn't hash it, and the
// import pinned it to 0. Pressing the ERROR chip and hitting Share handed
// the recipient a link that opened on All levels — a silent WRONG link.
//
// The guard these tests protect: the page no-ops its URL→state import when
// the incoming params hash to the sig it just wrote. That only holds while
// {sig, write, read} agree on one field set; drift means an infinite
// refetch or a clobbered filter (the v0.8.253/256/265 class).

const base: LogsUrlFilter = {
  service: '', cluster: '', search: '', severity: 0,
  traceId: '', spanId: '', hasTrace: false,
};
const rt = (f: LogsUrlFilter) => readLogsParams(writeLogsParams(new URLSearchParams(), f, '', ''));

describe('severity round-trip — the reported bug', () => {
  it('survives write → read', () => {
    expect(rt({ ...base, severity: 17 }).severity).toBe(17);
  });

  it('is actually written to the URL', () => {
    expect(writeLogsParams(new URLSearchParams(), { ...base, severity: 17 }, '', '').get('severity')).toBe('17');
  });

  it('0 (all levels) leaves no param behind', () => {
    expect(writeLogsParams(new URLSearchParams(), base, '', '').has('severity')).toBe(false);
    expect(rt(base).severity).toBe(0);
  });

  it('toggling a chip off clears a severity already in the URL', () => {
    // The page reuses the existing params; a stale severity must not survive
    // the toggle-off that sets it back to 0.
    const prev = new URLSearchParams('severity=17&service=api');
    expect(writeLogsParams(prev, { ...base, service: 'api' }, '', '').has('severity')).toBe(false);
  });

  it('every severity rung the chips can set round-trips', () => {
    // LVL_FACETS floors — a chip that silently resolved to 0 would reopen
    // the All-levels bug for that band only.
    for (const min of [1, 5, 9, 13, 17, 21]) {
      expect(rt({ ...base, severity: min }).severity).toBe(min);
    }
  });

  it('garbage severity reads as 0, never NaN', () => {
    // NaN would poison both the query and the sig (JSON.stringify(NaN) →
    // "null", so two different bad URLs would hash equal).
    for (const raw of ['abc', '', '-3', 'NaN', 'Infinity']) {
      expect(readLogsParams(new URLSearchParams(`severity=${raw}`)).severity).toBe(0);
    }
  });
});

describe('logsUrlSig — the guard', () => {
  it('hashes severity: a severity-only change must move the sig', () => {
    // This is the exact omission that let the bug hide: the old sig ignored
    // severity, so the guard treated an ERROR-chip URL as "no change".
    expect(logsUrlSig({ ...base, severity: 17 }, '', ''))
      .not.toBe(logsUrlSig(base, '', ''));
  });

  it('is stable for the same state', () => {
    expect(logsUrlSig({ ...base, severity: 17 }, 'f', 'c'))
      .toBe(logsUrlSig({ ...base, severity: 17 }, 'f', 'c'));
  });

  it('moves for every URL-bearing field', () => {
    const variants: Array<[string, LogsUrlFilter]> = [
      ['service',  { ...base, service: 'api' }],
      ['cluster',  { ...base, cluster: 'eu' }],
      ['search',   { ...base, search: 'boom' }],
      ['severity', { ...base, severity: 17 }],
      ['traceId',  { ...base, traceId: 'abc' }],
      ['spanId',   { ...base, spanId: 'def' }],
      ['hasTrace', { ...base, hasTrace: true }],
    ];
    const zero = logsUrlSig(base, '', '');
    for (const [name, f] of variants) {
      expect(logsUrlSig(f, '', ''), `${name} must move the sig`).not.toBe(zero);
    }
    expect(logsUrlSig(base, 'filters', ''), 'filters must move the sig').not.toBe(zero);
    expect(logsUrlSig(base, '', 'cols'), 'cols must move the sig').not.toBe(zero);
  });

  it('agrees across write→read: a written state hashes to the same sig it reads back as', () => {
    // The invariant the page's no-op depends on. If write and read disagree
    // on any field, the sig flips right after writeUrl and the import
    // clobbers the state the operator just set.
    const f: LogsUrlFilter = {
      service: 'api', cluster: 'eu', search: 'boom', severity: 17,
      traceId: 'abc', spanId: 'def', hasTrace: true,
    };
    expect(logsUrlSig(rt(f), '', '')).toBe(logsUrlSig(f, '', ''));
  });
});

describe('other params keep their pre-v0.8.546 behaviour', () => {
  it('drops the legacy `search` alias while still reading it', () => {
    expect(readLogsParams(new URLSearchParams('search=old')).search).toBe('old');
    expect(readLogsParams(new URLSearchParams('q=new&search=old')).search).toBe('new');
    expect(writeLogsParams(new URLSearchParams('search=old'), { ...base, search: 'new' }, '', '').has('search')).toBe(false);
  });

  it('hasTrace is 1/absent, not true/false', () => {
    expect(writeLogsParams(new URLSearchParams(), { ...base, hasTrace: true }, '', '').get('hasTrace')).toBe('1');
    expect(writeLogsParams(new URLSearchParams(), base, '', '').has('hasTrace')).toBe(false);
  });
});
