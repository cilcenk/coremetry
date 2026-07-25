import { encodeRange, encodeFilterGroup, encodeFilters } from '@/lib/urlState';
import type { TimeRange } from '@/lib/types';

// pivotHref — cross-signal deep links that CANNOT drop the time window.
//
// Every pivot into /traces is an answer to "show me the spans behind what
// I'm looking at". /traces resolves its own window from the URL and falls
// back to the operator's sticky range (useUrlRange), so a pivot that omits
// the range silently re-asks the question over a different window. The
// failure is invisible in the worst way: the destination renders an empty
// list, which reads as "there are no such traces" rather than "you're
// looking at the wrong hour".
//
// That bug shipped four separate times (Explore pivot v0.9.208, anomaly
// drawer / slow-queries / backtrace v0.9.213), so the window is a REQUIRED
// argument here rather than an option a caller can forget.
export type TracesPivot = {
  /** Absolute window. Either a TimeRange or explicit unix-ns bounds. */
  window: TimeRange | { fromNs: number; toNs: number };
  service?: string;
  /** Multi-service co-occurrence (caller,callee) — /traces `services=`. */
  services?: string[];
  /** Free-text search (endpoint path, operation name…). */
  search?: string;
  /** Pre-encoded FilterExpr[] JSON, as produced by encodeFilters(). */
  filters?: string;
  /**
   * Pre-encoded FilterGroup JSON (encodeFilterGroup) for pivots that need
   * OR / nesting — e.g. "this attribute under any of its three historical
   * names". /traces treats `filters` and `filterGroup` as mutually
   * exclusive, so setting this suppresses `filters` rather than emitting
   * both and letting the page pick.
   */
  filterGroup?: string;
  hasError?: boolean;
  /**
   * /traces defaults rootOnly to TRUE, but most error spans and most
   * caller→callee hops are mid-trace children — a pivot that leaves the
   * default on lists nothing (v0.8.585). Defaults to false here so the
   * safe choice is the one you get by not thinking about it.
   */
  rootOnly?: boolean;
  // v0.9.256 — was 'list' | 'aggregated'. /traces reads
  // 'list' | 'aggregate' | 'shapes' | 'relations'; 'aggregated' is not a
  // value it knows, so every caller asking for it silently landed on the
  // list view. Union corrected so the mistake is a type error.
  view?: 'list' | 'aggregate' | 'shapes' | 'relations';
};

/** Encode a window as the `range=` value /traces understands. */
function rangeParam(w: TracesPivot['window']): string {
  if ('preset' in w) return encodeRange(w);
  // Unix ns → ms. Floor/ceil so the window never narrows below what the
  // caller asked for (a truncated `to` can drop the newest bucket).
  const fromMs = Math.floor(w.fromNs / 1e6);
  const toMs = Math.ceil(w.toNs / 1e6);
  return encodeRange({ preset: 'custom', fromMs, toMs });
}

export function tracesPivotHref(p: TracesPivot): string {
  const q = new URLSearchParams();
  if (p.services?.length) q.set('services', p.services.join(','));
  else if (p.service) q.set('service', p.service);
  if (p.search) q.set('search', p.search);
  // Mutually exclusive by /traces' contract — never emit both.
  if (p.filterGroup) q.set('filterGroup', p.filterGroup);
  else if (p.filters) q.set('filters', p.filters);
  if (p.hasError) q.set('hasError', 'true');
  q.set('rootOnly', p.rootOnly ? 'true' : 'false');
  if (p.view) q.set('view', p.view);
  q.set('range', rangeParam(p.window));
  return `/traces?${q.toString()}`;
}


// messagingTracesHref — pivot from a queue/topic row into /traces.
//
// v0.9.256, operator-reported: "messaging kısmında tracelere erişemiyorum."
// The old link was DEAD for two independent reasons, both verified against
// live ClickHouse:
//
//  1. WRONG ATTRIBUTE. The messaging MV derives `destination` through a
//     three-step coalesce (messaging.destination.name → messaging.destination
//     → peer_service). The link filtered on `.name` alone. In the last hour
//     of live data that attribute had ZERO rows while the older
//     `messaging.destination` had 1280 — all 17 topics reported
//     has_name_attr = 0. So the link could only ever return nothing.
//  2. NO WINDOW. It dropped `range=`, so the destination fell back to its
//     own 30m default — the exact class pivotHref exists to prevent.
//
// Hence the OR group: match the destination under ANY of the names the MV
// itself accepts. Filtering on one name while the MV coalesces three is how
// a link ends up pointing at rows that cannot exist.
export function messagingTracesHref(p: {
  window: TracesPivot['window'];
  system: string;
  destination: string;
  /** 'producer' | 'consumer' — omit for both sides of the topic. */
  role?: 'producer' | 'consumer';
  service?: string;
  /** Span name, for the drawer's per-operation rows. */
  operation?: string;
  hasError?: boolean;
}): string {
  const destFilters = [
    { k: 'messaging.destination.name', op: '=', v: [p.destination] },
    { k: 'messaging.destination', op: '=', v: [p.destination] },
    { k: 'peer.service', op: '=', v: [p.destination] },
  ];
  const root = {
    join: 'AND',
    filters: [
      { k: 'messaging.system', op: '=', v: [p.system] },
      ...(p.role ? [{ k: 'kind', op: '=', v: [p.role] }] : []),
      ...(p.operation ? [{ k: 'name', op: '=', v: [p.operation] }] : []),
    ],
    // Only worth an OR group when the destination is real. 'unknown' is the
    // MV's own fallback for a row it could not name, and pinning it would
    // filter on a literal string no span carries.
    ...(p.destination && p.destination !== 'unknown'
      ? { groups: [{ join: 'OR', filters: destFilters }] }
      : {}),
  };
  // encodeFilterGroup returns '' for a FLAT-AND group by design (urlState:
  // back-compat — a group with no nested OR is carried by the legacy
  // `filters=` param instead). That is exactly the shape this builds when
  // the destination is 'unknown', so encoding into filterGroup alone would
  // silently drop `messaging.system` too and send the operator to an
  // UNFILTERED trace list. Fall back to the flat param in that case; a
  // regression test pins both branches.
  const grouped = encodeFilterGroup(root as never);
  return tracesPivotHref({
    window: p.window,
    service: p.service,
    hasError: p.hasError,
    // A messaging span is a CHILD span — the default rootOnly would list
    // nothing.
    rootOnly: false,
    ...(grouped
      ? { filterGroup: grouped }
      : { filters: encodeFilters(root.filters as never) }),
  });
}
