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
