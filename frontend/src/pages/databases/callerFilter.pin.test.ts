import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

// v0.10.704 — "Called from services" filtresi TEK üreticiden: tablo ve
// sayfa sayacı aynı saf predicate'i çağırır; tabloda eski yerel predicate
// (nameOf ile db adı yalnız unknown instance'ta) geri gelmesin.
const read = (p: string) => readFileSync(resolve(__dirname, '../../', p), 'utf8');

describe('caller filter tek üretici', () => {
  it('DependenciesTable saf predicate kullanır, yerel OR zinciri yok', () => {
    const src = read('components/DependenciesTable.tsx');
    expect(src).toContain('depRowMatches(r, term, systemFilter)');
    expect(src).not.toContain('nameOf(r).toLowerCase().includes(term)');
    expect(src).toContain("setParam('q', v.trim() === '' ? '' : v)");
  });
  it('Databases başlık sayacı aynı predicate ile q/msys okur', () => {
    const src = read('pages/Databases.tsx');
    expect(src).toContain("normalizeDepSearch(sp.get('q'))");
    expect(src).toContain("sp.get('msys')");
    expect(src).toContain('depRowMatches(r, callerTerm, callerSystem)');
    expect(src).toContain('Called from services (${callerCountLabel})');
  });
});
