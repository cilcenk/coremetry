// Pure decisions shared by DependenciesTable's three trend-column sites.
//
// v0.9.258. The Trend column is fed by /api/databases/trends →
// chstore.GetDBTrends → `FROM db_summary_5m`, and that MV is defined
// `WHERE db_system != ''` (internal/chstore/store.go:2513). A messaging
// span carries messaging.system and leaves db_system empty, so a queue
// row's join key (`kafka|orders-topic|`) can never appear in that table:
// on /messaging the column rendered the muted '—' 100% of the time while
// the page paid a LIMIT 200000 scan for it on every range change.
//
// This lives as one exported predicate rather than three inline
// `kind === 'db'` checks because the column definition, the fetch effect
// and the body <td> must agree EXACTLY. If they drift, the header and the
// row cells desync by one column — a silent layout corruption that no
// type error catches.
export type DepKind = 'db' | 'queue';

export function trendsEnabled(kind: DepKind): boolean {
  return kind === 'db';
}
