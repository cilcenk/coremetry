import { describe, it, expect } from 'vitest';
import { teamOptionsCI } from './teamOptions';

// v0.8.330 — operator-reported: SRE/owner team names arrive from span
// resource attrs with inconsistent casing ("avengerSY" vs "Avengersy"), and
// the filter dropdowns built a case-SENSITIVE Set — the same team listed as
// two separate options. The filter match itself was already EqualFold
// backend-side, so the fix is canonical, case-insensitive OPTION building:
// one entry per team, display = the most frequent original casing (tie →
// lexicographically first for determinism), sorted case-insensitively.
describe('teamOptionsCI', () => {
  it('merges casing variants into one option', () => {
    expect(teamOptionsCI(['avengerSY', 'Avengersy', 'avengerSY'])).toEqual(['avengerSY']);
  });

  it('majority casing wins the display form', () => {
    expect(teamOptionsCI(['Payments-SRE', 'payments-sre', 'Payments-SRE'])).toEqual(['Payments-SRE']);
  });

  it('tie resolves deterministically (lexicographically first)', () => {
    expect(teamOptionsCI(['CORE', 'core'])).toEqual(['CORE']);
    expect(teamOptionsCI(['core', 'CORE'])).toEqual(['CORE']);
  });

  it('sorts case-insensitively across distinct teams', () => {
    expect(teamOptionsCI(['zeta', 'Alpha', 'beta'])).toEqual(['Alpha', 'beta', 'zeta']);
  });

  it('skips empty/undefined and trims whitespace variants', () => {
    expect(teamOptionsCI(['', undefined, ' ops ', 'Ops'])).toEqual(['Ops']);
  });
});
