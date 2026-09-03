import { describe, it, expect } from 'vitest';
import { tracesExplainUrl } from './tracesExplainUrl';

// v0.10.328 — explain linki: boş/undefined/false atlanır, dizi virgül, explain=1 sonda.
describe('tracesExplainUrl', () => {
  it('parametreleri taşır, boşları atlar, explain=1 ekler', () => {
    const u = tracesExplainUrl({ from: 1, to: 2, service: 'svc', search: 'POST /x', hasError: false, filters: undefined, services: ['a', 'b'], count: 'skip' })!;
    const q = new URLSearchParams(u.slice(u.indexOf('?') + 1));
    expect(u.startsWith('/api/traces?')).toBe(true);
    expect(q.get('service')).toBe('svc');
    expect(q.get('search')).toBe('POST /x');
    expect(q.get('services')).toBe('a,b');
    expect(q.has('hasError')).toBe(false);
    expect(q.has('filters')).toBe(false);
    expect(q.get('explain')).toBe('1');
  });
  it('parametre yoksa null', () => {
    expect(tracesExplainUrl(null)).toBeNull();
  });
});
