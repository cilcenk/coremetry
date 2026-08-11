import { windowRangeParam } from '@/lib/urlState';

// stmtParam — v0.8.378 (Stage-2 slice D2). Pure URL codec for the
// /slow-queries statement detail drawer: the `?stmt=` param encodes
// (hash, optional dbSystem) as `<hash>[|<enc(system)>]` — the
// endpointParam (v0.8.360) codec style applied to the D1 statement
// identity. URL is the source of truth for the open drawer (house rule
// §4): a copied link reproduces the exact drill-down.
//
// hash is the v0.8.375 stmt_hash as a DECIMAL STRING (a uint64 in a JS
// number loses precision past 2^53 — the SlowQueryRow.stmtHash contract),
// validated digits-only here so a garbage deep-link keeps the drawer
// closed instead of firing a 400-bound fetch. "0" is the backend's
// "no statement" sentinel — never a real class, rejected too.

export interface StmtRef {
  /** stmt_hash as a decimal string (SlowQueryRow.stmtHash). */
  hash: string;
  /** Optional db_system scope ('' = fold across engines, the catalog default). */
  system: string;
}

const HASH_RE = /^[0-9]{1,20}$/; // uint64 max is 20 digits

export function encodeStmtParam(ref: StmtRef): string {
  return ref.hash + (ref.system ? '|' + encodeURIComponent(ref.system) : '');
}

// stmtDetailHref — v0.9.963 (UX denetimi G1-b). The producer for "open this
// statement's detail drawer", from anywhere.
//
// The drawer is the only surface that answers the second question an operator
// asks about an expensive statement: WHO ELSE runs it, and is it worse than
// the previous window? It lives on /slow-queries and is URL-owned (`?stmt=`),
// so a link is all it takes — but until now the only door was a row click on
// the fleet catalog itself. From a service's own DB panel the operator could
// reach /traces and nothing else, i.e. they had to recognise their statement
// by eye in a fleet-wide list to get one page further.
//
// Returns null when the row has no identity (`stmtHash` is optional — a
// response served from a pre-D1 cache entry omits it, and "0" is the
// backend's no-statement sentinel). A null result must hide the affordance:
// a link to `/slow-queries?stmt=undefined` opens the catalog with the drawer
// silently shut, which reads as "the button is broken".
//
// The window is a REQUIRED argument, same contract as tracesPivotHref: the
// destination resolves its own range from the URL and falls back to the
// operator's sticky one, so a detail link without it re-asks the question
// over a different hour and the drawer's trend/compare answer the wrong one.
export function stmtDetailHref(
  ref: { hash?: string; system?: string },
  window: import('@/lib/types').TimeRange | { fromNs: number; toNs: number },
): string | null {
  const hash = (ref.hash ?? '').trim();
  if (!HASH_RE.test(hash) || /^0+$/.test(hash)) return null;
  const q = new URLSearchParams();
  q.set('stmt', encodeStmtParam({ hash, system: ref.system ?? '' }));
  const range = windowRangeParam(window);
  if (range) q.set('range', range);
  return `/slow-queries?${q.toString()}`;
}

// decodeStmtParam parses a raw `?stmt=` value. Returns null for anything
// malformed (non-digit hash, zero sentinel, bad escape, extra fields) —
// the drawer simply stays closed on a garbage deep-link.
export function decodeStmtParam(raw: string | null): StmtRef | null {
  if (!raw) return null;
  const parts = raw.split('|');
  if (parts.length > 2 || !parts[0]) return null;
  if (!HASH_RE.test(parts[0])) return null;
  if (/^0+$/.test(parts[0])) return null; // the "no statement" sentinel
  if (parts.length === 2 && !parts[1]) return null;
  try {
    return {
      hash: parts[0],
      system: parts.length === 2 ? decodeURIComponent(parts[1]) : '',
    };
  } catch {
    return null; // malformed %-escape
  }
}

// densifyTrend — the backend trend series is SPARSE (buckets with no
// data are absent); this expands it onto the window's dense bucket grid
// so the sparkline x-axis is time-linear and gaps render as zeros. The
// grid start snaps to the 5-minute MV grain exactly like the backend
// (dbStmtDetailWhere), and bucketSec comes from the payload
// (trendBucketSec) so the two sides can never disagree on coarsening.
export interface DenseTrend {
  calls: number[];
  errors: number[];
  avgMs: number[];
  p95Ms: number[];
}

export function densifyTrend(
  points: Array<{ tsNs: number; calls: number; errors: number; avgMs: number; p95Ms: number }>,
  fromNs: number,
  toNs: number,
  bucketSec: number,
): DenseTrend {
  const empty: DenseTrend = { calls: [], errors: [], avgMs: [], p95Ms: [] };
  if (bucketSec <= 0 || toNs <= fromNs) return empty;
  const startSec = Math.floor(fromNs / 1e9 / 300) * 300; // 5m-grain snap
  const endSec = Math.ceil(toNs / 1e9);
  let n = Math.ceil((endSec - startSec) / bucketSec);
  if (n < 1) n = 1;
  if (n > 400) n = 400; // mirror the backend LIMIT — never unbounded
  const out: DenseTrend = {
    calls: new Array(n).fill(0),
    errors: new Array(n).fill(0),
    avgMs: new Array(n).fill(0),
    p95Ms: new Array(n).fill(0),
  };
  for (const p of points) {
    const i = Math.round((p.tsNs / 1e9 - startSec) / bucketSec);
    if (i >= 0 && i < n) {
      out.calls[i] = p.calls;
      out.errors[i] = p.errors;
      out.avgMs[i] = p.avgMs;
      out.p95Ms[i] = p.p95Ms;
    }
  }
  return out;
}
