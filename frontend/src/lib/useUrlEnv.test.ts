import { describe, expect, it } from 'vitest';
import { normalizeEnv, resolveEnv } from './useUrlEnv';

// v0.8.383 — env-separation Phase 1: the ?env= URL codec behind the
// global Topbar picker. Pins the precedence contract (URL > explicit
// empty > sticky store > all) so a shared "all environments" link can
// never be silently overridden by the reader's local sticky pick.

describe('normalizeEnv', () => {
  it('empty-ish inputs mean "all environments"', () => {
    expect(normalizeEnv(null)).toBe('');
    expect(normalizeEnv(undefined)).toBe('');
    expect(normalizeEnv('')).toBe('');
    expect(normalizeEnv('   ')).toBe('');
  });

  it('trims whitespace', () => {
    expect(normalizeEnv('  uat ')).toBe('uat');
  });

  it('caps crafted over-long values', () => {
    const junk = 'x'.repeat(500);
    expect(normalizeEnv(junk)).toHaveLength(64);
  });
});

describe('resolveEnv', () => {
  it('URL value wins over the stored pick', () => {
    expect(resolveEnv('prep', 'uat')).toBe('prep');
  });

  it('absent param falls back to the stored sticky pick', () => {
    expect(resolveEnv(null, 'uat')).toBe('uat');
  });

  it('explicitly EMPTY param means "all environments" — the sticky pick must not resurrect', () => {
    // URLSearchParams.get('env') returns '' for "?env=" and null when
    // the param is absent; only the latter may inherit the store.
    expect(resolveEnv('', 'uat')).toBe('');
    expect(resolveEnv('   ', 'uat')).toBe('');
  });

  it('nothing anywhere = all environments', () => {
    expect(resolveEnv(null, null)).toBe('');
  });

  it('normalizes both sources', () => {
    expect(resolveEnv(' int ', null)).toBe('int');
    expect(resolveEnv(null, ' int ')).toBe('int');
  });
});
