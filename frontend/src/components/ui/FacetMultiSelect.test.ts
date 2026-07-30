import { describe, it, expect } from 'vitest';
import { facetSummary } from './FacetMultiSelect';

// v0.9.357 — the button label is the whole point of mockup C: the bar must be
// readable WITHOUT opening the panel. If this lies, the operator has to open
// every dropdown to know what they're looking at.
describe('facetSummary', () => {
  it('full selection reads tümü', () => {
    expect(facetSummary(['P1', 'P2', 'P3'], 3)).toBe('tümü');
  });
  it('short selections list the labels and count the closed', () => {
    expect(facetSummary(['Exceptions'], 4)).toBe('Exceptions +3 kapalı');
    expect(facetSummary(['P1', 'P2'], 3)).toBe('P1 + P2 +1 kapalı');
  });
  it('long selections collapse to a count', () => {
    expect(facetSummary(['a', 'b', 'c'], 4)).toBe('3 seçili +1 kapalı');
  });
  it('over-selection never goes negative', () => {
    // Defensive: a stale selected set larger than options must not render
    // "+-1 kapalı".
    expect(facetSummary(['a', 'b'], 2)).toBe('tümü');
  });
});
