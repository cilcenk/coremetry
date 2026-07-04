// Centralised query-key registry. Every useQuery call in the app
// derives its key from these factories, so:
//
//  1. Cache invalidation is type-safe — `qc.invalidateQueries({
//     queryKey: keys.problems.all })` doesn't drift away from the
//     hook's actual key.
//
//  2. Query keys form a tree where invalidating a parent ('problems')
//     invalidates every child ('problems open', 'problems for svc=x'),
//     so a mutation that creates a new problem can blow away every
//     problem-related cache without enumerating them.
//
// This pattern is the React Query "Query Key Factory" idiom — see
// https://tkdodo.eu/blog/effective-react-query-keys for full
// rationale. Keys are arrays; the first element is the namespace
// the rest are filters.

export const keys = {
  health:        ['health'] as const,

  services: {
    all:         ['services'] as const,
    list:        (range: { from: number; to: number }, opts?: { limit?: number; name?: string }) =>
                   ['services', 'list', range, opts ?? {}] as const,
    page:        (range: { from: number; to: number }, opts: { limit?: number; offset?: number; name?: string }) =>
                   ['services', 'page', range, opts] as const,
    names:       (q?: string, limit?: number, offset?: number) =>
                   ['services', 'names', q ?? '', limit ?? 200, offset ?? 0] as const,
    sparklines:  (range: { from: number; to: number }, names?: string[]) =>
                   ['services', 'sparklines', range, names ?? []] as const,
    structure:   (svc: string, since: string, samples: number) =>
                   ['services', 'structure', svc, since, samples] as const,
    neighbors:   (svc: string, since: string, samples: number) =>
                   ['services', 'neighbors', svc, since, samples] as const,
    infra:       (svc: string, since: string) =>
                   ['services', 'infra', svc, since] as const,
    runtime:     (svc: string) =>
                   ['services', 'runtime', svc] as const,
    map:         (since: string, samples: number, diff?: string, topN = 0) =>
                   ['services', 'map', since, samples, diff ?? '', topN] as const,
    backtrace:   (svc: string, opts: { since?: string; from?: number; to?: number; limit?: number }) =>
                   ['services', 'backtrace', svc, opts] as const,
    // group_id rel C — per-operation aggregate keyed on the normalized
    // flag so the raw and op_group-shape tables cache as two distinct
    // entries (they return different row sets for the same window).
    operations:  (svc: string, range: { from: number; to: number }, normalized: boolean) =>
                   ['services', 'operations', svc, range, normalized] as const,
    // Operator-curated catalog metadata (owner / SRE team / runbook
    // links) — one map for the whole install, joined locally by the
    // consumers (/services team filters, /admin/catalog editor).
    metadata:    ['services', 'metadata'] as const,
  },

  problems: {
    all:         ['problems'] as const,
    list:        (filter: { status?: string; service?: string; limit?: number }) =>
                   ['problems', 'list', filter] as const,
  },

  anomalies: {
    all:         ['anomalies'] as const,
    logPatterns: ['anomalies', 'log-patterns'] as const,
    traceOps:    ['anomalies', 'trace-ops'] as const,
    metrics:     ['anomalies', 'metrics'] as const,
    events:      ['anomalies', 'events'] as const,
    silences:    ['anomalies', 'silences'] as const,
  },

  exceptions: {
    all:         ['exceptions'] as const,
    groups:      (filter: { state?: string; service?: string; limit?: number }) =>
                   ['exceptions', 'groups', filter] as const,
    samples:     (fingerprint: string, limit: number) =>
                   ['exceptions', 'samples', fingerprint, limit] as const,
  },

  incidents: {
    all:         ['incidents'] as const,
    list:        (filter: { status?: string; service?: string; severity?: string; limit?: number }) =>
                   ['incidents', 'list', filter] as const,
    one:         (id: string) =>
                   ['incidents', 'one', id] as const,
    events:      (id: string) =>
                   ['incidents', 'events', id] as const,
    problems:    (id: string) =>
                   ['incidents', 'problems', id] as const,
  },

  admin: {
    systemStats: ['admin', 'system-stats'] as const,
    cardinality: ['admin', 'cardinality'] as const,
  },

  spans: {
    exemplar:    (svc: string, op: string, from: number, to: number, kind: string) =>
                   ['spans', 'exemplar', svc, op, from, to, kind] as const,
  },

  deploys: {
    forService:  (svc: string, from: number, to: number) =>
                   ['deploys', svc, from, to] as const,
  },

  // The "auth" key is special — invalidating it on logout drops every
  // cached query that depends on the user, which is most of them.
  auth: {
    me:          ['auth', 'me'] as const,
  },

  users: {
    all:         ['users'] as const,
    list:        ['users', 'list'] as const,
    // Custom-role catalog rides under the users namespace so the
    // Users page's single `invalidateQueries(keys.users.all)` after
    // a mutation refreshes both the list and the role picker.
    customRoles: ['users', 'custom-roles'] as const,
  },

  // Explore v2 multi-query builder (Phase 2). `sig` is the stable
  // querySignature(...) digest of every fetch-relevant builder input —
  // two letters with identical inputs share one cache entry.
  explore: {
    all:         ['explore'] as const,
    query:       (sig: string, from: number, to: number) =>
                   ['explore', 'query', sig, from, to] as const,
  },
} as const;
