import { describe, expect, it } from 'vitest';
import { trendsEnabled, latencyPresent, type DepKind, resolveTrends } from './depsTable';
import type { DBTrend } from '@/lib/types';

// v0.9.258 regression. DependenciesTable declared the Trend column and
// fired its /api/databases/trends fetch unconditionally, for both kinds.
// db_summary_5m is built `WHERE db_system != ''`, so a queue row could
// never join a trend: /messaging showed a permanently '—' column and
// paid a LIMIT 200000 ClickHouse scan for it on every range change.
//
// The predicate is trivial by design — its value is that the column
// definition, the fetch effect and the body <td> all read THIS function.
// A future edit that re-enables one site without the others desyncs the
// header from the row cells, which nothing else would catch.
describe('trendsEnabled', () => {
  const cases: Array<[DepKind, boolean]> = [
    ['db', true],
    // v0.9.434 — bilinçli pin değişimi: queue artık KENDİ endpoint'inden
    // (messaging/trends, messaging_summary_5m) beslenir; v0.9.258'in
    // kapattığı "yanlış MV + boşa tarama" kusuru yok.
    ['queue', true],
  ];
  it.each(cases)('%s → %s', (kind, want) => {
    expect(trendsEnabled(kind)).toBe(want);
  });

  it('queue is included — served by its own messaging/trends endpoint (v0.9.434)', () => {
    // v0.9.258 bu satırı 'db_summary_5m messaging satırı içeremez'
    // gerekçesiyle kapatmıştı; kusur endpoint'in yanlışlığıydı, kolonun
    // kendisi değil. Fetch effect'i kind'a göre endpoint seçer.
    expect(trendsEnabled('queue')).toBe(true);
  });
});

// v0.9.262 regression. Receiver-discovered rows have no duration data at all,
// but the Go fields are plain float64 and marshal as 0 — so the grid printed
// "0.0ms" and a database with zero application traffic read as the fastest
// row on the page, contradicting its own "receiver" badge.
describe('latencyPresent', () => {
  const cases: Array<[string, 'spans' | 'receiver' | undefined, number | undefined, boolean]> = [
    ['receiver row, zero value → absent',           'receiver',  0,         false],
    ['receiver row, nonzero value → still absent',  'receiver',  12.5,      false],
    ['span row, value undefined → absent',          'spans',     undefined, false],
    ['no source, value undefined → absent',         undefined,   undefined, false],
    ['span row with a real value → present',        'spans',     12.5,      true],
    ['no source but value present → present',       undefined,   12.5,      true],
  ];
  it.each(cases)('%s', (_label, source, v, want) => {
    expect(latencyPresent(source, v)).toBe(want);
  });

  it('a measured 0 on a span row IS present', () => {
    // A genuine sub-0.05ms p50 rounds to 0.0 and is a real measurement.
    // Only the SOURCE tells us a value was never measured — treating
    // value-zero as absent would hide real (very fast) rows behind '—'.
    expect(latencyPresent('spans', 0)).toBe(true);
  });

  it('receiver rows are absent regardless of value — the 0.0ms defect', () => {
    // discoverReceiverInstances builds rows from metric_points with no
    // duration data; the Go fields are plain float64 so they marshal as 0.
    // Rendering that as "0.0ms" made a database with zero application
    // traffic sort to the top as the fastest row on the page.
    expect(latencyPresent('receiver', 0)).toBe(false);
  });
});

// v0.10.576 — resolveTrends: trend sütununun üç durumu.
//
// Sütun React Query'ye taşındı; bu türetme bileşenin içinde kalsaydı hiçbir
// kapı görmezdi. Asıl korunan sözleşme SIRA: kapalı sütun "yükleniyor"dan
// ÖNCE elenmeli, çünkü devre dışı bir RQ sorgusunda isPending KALICI olarak
// true'dur — sıra ters olsa çizilmeyen sütuna sonsuz spinner park ederdi
// (v0.9.258'in tekrarı).
describe('resolveTrends', () => {
  const t = (dbSystem: string, instance: string, dbName: string, cluster = '') =>
    ({ dbSystem, instance, dbName, cluster } as unknown as DBTrend);

  it('kapalı sütun null döner — pending true OLSA BİLE', () => {
    expect(resolveTrends({ enabled: false, pending: true, list: undefined, kind: 'queue' })).toBeNull();
  });

  it('açık + pending → undefined (spinner)', () => {
    expect(resolveTrends({ enabled: true, pending: true, list: undefined, kind: 'db' })).toBeUndefined();
  });

  it('okuma başarısız / boş cevap → null, undefined DEĞİL', () => {
    expect(resolveTrends({ enabled: true, pending: false, list: null, kind: 'db' })).toBeNull();
  });

  it('db: tam anahtar + gevşek yedek, ilk yazan kazanır', () => {
    const real = t('postgres', 'pg-1', 'orders');
    const dflt = t('postgres', 'pg-1', 'default');
    const m = resolveTrends({ enabled: true, pending: false, list: [real, dflt], kind: 'db' })!;
    expect(m.get('postgres|pg-1|orders')).toBe(real);
    expect(m.get('postgres|pg-1|default')).toBe(dflt);
    // Gevşek anahtarı İLK giren (gerçek db.name'li) tutar.
    expect(m.get('postgres|pg-1')).toBe(real);
  });

  it('queue: anahtar cluster İÇERİR — aynı destination iki cluster ezişmez', () => {
    const a = t('kafka', 'orders', '', 'prod-eu');
    const b = t('kafka', 'orders', '', 'dr-eu');
    const m = resolveTrends({ enabled: true, pending: false, list: [a, b], kind: 'queue' })!;
    expect(m.get('kafka|prod-eu|orders')).toBe(a);
    expect(m.get('kafka|dr-eu|orders')).toBe(b);
    expect(m.size).toBe(2);
  });
});
