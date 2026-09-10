import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { slowQueryTracesHref, SLOW_QUERY_SNIPPET_LEN } from './tracesHref';
import { decodeFilters } from '@/lib/urlState';

// v0.10.652 — operatör isteği: Slow queries'te "Ne oldu?" (insight çipi)
// kalktı, "Search traces with this query" en sağda "Traces →" kolonu oldu.
// Saf href sözleşmesi + BAĞLANMA pinleri.

const row = { service: 'shop-payment', sampleStatement: 'insert into orders (id, amount, created_at) values (1, 2, 3)' };

describe('slowQueryTracesHref', () => {
  it('trace listesine gider: servis, pencere ve db.statement LIKE filtresi (ilk 60 karakter)', () => {
    const href = slowQueryTracesHref(row, { preset: '3h' });
    expect(href.startsWith('/traces')).toBe(true);
    const u = new URL(href, 'http://x');
    expect(u.searchParams.get('service')).toBe('shop-payment');
    expect(decodeURIComponent(href)).toContain('3h');
    const f = decodeFilters(u.searchParams.get('filters') ?? '');
    expect(f).toEqual([{ k: 'db.statement', op: 'LIKE', v: [row.sampleStatement.slice(0, SLOW_QUERY_SNIPPET_LEN)] }]);
  });
  it('uzun ifade 60 karakterde kesilir (LIKE deseni şişmez)', () => {
    const long = { service: 's', sampleStatement: 'x'.repeat(500) };
    const u = new URL(slowQueryTracesHref(long, { preset: '1h' }), 'http://x');
    const f = decodeFilters(u.searchParams.get('filters') ?? '');
    expect(f[0].v[0]).toBe('x'.repeat(SLOW_QUERY_SNIPPET_LEN));
  });
});

describe('BAĞLANMA (SlowQueries.tsx)', () => {
  const src = readFileSync(new URL('../SlowQueries.tsx', import.meta.url), 'utf8');
  it('Traces en sağda kolon; her satırda link; genişletilmiş satırda eski link yok', () => {
    const cols = src.slice(src.indexOf('const SLOW_COLS'), src.indexOf('];', src.indexOf('const SLOW_COLS')));
    const ids = Array.from(cols.matchAll(/id: '([a-zA-Z]+)'/g)).map(m => m[1]);
    expect(ids[ids.length - 1]).toBe('traces');
    expect(src).toContain('<Link to={slowQueryTracesHref(r, range)}');
    expect(src).not.toContain('Search traces with this query');
    // colSpan = 1 (chevron) + 11 kolon; yanlış sayı örnek satırını taşırır.
    expect(src).toContain('colSpan={12}');
  });
  it('"Ne oldu?" insight çipi ve yuvası bu sayfadan söküldü (operatör kararı)', () => {
    expect(src).not.toContain('InsightRowChip');
    expect(src).not.toContain('InsightRowSlot');
    expect(src).not.toContain('useInsightRow(');
  });
});
