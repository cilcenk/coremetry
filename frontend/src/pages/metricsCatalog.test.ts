import { describe, it, expect } from 'vitest';
import {
  metricGroup, decodeFacet, facetUrlValue, decodeCatalogParams, applyCatalogParams,
  catalogCountLabel, facetCountsComplete, nextCatalogLimit,
  CATALOG_PAGE, CATALOG_MAX,
  type MFacet,
} from './metricsCatalog';

// v0.9.832 — /metrics katalog dürüstlüğü + servis filtresi + URL-state.
//
// Üç ayrı bug sınıfını çiviliyor:
//   • facet/sayaç yalanı (sunucunun `total`ı atılıyordu),
//   • kodek asimetrisi (yaz/oku farklı varsayılan → seçim yutulur;
//     v0.8.253/256/265/267 ve v0.9.561 aynı sınıf),
//   • yabancı param düşürme (range/editor'ü ezmek).

describe('metricGroup', () => {
  const cases: [string, string][] = [
    ['http.server.request.duration', 'http'],
    ['HTTP.client.duration', 'http'],
    ['rpc.server.duration', 'rpc'],
    ['db.client.connections.usage', 'db'],
    ['database_query_duration', 'db'],
    ['oracledb.sessions.active', 'db'],
    ['messaging.publish.duration', 'messaging'],
    ['kafka.consumer.lag', 'messaging'],
    ['jvm.gc.duration', 'runtime'],
    ['process.runtime.goroutines', 'runtime'],
    ['system.cpu.utilization', 'runtime'],
    ['banking.transfers.rate', 'other'],
    ['cache.hit_ratio', 'other'],
  ];
  it.each(cases)('%s → %s', (name, want) => {
    expect(metricGroup(name)).toBe(want);
  });
});

describe('facet codec round-trip', () => {
  const facets: MFacet[] = ['all', 'http', 'rpc', 'runtime', 'db', 'messaging', 'other'];
  it.each(facets)('%s round-trips', f => {
    expect(decodeFacet(facetUrlValue(f))).toBe(f);
  });

  it('default facet writes NO param', () => {
    expect(facetUrlValue('all')).toBeNull();
  });

  it.each([null, undefined, '', 'HTTP', 'bogus', 'All'])('unknown %s falls back to all', raw => {
    expect(decodeFacet(raw)).toBe('all');
  });
});

describe('decodeCatalogParams', () => {
  it('empty URL = empty selection', () => {
    expect(decodeCatalogParams(new URLSearchParams())).toEqual({ search: '', facet: 'all', service: '' });
  });

  it('reads all three', () => {
    const sp = new URLSearchParams('search=http&facet=db&service=api-gateway');
    expect(decodeCatalogParams(sp)).toEqual({ search: 'http', facet: 'db', service: 'api-gateway' });
  });

  it('bogus facet does not poison the other two', () => {
    const sp = new URLSearchParams('search=cpu&facet=nope&service=cart');
    expect(decodeCatalogParams(sp)).toEqual({ search: 'cpu', facet: 'all', service: 'cart' });
  });
});

describe('applyCatalogParams', () => {
  it('writes the non-default params', () => {
    const out = applyCatalogParams(new URLSearchParams(), { search: 'http', facet: 'db', service: 'cart' });
    expect(out.get('search')).toBe('http');
    expect(out.get('facet')).toBe('db');
    expect(out.get('service')).toBe('cart');
  });

  it('deletes params that fall back to the default', () => {
    const prev = new URLSearchParams('search=http&facet=db&service=cart');
    const out = applyCatalogParams(prev, { search: '', facet: 'all', service: '' });
    expect(out.has('search')).toBe(false);
    expect(out.has('facet')).toBe(false);
    expect(out.has('service')).toBe(false);
    expect(out.toString()).toBe('');
  });

  it('trims whitespace-only input to a deletion', () => {
    const out = applyCatalogParams(new URLSearchParams('search=x'), { search: '   ', facet: 'all', service: '  ' });
    expect(out.has('search')).toBe(false);
    expect(out.has('service')).toBe(false);
  });

  // Regresyon: yabancı param koruması (ev kuralı — prev KOPYALANIR).
  it('preserves foreign params (range, editor)', () => {
    const prev = new URLSearchParams('range=6h&editor=0&foo=bar');
    const out = applyCatalogParams(prev, { search: 'jvm', facet: 'runtime', service: '' });
    expect(out.get('range')).toBe('6h');
    expect(out.get('editor')).toBe('0');
    expect(out.get('foo')).toBe('bar');
    expect(out.get('search')).toBe('jvm');
  });

  it('round-trips through decode', () => {
    const p = { search: 'db.client', facet: 'db' as MFacet, service: 'orders-api' };
    expect(decodeCatalogParams(applyCatalogParams(new URLSearchParams(), p))).toEqual(p);
  });
});

describe('catalogCountLabel', () => {
  const cases: [number, number, string][] = [
    [0, 0, 'No metrics'],
    [114, 114, '114 metrics'],
    [1, 1, '1 metric'],
    [1240, 200, 'showing 200 of 1,240 metrics'],
    [1240, 400, 'showing 400 of 1,240 metrics'],
    // listed > total can only mean a stale total; never claim a prefix.
    [3, 5, '3 metrics'],
    [7, 0, '7 metrics'],
  ];
  it.each(cases)('total=%i listed=%i → %s', (total, listed, want) => {
    expect(catalogCountLabel(total, listed)).toBe(want);
  });
});

describe('facetCountsComplete', () => {
  it('complete when everything is listed', () => {
    expect(facetCountsComplete(114, 114)).toBe(true);
  });
  it('incomplete on a truncated prefix — the chips must say so', () => {
    expect(facetCountsComplete(1240, 200)).toBe(false);
  });
});

describe('nextCatalogLimit', () => {
  it('no more results → no button', () => {
    expect(nextCatalogLimit(CATALOG_PAGE, false)).toBeNull();
  });
  it('grows by a page', () => {
    expect(nextCatalogLimit(200, true)).toBe(400);
    expect(nextCatalogLimit(400, true)).toBe(600);
  });
  it('clamps to the server cap', () => {
    expect(nextCatalogLimit(900, true)).toBe(CATALOG_MAX);
  });
  it('stops at the server cap — refine instead of a silent truncation', () => {
    expect(nextCatalogLimit(CATALOG_MAX, true)).toBeNull();
    expect(nextCatalogLimit(CATALOG_MAX + 200, true)).toBeNull();
  });
});
