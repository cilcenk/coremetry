import { encodeRange, encodeFilters, buildQuery } from '@/lib/urlState';
import type { TimeRange } from '@/lib/types';

// links.ts — the /endpoints family's outbound pivots (v0.9.839).
//
// Both generators lived inside Endpoints.tsx while the drill-down was a
// drawer and a modal in the same file. The drill-down is now its own
// PAGE, and both surfaces still need the identical links: duplicating
// them is how two pivots off the same row drift into asking different
// questions. One module, one definition.
//
// The parameters take only the IDENTITY ({service, path}) — never a
// whole EndpointRow. The detail page must build these links from a deep
// link that has no row behind it, and a signature that demanded a row
// would force a fabricated one.

/**
 * tracesLink — /traces filtered to this endpoint.
 *
 * search matches span.name OR attrs; with rootOnly=false and the
 * service filter, this returns every trace that includes a call on the
 * endpoint.
 *
 * v0.9.307 — env/cluster ride the pivot. Without them a row read under
 * env=uat opened an UNFILTERED trace list: the pivot silently widened
 * the question it was launched from.
 */
export function tracesLink(
  r: { service: string; path: string }, range: TimeRange, env?: string, cluster?: string,
): string {
  return `/traces?service=${encodeURIComponent(r.service)}` +
    `&search=${encodeURIComponent(r.path)}` +
    `&range=${encodeURIComponent(encodeRange(range))}` +
    (env ? `&env=${encodeURIComponent(env)}` : '') +
    (cluster ? `&cluster=${encodeURIComponent(cluster)}` : '') +
    `&view=list&rootOnly=false`;
}

/**
 * exploreLink — "Open in Explore →" (v0.9.307, brief N6b).
 *
 * Zero new queries: http.route is already a resolver tier dimension
 * (TIER_DIM_KEYS), so Explore answers this from the spanmetrics
 * rollups rather than raw spans. The URL is the SAME legacy
 * ?result=metric shape OperationsTable already emits — no new scheme
 * invented, and seedFromLegacyParams decodes it unchanged.
 */
export function exploreLink(
  r: { service: string; path: string }, range: TimeRange, agg: string,
  env?: string, cluster?: string,
): string {
  const filters = encodeFilters([
    { k: 'service.name', op: '=', v: [r.service] },
    { k: 'http.route', op: '=', v: [r.path] },
  ]);
  return `/explore?${buildQuery([
    ['range', encodeRange(range)],
    ['filters', filters],
    ['agg', agg],
    ['field', 'duration_ms'],
    ['result', 'metric'],
    ['env', env ?? ''],
    ['cluster', cluster ?? ''],
  ])}`;
}
