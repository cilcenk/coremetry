import { encodeRange } from '@/lib/urlState';
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
  hasError?: boolean;
  /**
   * /traces defaults rootOnly to TRUE, but most error spans and most
   * caller→callee hops are mid-trace children — a pivot that leaves the
   * default on lists nothing (v0.8.585). Defaults to false here so the
   * safe choice is the one you get by not thinking about it.
   */
  rootOnly?: boolean;
  view?: 'list' | 'aggregated';
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
  if (p.filters) q.set('filters', p.filters);
  if (p.hasError) q.set('hasError', 'true');
  q.set('rootOnly', p.rootOnly ? 'true' : 'false');
  if (p.view) q.set('view', p.view);
  q.set('range', rangeParam(p.window));
  return `/traces?${q.toString()}`;
}
