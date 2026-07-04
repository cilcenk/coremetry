import { describe, it, expect } from 'vitest';
import {
  SERVICE_COLS, DEFAULT_SERVICES_SORT,
  sanitizeServicesSort, decodeLegacyServicesSort,
} from './servicesTable';

// v0.8.251 — /services moved its hand-rolled server-sort header onto the
// shared DataTable primitive's serverSort mode. These pin the two page-side
// contracts the migration must not drift on:
//  • decodeLegacyServicesSort — pre-v0.8.251 shared links used the backend's
//    own `?sort=&dir=` pair; the bridge must keep decoding them (old links
//    must not break) while new writes go to `s_services`.
//  • sanitizeServicesSort — the primitive persists sort ids in localStorage
//    + the URL, so a stale/unknown id must never leak into the backend's
//    ORDER BY.

describe('SERVICE_COLS — server-sort column contract', () => {
  it('keeps the exact pre-migration ?sort= keys', () => {
    expect(SERVICE_COLS.map(c => c.id))
      .toEqual(['name', 'spanCount', 'errorRate', 'avg', 'p99', 'apdex']);
  });
  it('keeps the pre-migration natural directions (NATURAL_DIR parity)', () => {
    // name alphabetical asc; apdex asc (worst services first); the
    // volume/latency columns default desc (biggest first).
    const dirs = Object.fromEntries(SERVICE_COLS.map(c => [c.id, c.naturalDir ?? 'desc']));
    expect(dirs).toEqual({
      name: 'asc', spanCount: 'desc', errorRate: 'desc',
      avg: 'desc', p99: 'desc', apdex: 'asc',
    });
  });
  it('marks every column click-sortable (sortValue present) so DataTableHead renders the arrows', () => {
    expect(SERVICE_COLS.every(c => !!c.sortValue)).toBe(true);
  });
});

describe('decodeLegacyServicesSort — old ?sort=&dir= link bridge', () => {
  it('decodes a full legacy pair', () => {
    expect(decodeLegacyServicesSort('?sort=p99&dir=asc')).toEqual({ id: 'p99', dir: 'asc' });
    expect(decodeLegacyServicesSort('?sort=name&dir=desc')).toEqual({ id: 'name', dir: 'desc' });
  });
  it('falls back to the column natural direction when dir is missing', () => {
    expect(decodeLegacyServicesSort('?sort=spanCount')).toEqual({ id: 'spanCount', dir: 'desc' });
    expect(decodeLegacyServicesSort('?sort=apdex')).toEqual({ id: 'apdex', dir: 'asc' });
  });
  it('falls back to the column natural direction when dir is malformed', () => {
    expect(decodeLegacyServicesSort('?sort=errorRate&dir=down')).toEqual({ id: 'errorRate', dir: 'desc' });
    expect(decodeLegacyServicesSort('?sort=name&dir=')).toEqual({ id: 'name', dir: 'asc' });
  });
  it('returns null when the legacy param is absent or names an unknown column', () => {
    expect(decodeLegacyServicesSort('')).toBeNull();
    expect(decodeLegacyServicesSort('?range=30m&cluster=prod')).toBeNull();
    expect(decodeLegacyServicesSort('?sort=bogusCol&dir=asc')).toBeNull();
  });
  it('coexists with unrelated params on the same URL', () => {
    expect(decodeLegacyServicesSort('?cluster=prod&sort=avg&dir=asc&range=1h'))
      .toEqual({ id: 'avg', dir: 'asc' });
  });
});

describe('sanitizeServicesSort — dt.sort → backend ORDER BY pair', () => {
  it('passes a known column id through with its direction', () => {
    expect(sanitizeServicesSort({ id: 'p99', dir: 'asc' })).toEqual({ sort: 'p99', dir: 'asc' });
  });
  it('falls back to the default PAIR for a stale/unknown id (dir is just as untrusted)', () => {
    // v0.8.259 — operator request: landing sort is span volume, not
    // error rate. The fallback pair follows the default.
    expect(sanitizeServicesSort({ id: 'oldSchemaCol', dir: 'asc' }))
      .toEqual({ sort: 'spanCount', dir: 'desc' });
  });
  it('falls back for a null id (no active sort persisted)', () => {
    expect(sanitizeServicesSort({ id: null, dir: 'asc' }))
      .toEqual({ sort: DEFAULT_SERVICES_SORT.id, dir: DEFAULT_SERVICES_SORT.dir });
  });
});
