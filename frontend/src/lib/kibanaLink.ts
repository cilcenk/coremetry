// Build a Kibana Discover deep-link URL with the current Logs
// filter pre-applied. OpenShift's "Discover in Kibana" affordance
// uses the same pattern — the _g + _a state params carry time
// range + query + data view. We Rison-encode each state object
// (Kibana's wire format) so the URL doesn't break on special
// characters in service names / IDs.
//
// Reference: Kibana docs/source — `rison-node` is what Kibana
// uses; here we ship a tiny subset of the format sufficient for
// our shape (objects + strings + booleans). No need to vendor
// the full library.

import type { KibanaSettings } from './types';

export type KibanaQueryContext = {
  // ISO timestamps or "now-Xh" style relative; Kibana accepts
  // both. We emit ISO when given numeric unix-ns bounds.
  fromNs?: number;
  toNs?: number;
  // KQL string. Empty / undefined → no query filter.
  kql?: string;
};

export function buildKibanaURL(
  cfg: KibanaSettings | null | undefined,
  ctx: KibanaQueryContext,
): string | null {
  if (!cfg || !cfg.enabled || !cfg.baseUrl) return null;

  const fromIso = ctx.fromNs ? nsToIso(ctx.fromNs) : 'now-15m';
  const toIso   = ctx.toNs   ? nsToIso(ctx.toNs)   : 'now';

  const g = {
    time: { from: fromIso, to: toIso },
  };
  const a: Record<string, unknown> = {
    query: { language: 'kuery', query: ctx.kql ?? '' },
  };
  if (cfg.dataView) {
    a.index = cfg.dataView;
  }

  const base = cfg.baseUrl.replace(/\/+$/, '');
  return `${base}/app/discover#/?_g=${rison(g)}&_a=${rison(a)}`;
}

function nsToIso(ns: number): string {
  // unix-ns → ISO 8601 (millisecond precision is what Kibana
  // parses cleanly).
  return new Date(ns / 1_000_000).toISOString();
}

// rison — Kibana's compact URL state format. We only need the
// subset that handles plain objects, strings, numbers, booleans,
// nulls, and arrays. Strings are quoted with single quotes and
// inner single quotes are doubled; everything else folds to a
// reasonable encoding via JSON-style printing.
function rison(v: unknown): string {
  if (v === null) return '!n';
  if (v === undefined) return '!u';
  if (typeof v === 'boolean') return v ? '!t' : '!f';
  if (typeof v === 'number') {
    if (!Number.isFinite(v)) return '!n';
    return String(v);
  }
  if (typeof v === 'string') return risonString(v);
  if (Array.isArray(v)) return `!(${v.map(rison).join(',')})`;
  if (typeof v === 'object') {
    const entries = Object.entries(v as Record<string, unknown>)
      .filter(([, val]) => val !== undefined);
    const pairs = entries.map(([k, val]) => `${risonKey(k)}:${rison(val)}`);
    return `(${pairs.join(',')})`;
  }
  return '!n';
}

function risonString(s: string): string {
  // No special chars? bare identifier; else quoted with `'` and
  // doubled inner `'` escapes.
  if (/^[A-Za-z_][A-Za-z0-9_-]*$/.test(s) && s.length > 0) return s;
  return "'" + s.replace(/'/g, "''") + "'";
}

function risonKey(k: string): string {
  // Same logic as risonString — keys can be bare identifiers
  // when they're alphanumeric, else quoted.
  return risonString(k);
}
