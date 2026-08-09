// endpointParam — v0.8.360 (Stage-2 slice E2). Pure helpers for the
// /endpoints detail drawer:
//
//   • encode/decodeEndpointParam — the `?endpoint=` URL codec. URL is
//     the source of truth for the open drawer (house rule §4): the
//     param encodes (service, path, sig) as
//     `<enc(service)>|<enc(path)>[|sig]` — each field is
//     URI-component-encoded FIRST so a literal `|` inside a path can
//     never forge a field boundary. `sig` marks the path as an
//     ID-collapsed signature (/orders/:id — the table's "group by
//     shape" mode), baked into the param so a copied link reproduces
//     the same drill-down regardless of the viewer's toggle state.
//   • trimHistogram — strips the all-zero leading/trailing bins off
//     the backend's fixed 28-bin log-scale distribution so the chart
//     zooms on the populated latency range.

export interface EndpointRef {
  service: string;
  path: string;
  sig: boolean;
}

// ── /endpoint full-page detail (v0.9.839) ────────────────────────────
//
// The drawer and the sparkline modal are retired: a row click now
// NAVIGATES. The page's own URL is human-readable on purpose —
// `?service=&path=&sig=1` rather than the packed `?endpoint=` codec —
// because a link pasted into an incident channel should be legible
// there. The codec above stays: it is still what the OLD deep links
// carry, and legacyEndpointTarget is the one place that decodes them.

/** ENDPOINT_PAGE — the detail route the row click and every legacy
 *  deep link resolve to. */
export const ENDPOINT_PAGE = '/endpoint';

/** Scope carried from the list page onto the detail page. Every field
 *  is optional: absent means "the page's own default", never a
 *  fabricated value. */
export interface EndpointPageScope {
  range?: string;
  env?: string;
  cluster?: string;
  compare?: boolean;
  /** 'rpc' = the row came from the RPC & Messaging tab, where the path
   *  column carries the span name and http_route is '' by definition.
   *  The detail page needs it to look the row back up in the same
   *  population the table listed. */
  entry?: 'rpc';
}

/**
 * endpointDetailHref — build the /endpoint URL for one row.
 *
 * The scope travels with the pivot (the v0.9.307 rule): a row read
 * under env=uat must open a page that asks the same question. Empty
 * fields are OMITTED rather than written as `env=` — an explicitly
 * empty env means "all environments" elsewhere in the app, and
 * emitting one here would widen the scope while looking like it
 * narrowed it.
 */
export function endpointDetailHref(
  ref: EndpointRef, scope: EndpointPageScope = {},
): string {
  const p = new URLSearchParams();
  p.set('service', ref.service);
  p.set('path', ref.path);
  if (ref.sig) p.set('sig', '1');
  if (scope.range) p.set('range', scope.range);
  if (scope.env) p.set('env', scope.env);
  if (scope.cluster) p.set('cluster', scope.cluster);
  if (scope.compare) p.set('compare', '1');
  if (scope.entry) p.set('entry', scope.entry);
  return `${ENDPOINT_PAGE}?${p.toString()}`;
}

/**
 * legacyEndpointTarget — the ONE redirect for pre-v0.9.839 deep links.
 *
 * `?endpoint=<codec>` opened the drawer, `?detail=<codec>` opened the
 * sparkline modal. Both are gone, but saved_views rows and links
 * pasted in postmortems still carry them, so /endpoints resolves them
 * to the new page on mount. This is a redirect, not a compat shim: the
 * old surfaces do not exist behind it, and nothing else in the app
 * writes these params any more.
 *
 * Returns null when the search string carries neither param or the
 * codec is malformed — a garbage deep link lands on the plain table,
 * exactly as the drawer used to simply stay closed.
 */
export function legacyEndpointTarget(search: string): string | null {
  const p = new URLSearchParams(search);
  const raw = p.get('endpoint') ?? p.get('detail');
  const ref = decodeEndpointParam(raw);
  if (!ref) return null;
  return endpointDetailHref(ref, {
    range: p.get('range') ?? undefined,
    env: p.get('env') ?? undefined,
    cluster: p.get('cluster') ?? undefined,
    compare: p.get('compare') === '1',
  });
}

/**
 * endpointSearchHint — the substring to narrow the detail page's list
 * read with, so finding ONE row doesn't mean pulling a whole service's
 * top-N.
 *
 * `search` on /api/endpoints filters the RAW path dimension
 * (positionCaseInsensitive). In raw mode the path IS a raw value, so it
 * narrows exactly. In SIGNATURE mode it is not: `/orders/:id` is a
 * collapsed shape and appears in no raw path, so passing it whole would
 * filter every row away and the page would claim the endpoint has no
 * data. The literal prefix before the first collapsed segment is a
 * substring of EVERY raw path the shape covers, which narrows honestly
 * — never at the cost of a false negative.
 *
 * Returns '' when there is no usable prefix (a shape that starts with a
 * placeholder); the caller then omits the filter rather than sending a
 * hint that excludes the row it is looking for.
 */
export function endpointSearchHint(path: string, sig: boolean): string {
  if (!sig) return path;
  const i = path.indexOf('/:');
  if (i < 0) return path; // shape with no placeholder = a literal route
  return path.slice(0, i + 1); // keep the trailing '/', drop ':id' on
}

/**
 * parseEndpointPageRef — read the detail page's OWN params back into a
 * ref. Returns null when either identity field is missing, so the page
 * renders an honest "no endpoint selected" state instead of querying
 * for the empty string.
 */
export function parseEndpointPageRef(search: string): EndpointRef | null {
  const p = new URLSearchParams(search);
  const service = p.get('service') ?? '';
  const path = p.get('path') ?? '';
  if (!service || !path) return null;
  return { service, path, sig: p.get('sig') === '1' };
}

export function encodeEndpointParam(ref: EndpointRef): string {
  return (
    encodeURIComponent(ref.service) +
    '|' +
    encodeURIComponent(ref.path) +
    (ref.sig ? '|sig' : '')
  );
}

// decodeEndpointParam parses a raw `?endpoint=` value. Returns null for
// anything malformed (missing field, bad escape) — the drawer simply
// stays closed on a garbage deep-link instead of crashing.
export function decodeEndpointParam(raw: string | null): EndpointRef | null {
  if (!raw) return null;
  const parts = raw.split('|');
  if (parts.length < 2 || parts.length > 3 || !parts[0] || !parts[1]) return null;
  if (parts.length === 3 && parts[2] !== 'sig') return null;
  try {
    return {
      service: decodeURIComponent(parts[0]),
      path: decodeURIComponent(parts[1]),
      sig: parts[2] === 'sig',
    };
  } catch {
    return null; // malformed %-escape
  }
}

// trimHistogram — returns the [bins, counts] slice between the first
// and last non-zero count (inclusive), so the fixed 0.1ms→1Ms grid
// renders only its populated range. All-zero input → empty arrays
// (caller shows the section's empty state).
export function trimHistogram(
  bins: number[],
  counts: number[],
): { bins: number[]; counts: number[] } {
  const n = Math.min(bins.length, counts.length);
  let first = -1;
  let last = -1;
  for (let i = 0; i < n; i++) {
    if (counts[i] > 0) {
      if (first === -1) first = i;
      last = i;
    }
  }
  if (first === -1) return { bins: [], counts: [] };
  return { bins: bins.slice(first, last + 1), counts: counts.slice(first, last + 1) };
}
