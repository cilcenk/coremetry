import { describe, expect, it } from 'vitest';
import { trendsEnabled, type DepKind } from './depsTable';

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
    ['queue', false],
  ];
  it.each(cases)('%s → %s', (kind, want) => {
    expect(trendsEnabled(kind)).toBe(want);
  });

  it('queue is excluded — db_summary_5m cannot contain messaging rows', () => {
    // Pinned as its own case because this is the actual defect, not a
    // style preference: re-enabling it would restore both the dead
    // column and the wasted scan.
    expect(trendsEnabled('queue')).toBe(false);
  });
});
