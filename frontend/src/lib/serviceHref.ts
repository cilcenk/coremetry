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
