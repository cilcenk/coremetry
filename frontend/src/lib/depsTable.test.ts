import { describe, expect, it } from 'vitest';
import { trendsEnabled, latencyPresent, type DepKind } from './depsTable';

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
