import { describe, it, expect } from 'vitest';
import { withProblemParam, withExcParam, problemDetailHref, excDetailHref } from './problemLink';

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

// v0.8.443 (operator-reported): the exception-group full detail had the
// same "can't share as a link" gap ?problem= was fixed for in v0.8.256 —
// it only ever lived in local state, never the URL. Same both-ways
// contract, pinned the same way.
describe('withExcParam', () => {
  it('open: sets ?exc= and keeps existing params', () => {
    const prev = new URLSearchParams('range=30m&service=checkout');
    const next = withExcParam(prev, 'fp-abc');
    expect(next.get('exc')).toBe('fp-abc');
    expect(next.get('range')).toBe('30m');
    expect(next.get('service')).toBe('checkout');
  });

  it('close: removes ?exc= and keeps existing params', () => {
    const prev = new URLSearchParams('exc=fp-abc&range=30m');
    const next = withExcParam(prev, null);
    expect(next.get('exc')).toBeNull();
    expect(next.get('range')).toBe('30m');
  });

  it('switching groups replaces the fingerprint instead of duplicating', () => {
    const prev = new URLSearchParams('exc=old');
    const next = withExcParam(prev, 'new');
    expect(next.getAll('exc')).toEqual(['new']);
  });

  it('does not collide with the ?exception= inline-expand seed', () => {
    const prev = new URLSearchParams('exception=fp-inline');
    const next = withExcParam(prev, 'fp-full');
    expect(next.get('exception')).toBe('fp-inline');
    expect(next.get('exc')).toBe('fp-full');
  });

  it('does not mutate the input params', () => {
    const prev = new URLSearchParams('exc=keep');
    withExcParam(prev, null);
    expect(prev.get('exc')).toBe('keep');
  });
});

// v0.10.221 — satır hücrelerinin gerçek href'i (orta tık / ⌘-tık yeni sekme).
// Aynı param sözleşmesi, sayfa yolu korunur (Inbox'ta çizilen bölüm /inbox'ta
// kalır), diğer paramlar (range/filtre) korunur, id yenisiyle DEĞİŞİR.
describe('problemDetailHref / excDetailHref', () => {
  it('pathname + ?problem=, diğer paramlar korunur', () => {
    const prev = new URLSearchParams('range=30m&service=checkout');
    expect(problemDetailHref('/problems', prev, 'p1')).toBe('/problems?range=30m&service=checkout&problem=p1');
  });
  it('Inbox\'ta çizilince /inbox yolunda kalır', () => {
    expect(problemDetailHref('/inbox', new URLSearchParams(''), 'p1')).toBe('/inbox?problem=p1');
  });
  it('açık bir problem varken id yenisiyle değişir, çoğalmaz', () => {
    const h = problemDetailHref('/problems', new URLSearchParams('problem=old'), 'new');
    expect(new URLSearchParams(h.slice(h.indexOf('?') + 1)).getAll('problem')).toEqual(['new']);
  });
  it('exception: pathname + ?exc=, mevcut ?exception= (inline seed) ile çakışmaz', () => {
    const prev = new URLSearchParams('exception=abc&range=1h');
    const h = excDetailHref('/problems', prev, 'fp9');
    const q = new URLSearchParams(h.slice(h.indexOf('?') + 1));
    expect(h.startsWith('/problems?')).toBe(true);
    expect(q.get('exc')).toBe('fp9');
    expect(q.get('exception')).toBe('abc');
    expect(q.get('range')).toBe('1h');
  });
  it('girdi paramlarını değiştirmez', () => {
    const prev = new URLSearchParams('range=30m');
    problemDetailHref('/problems', prev, 'p1'); excDetailHref('/problems', prev, 'f1');
    expect(prev.toString()).toBe('range=30m');
  });
});
