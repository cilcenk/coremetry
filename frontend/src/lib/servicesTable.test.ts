import { describe, it, expect } from 'vitest';
import {
  SERVICE_COLS, DEFAULT_SERVICES_SORT,
  sanitizeServicesSort, decodeLegacyServicesSort,
} from './servicesTable';

// v0.8.251 — /services moved its hand-rolled server-sort header onto the
// shared DataTable primitive's serverSort mode. These pin the two page-side
// contracts the migration must not drift on:
//  • decodeLegacyServicesSort — pre-v0.8.251 shared links used the backend's
//    own `?sort=&dir=` pair; the bridge must keep decoding them (old links
//    must not break) while new writes go to `s_services`.
//  • sanitizeServicesSort — the primitive persists sort ids in localStorage
//    + the URL, so a stale/unknown id must never leak into the backend's
//    ORDER BY.

describe('SERVICE_COLS — server-sort column contract', () => {
  it('keeps the exact pre-migration ?sort= keys (+ p99Delta v0.9.1111, lastSeen v0.9.1317)', () => {
    expect(SERVICE_COLS.map(c => c.id))
      .toEqual(['name', 'spanCount', 'errorRate', 'avg', 'p99', 'p99Delta', 'apdex', 'lastSeen']);
  });
  it('keeps the pre-migration natural directions (NATURAL_DIR parity)', () => {
    // name alphabetical asc; apdex asc (worst services first); the
    // volume/latency columns default desc (biggest first); p99Delta
    // desc = the most-worsened service first (v0.9.1111). lastSeen has
    // no natural direction that matters — it is not click-sortable.
    const dirs = Object.fromEntries(SERVICE_COLS.map(c => [c.id, c.naturalDir ?? 'desc']));
    expect(dirs).toEqual({
      name: 'asc', spanCount: 'desc', errorRate: 'desc',
      avg: 'desc', p99: 'desc', p99Delta: 'desc', apdex: 'asc',
      lastSeen: 'desc',
    });
  });

  // v0.9.1317 — this used to assert `every(c => !!c.sortValue)`. The fear it
  // encoded was never "all columns must be sortable" for its own sake; it was
  // that a column whose id the BACKEND cannot turn into an ORDER BY must not
  // look sortable, because servicesAggSortExpr's whitelist silently falls
  // through to `spans DESC` and the operator gets a table that claims one
  // ordering and serves another. So the assertion is narrowed rather than
  // dropped: the sortable set must be exactly the backend's key set, and
  // lastSeen — joined in Go AFTER pagination, absent from service_summary_5m
  // — must stay out of it.
  const BACKEND_SORT_KEYS = ['name', 'spanCount', 'errorRate', 'avg', 'p99', 'p99Delta', 'apdex'];

  it('marks exactly the backend-sortable columns click-sortable (sortValue present)', () => {
    expect(SERVICE_COLS.filter(c => !!c.sortValue).map(c => c.id)).toEqual(BACKEND_SORT_KEYS);
  });
  it('leaves lastSeen non-sortable — the backend cannot ORDER BY it', () => {
    const lastSeen = SERVICE_COLS.find(c => c.id === 'lastSeen');
    expect(lastSeen).toBeDefined();
    expect(lastSeen!.sortValue).toBeUndefined();
  });
});

describe('decodeLegacyServicesSort — old ?sort=&dir= link bridge', () => {
  it('decodes a full legacy pair', () => {
    expect(decodeLegacyServicesSort('?sort=p99&dir=asc')).toEqual({ id: 'p99', dir: 'asc' });
    expect(decodeLegacyServicesSort('?sort=name&dir=desc')).toEqual({ id: 'name', dir: 'desc' });
  });
  it('falls back to the column natural direction when dir is missing', () => {
    expect(decodeLegacyServicesSort('?sort=spanCount')).toEqual({ id: 'spanCount', dir: 'desc' });
    expect(decodeLegacyServicesSort('?sort=apdex')).toEqual({ id: 'apdex', dir: 'asc' });
  });
  it('falls back to the column natural direction when dir is malformed', () => {
    expect(decodeLegacyServicesSort('?sort=errorRate&dir=down')).toEqual({ id: 'errorRate', dir: 'desc' });
    expect(decodeLegacyServicesSort('?sort=name&dir=')).toEqual({ id: 'name', dir: 'asc' });
  });
  it('returns null when the legacy param is absent or names an unknown column', () => {
    expect(decodeLegacyServicesSort('')).toBeNull();
    expect(decodeLegacyServicesSort('?range=30m&cluster=prod')).toBeNull();
    expect(decodeLegacyServicesSort('?sort=bogusCol&dir=asc')).toBeNull();
  });
  it('returns null for a column that exists but is NOT sortable (v0.9.1317)', () => {
    // A hand-edited link naming the lifecycle column must not seed the
    // hook's sort state — sanitizeServicesSort would drop it anyway, and
    // in between the header would render as an active sort that the
    // backend never honoured.
    expect(decodeLegacyServicesSort('?sort=lastSeen&dir=desc')).toBeNull();
  });
  it('coexists with unrelated params on the same URL', () => {
    expect(decodeLegacyServicesSort('?cluster=prod&sort=avg&dir=asc&range=1h'))
      .toEqual({ id: 'avg', dir: 'asc' });
  });
});

describe('sanitizeServicesSort — dt.sort → backend ORDER BY pair', () => {
  it('passes a known column id through with its direction', () => {
    expect(sanitizeServicesSort({ id: 'p99', dir: 'asc' })).toEqual({ sort: 'p99', dir: 'asc' });
  });
  it('falls back to the default PAIR for a stale/unknown id (dir is just as untrusted)', () => {
    // v0.8.259 — operator request: landing sort is span volume, not
    // error rate. The fallback pair follows the default.
    expect(sanitizeServicesSort({ id: 'oldSchemaCol', dir: 'asc' }))
      .toEqual({ sort: 'spanCount', dir: 'desc' });
  });
  it('falls back for a null id (no active sort persisted)', () => {
    expect(sanitizeServicesSort({ id: null, dir: 'asc' }))
      .toEqual({ sort: DEFAULT_SERVICES_SORT.id, dir: DEFAULT_SERVICES_SORT.dir });
  });
  it('falls back for a column that EXISTS but the backend cannot sort (v0.9.1317)', () => {
    // The regression this guards: `lastSeen` reaching servicesAggSortExpr,
    // matching no case, and being served as `spans DESC` under a header
    // that says "Last seen". Membership in SERVICE_COLS is no longer
    // sufficient — sortability is the gate.
    expect(sanitizeServicesSort({ id: 'lastSeen', dir: 'desc' }))
      .toEqual({ sort: 'spanCount', dir: 'desc' });
  });
});

// v0.9.1329 — /services başlıkları TEK DİL. v0.9.1317 buraya 'Son görülme'
// koydu ve sayfanın diğer yedi başlığı İngilizceydi; operatör kararı
// İngilizce oldu. Bu gate string eşitliği DEĞİL (kırılgan olurdu, her
// yeniden adlandırma testi kırardı) — Türkçeye ÖZGÜ harf arıyor.
//
// ⚠ İlk yazımı "non-ASCII" idi ve KENDİ TESTİM YAKALADI: 'P99 Δ' (Yunan
// deltası) düştü. Δ meşru bir matematik simgesi, Türkçe değil — yani
// "non-ASCII" ile "Türkçe" aynı şey değil ve fazla geniş bir yüklem
// doğru başlığı suçluyordu. Yüklem artık ç/ğ/ı/İ/ö/ş/ü ailesine bakıyor;
// Δ, µ, ° gibi simgeler serbest.
//
// Kapsam bilinçli olarak yalnız SERVICE_COLS: bu sayfanın başlıkları
// İngilizce, ürünün geri kalanı (Settings, AnomalyTab…) Türkçe ve orada
// aynı kural GEÇERSİZ. Gate'i genişletmek yanlış olur.
const TURKISH_LETTERS = /[çÇğĞıİöÖşŞüÜ]/;

describe('SERVICE_COLS başlık dili', () => {
  it('hiçbir başlıkta Türkçeye özgü harf yok', () => {
    const turkish = SERVICE_COLS
      .filter(c => TURKISH_LETTERS.test(c.label))
      .map(c => `${c.id}: ${c.label}`);
    expect(turkish).toEqual([]);
  });

  it('matematik simgeleri serbest — yüklem fazla geniş değil', () => {
    // Negatif kontrol: gate'in Δ'ya ısırmadığını AÇIKÇA çiviler, yoksa
    // biri onu "non-ASCII" diye geri genişletir ve P99 Δ yine düşer.
    expect(TURKISH_LETTERS.test('P99 Δ')).toBe(false);
    expect(TURKISH_LETTERS.test('Son görülme')).toBe(true);
  });
});
