import { windowRangeParam } from '@/lib/urlState';
import type { TimeRange } from '@/lib/types';

// serviceHref — v0.9.860 (UX denetimi K1 / §4.1). The single builder for a
// `/service?name=…` link.
//
// The bug it exists to end: ~35 hand-written `/service?name=` strings, almost
// none of which carried the time window. The window is canonical in the URL,
// and a CUSTOM window lives ONLY there (v0.8.409 deliberately removed absolute
// windows from the sticky localStorage channel, because persisting a brushed
// window froze every later page load). So a link that drops `range` is:
//
//   • invisible on a relative preset — sticky refills it, nothing looks wrong;
//   • silent data loss on a custom window — the destination opens on "now".
//
// The operator experience that motivated it: at 03:14 you click the "Blast
// radius" service pill inside a problem, and the service page opens on the
// sticky NOW window. The incident is not in view. Best case you rebuild the
// window by hand from the timestamp you just read; worst case you conclude the
// problem has passed. The window was computed IN THE SAME COMPONENT for the
// logs and traces links — it just never reached the service link.
//
// Second effect: a copied URL carries no range/env, so it opens with the
// RECIPIENT's sticky settings. "The chart I sent you shows the error" stops
// being true — the two of you are looking at different windows.
//
// Precedent: podDetailPath (v0.9.152) was extracted for exactly this failure
// on /pod drills, and tracesPivotHref makes the window a REQUIRED argument
// after the same bug shipped four times. This completes that family for the
// last unbuilt producer.
//
// `range` is OPTIONAL here rather than required, unlike tracesPivotHref,
// because ~30 of the call sites are plain catalogue rows (Hosts, External,
// Clusters…) where the page's own range is the honest window and there is no
// event window to carry. Event-context callers — problem, anomaly, exception,
// inbox — MUST pass one; that is the contract §4.1 states and what
// serviceHref.test.ts pins.
export interface ServiceHrefOpts {
  /** The window to carry. TimeRange, or explicit unix-ns event bounds. */
  range?: TimeRange | { fromNs: number; toNs: number } | string | null;
  /** Env scope. Passing the sticky env keeps a SHARED link honest. */
  env?: string | null;
  /** Service-page tab (`operations`, `pods`, `infra`, `details`, `topology`). */
  tab?: string | null;
  /** Extra params a specific surface needs (e.g. `jpod`, `op`). */
  params?: Record<string, string | number | undefined | null>;
  /** Fragment WITHOUT the '#' (e.g. 'deploys'). */
  hash?: string | null;
}

/** Encode a window as the `range=` value /service understands. */
function rangeParam(r: NonNullable<ServiceHrefOpts['range']>): string {
  // Already-encoded strings ("6h", "custom:1-2") pass through — several call
  // sites hold the raw URL value rather than a TimeRange.
  if (typeof r === 'string') return r;
  // v0.9.963 — the ns→ms rounding rule and the decodeRange acceptance test
  // moved to lib/urlState (windowRangeParam) once a third link builder needed
  // them. serviceHref.test.ts still pins both behaviours through this call.
  return windowRangeParam(r);
}

export function serviceHref(name: string, opts: ServiceHrefOpts = {}): string {
  const q = new URLSearchParams();
  q.set('name', name);
  if (opts.tab) q.set('tab', opts.tab);
  for (const [k, v] of Object.entries(opts.params ?? {})) {
    if (v === undefined || v === null || v === '') continue;
    q.set(k, String(v));
  }
  if (opts.env) q.set('env', opts.env);
  if (opts.range) {
    const enc = rangeParam(opts.range);
    if (enc) q.set('range', enc);
  }
  return `/service?${q.toString()}` + (opts.hash ? `#${opts.hash}` : '');
}

// inboxItemWindow — v0.9.860. The event window for an inbox row / triage
// drawer, in the shape serviceHref takes.
//
// An inbox item spans startedAt → lastSeen. Both edges get a pad: the lead-in
// shows what the service looked like BEFORE the event (a chart that starts at
// onset has nothing to compare against), and the trail-out covers ingest lag
// plus "did it actually stop". Same shape as the anomaly drawer's spike
// window, which has carried a lead-in since v0.9.213.
//
// Degenerate rows (missing/zero timestamps) return undefined so the caller
// emits NO range and the destination self-resolves — better than pinning a
// window around epoch 0, which would render an empty page with a confident
// wrong timestamp.
const INBOX_LEAD_NS = 30 * 60 * 1e9;   // 30 min before onset
const INBOX_TRAIL_NS = 10 * 60 * 1e9;  // 10 min after last seen

export function inboxItemWindow(
  item: { startedAt?: number; lastSeen?: number } | undefined | null,
): { fromNs: number; toNs: number } | undefined {
  if (!item) return undefined;
  const start = item.startedAt;
  if (!start || start <= 0) return undefined;
  // A still-open item has lastSeen === startedAt (or older on a stale row);
  // never let the window invert or collapse to zero width.
  const last = item.lastSeen && item.lastSeen > start ? item.lastSeen : start;
  return { fromNs: start - INBOX_LEAD_NS, toNs: last + INBOX_TRAIL_NS };
}

// pointEventWindow — v0.9.966. A window around an instant.
//
// Several rows carry ONE timestamp, not a span: a deploy chip (timeUnixNs), an
// operator annotation (time), a trace-op / log-pattern anomaly (lastSeenNs).
// Each of them was about to grow its own pad, which is how two surfaces end up
// showing "the same moment" over different windows. Same lead-in/trail-out as
// an inbox item, so an operator who learned one learned all of them.
export function pointEventWindow(
  tsNs: number | undefined | null,
): { fromNs: number; toNs: number } | undefined {
  return inboxItemWindow(tsNs ? { startedAt: tsNs, lastSeen: tsNs } : undefined);
}

// eventLifespanWindow — v0.9.966. The window of a thing that STARTED and may
// or may not have ended: a problem, an incident.
//
// This is NOT inboxItemWindow. An inbox item's lastSeen is a real observation,
// so padding it is enough. An OPEN problem has no end at all — treating
// startedAt as the end (the inboxItemWindow shape) would pin a 40-minute
// window around an onset that may be four hours behind us, and the service
// page would open on a moment where nothing is wrong yet, for an incident that
// is still burning. So an unresolved item runs to NOW.
//
// The bounds are lifted verbatim from ProblemDetail (v0.9.860), which already
// used exactly these for its logs / traces / service pivots — a one-hour
// lead-in (you cannot see a regression without the "before") and a ten-minute
// trail-out (ingest lag plus "did it actually stop"). Extracted rather than
// re-derived so /problems, /anomalies and /incident cannot drift from the
// detail page they open into.
//
// `nowNs` is an argument, not a call, so the function stays pure and testable.
const LIFESPAN_LEAD_NS = 60 * 60 * 1e9;   // 1 hour before onset
const LIFESPAN_TRAIL_NS = 10 * 60 * 1e9;  // 10 min after the end

export function eventLifespanWindow(
  ev: { startedAt?: number; resolvedAt?: number } | undefined | null,
  nowNs: number = Date.now() * 1e6,
): { fromNs: number; toNs: number } | undefined {
  if (!ev) return undefined;
  const start = ev.startedAt;
  if (!start || start <= 0) return undefined;
  // A resolvedAt at or before onset is a bad row, not a zero-length event —
  // fall back to "still open" rather than emitting an inverted window.
  const end = ev.resolvedAt && ev.resolvedAt > start ? ev.resolvedAt : Math.max(nowNs, start);
  return { fromNs: start - LIFESPAN_LEAD_NS, toNs: end + LIFESPAN_TRAIL_NS };
}
