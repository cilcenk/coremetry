import { describe, it, expect } from 'vitest';
import { withProblemParam } from './problemLink';

// v0.8.256 — operator-reported: "spesifik bir problemi link olarak
// paylaşamıyorum, URL hep /problems". The drawer seeded FROM
// ?problem= but never wrote back. These cases pin the both-ways
// URL contract so a future drawer refactor can't silently drop it.
describe('withProblemParam', () => {
  it('open: sets ?problem= and keeps existing params', () => {
    const prev = new URLSearchParams('range=30m&service=checkout');
    const next = withProblemParam(prev, 'prob-123');
    expect(next.get('problem')).toBe('prob-123');
    expect(next.get('range')).toBe('30m');
    expect(next.get('service')).toBe('checkout');
  });

  it('close: removes ?problem= and keeps existing params', () => {
    const prev = new URLSearchParams('problem=prob-123&range=30m');
    const next = withProblemParam(prev, null);
    expect(next.get('problem')).toBeNull();
    expect(next.get('range')).toBe('30m');
  });

  it('switching problems replaces the id instead of duplicating', () => {
    const prev = new URLSearchParams('problem=old');
    const next = withProblemParam(prev, 'new');
    expect(next.getAll('problem')).toEqual(['new']);
  });

  it('does not mutate the input params', () => {
    const prev = new URLSearchParams('problem=keep');
    withProblemParam(prev, null);
    expect(prev.get('problem')).toBe('keep');
  });
});
