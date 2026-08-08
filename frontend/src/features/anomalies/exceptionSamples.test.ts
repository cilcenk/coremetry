// exceptionSamples.test.ts — v0.9.795 (operator-reported): the empty-state
// sentence must distinguish all three endings of the paged sample scan.
// Table-driven over every branch: a wrong branch here is how a fixable
// "we gave up early" reads as an unfixable "there is nothing".
import { describe, expect, it } from 'vitest';
import { emptySamplesNote } from './exceptionSamples';

describe('emptySamplesNote', () => {
  const fallback = 'No sample traces.';

  it('reports the REAL cumulative candidate count when capped, not a hardcoded 500', () => {
    const note = emptySamplesNote({ scanned: 5000, scanCapped: true }, fallback);
    expect(note.warn).toBe(true);
    // Locale-agnostic: the grouping separator differs per runtime locale.
    expect(note.text).toMatch(/5[.,  ]?000/);
    expect(note.text).not.toContain('500 aday tarandı');
  });

  it('says the window was read to the end — no scan-budget excuse', () => {
    const note = emptySamplesNote({ scanned: 812, windowExhausted: true }, fallback);
    expect(note.warn).toBe(false);
    expect(note.text).toContain('812');
    expect(note.text).toContain('retansiyon');
    expect(note.text).not.toContain('tavan');
  });

  it('prefers the cap message when both flags somehow arrive', () => {
    const note = emptySamplesNote({ scanned: 5000, scanCapped: true, windowExhausted: true }, fallback);
    expect(note.text).toContain('tavan');
  });

  it('falls back to the surface plain line for an uninteresting empty scan', () => {
    expect(emptySamplesNote({ scanned: 0 }, fallback)).toEqual({ warn: false, text: fallback });
  });

  it('falls back when there is no envelope at all', () => {
    expect(emptySamplesNote(null, fallback).text).toBe(fallback);
    expect(emptySamplesNote(undefined, fallback).text).toBe(fallback);
  });
});
