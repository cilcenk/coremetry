// databaseParam — v0.9.840. The /database detail page's URL contract,
// and the one redirect that keeps pre-v0.9.840 `?row=` deep links alive.
//
// IDENTITY IS A TRIPLE (v0.9.821): (system, instance, dbName). Not a
// label. DependenciesTable's nameOf() prints db.name as the LABEL when
// instance is 'unknown', and a surface that treats that label as the
// identity asks the store for instance=<db.name>, which matches nothing
// — a silently empty panel. The page therefore reads all three fields
// separately and never derives one from another.

/** Ref — the three identity fields plus the row's data ORIGIN, which
 *  decides whether the receiver-only engine panel may render. */
export interface DatabaseRef {
  system: string;
  instance: string;
  /** '' = every database on this instance. A real, statable state — the
   *  drawer said so out loud and so does the page. */
  dbName: string;
  source: 'spans' | 'receiver';
}

export const DATABASE_PAGE = '/database';

export interface DatabasePageScope {
  range?: string;
  env?: string;
}

/**
 * databaseDetailHref — build the /database URL for one row.
 * `name` is the db.name field; omitted when empty so the URL does not
 * imply a narrowing that isn't there.
 */
export function databaseDetailHref(
  ref: DatabaseRef, scope: DatabasePageScope = {},
): string {
  const p = new URLSearchParams();
  p.set('system', ref.system);
  p.set('instance', ref.instance);
  if (ref.dbName) p.set('name', ref.dbName);
  if (ref.source === 'receiver') p.set('source', 'receiver');
  if (scope.range) p.set('range', scope.range);
  if (scope.env) p.set('env', scope.env);
  return `${DATABASE_PAGE}?${p.toString()}`;
}

// databasesFilterHref — v0.9.964 (UX denetimi Ö9 / G2, DB yarısı). The
// producer for "show me this database in the catalogue", from a row that
// knows the engine and the db.name but NOT the instance.
//
// Why not databaseDetailHref: identity there is a TRIPLE (system, instance,
// dbName) and a DB STATEMENT row has no instance — statements aggregate
// across every instance the engine runs on. Inventing one would open a
// silently empty detail page (the v0.9.821 label-as-identity failure). The
// honest destination is the catalogue, narrowed as far as the row's own
// dimensions actually go.
//
// Two narrowings the row must NOT assert:
//   • dbName === 'default' — that is the not-found sentinel on both sides
//     (the raw-spans coalesce AND db_summary_5m), i.e. "these spans carried
//     no db.name". Filtering on it reads in the UI as a database literally
//     named "default".
//   • dbNameCount > 1 — the statement grouping deliberately FOLDS db_name,
//     so the name on the row is a representative one of several. Pinning it
//     would drop the other databases the very same statement ran against.
// In both cases the engine filter still ships: a narrower-than-possible
// catalogue beats a confidently wrong one.
export function databasesFilterHref(
  row: { dbSystem?: string; dbName?: string; dbNameCount?: number },
  scope: DatabasePageScope = {},
): string {
  const p = new URLSearchParams();
  const sys = (row.dbSystem ?? '').trim();
  const name = (row.dbName ?? '').trim();
  if (sys) p.set('dbsys', sys);
  if (name && name !== 'default' && (row.dbNameCount ?? 1) <= 1) p.set('dbname', name);
  if (scope.range) p.set('range', scope.range);
  if (scope.env) p.set('env', scope.env);
  const qs = p.toString();
  return qs ? `/databases?${qs}` : '/databases';
}

/**
 * parseDatabasePageRef — the page's own params back into a ref.
 * system + instance are required; a link missing either renders the
 * page's honest "no database in this link" state rather than querying
 * for the empty string.
 */
export function parseDatabasePageRef(search: string): DatabaseRef | null {
  const p = new URLSearchParams(search);
  const system = p.get('system') ?? '';
  const instance = p.get('instance') ?? '';
  if (!system || !instance) return null;
  return {
    system,
    instance,
    dbName: p.get('name') ?? '',
    source: p.get('source') === 'receiver' ? 'receiver' : 'spans',
  };
}

/**
 * legacyDatabaseRowTarget — the ONE redirect for pre-v0.9.840 `?row=`
 * deep links, which used to open the inline row drawer on /databases.
 *
 * The param is depRowKey's `system|cluster|instance|dbName`. For DB
 * rows cluster is always '' (no cluster dimension), so a 4-field key
 * with a non-empty cluster belongs to /messaging and is left alone —
 * both tables share the param, and hijacking messaging's would send an
 * operator looking at a Kafka topic to a database page.
 *
 * The origin (spans / receiver) is NOT in the key, so the redirect
 * cannot know it and does not guess: it omits `source`, and the page
 * defaults to 'spans', which merely hides the receiver-only engine
 * panel. Guessing 'receiver' would render an engine panel for a row
 * that may have none — inventing a claim to avoid an omission.
 *
 * Returns null for anything that is not a decodable DB row key.
 */
export function legacyDatabaseRowTarget(search: string): string | null {
  const p = new URLSearchParams(search);
  const raw = p.get('row');
  if (!raw) return null;
  const parts = raw.split('|');
  if (parts.length !== 4) return null;
  const [system, cluster, instance, dbName] = parts;
  if (!system || !instance) return null;
  if (cluster) return null; // a messaging row — not ours to redirect
  return databaseDetailHref(
    { system, instance, dbName, source: 'spans' },
    { range: p.get('range') ?? undefined, env: p.get('env') ?? undefined },
  );
}
