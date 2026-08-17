// metricSource.ts — v0.9.1151, VictoriaMetrics deneme modu.
//
// `?metricsrc=vm` (or `=ch`) in the PAGE URL routes that page's metric API
// calls at one backend for the duration, without touching the global
// Settings toggle. The operator needs it because metric NAMES differ
// between the two stores (VM sanitises `jvm.memory.used` to
// `jvm_memory_used`), so "do my dashboards survive VictoriaMetrics" can
// only be answered by looking at a real chart — and the global toggle is
// the wrong instrument for that question, since it moves every panel of
// every logged-in user at once.
//
// WHY THIS IS ONE FILE, not a param spread across call sites: the backend
// seam (internal/api/metricsource.go) exists for exactly the same reason.
// A page that forwards the param on its chart fetch but not on its picker
// fetch would autocomplete names from ClickHouse and query them against
// VictoriaMetrics — empty series, and the operator concludes VM has no
// data. So the param is attached inside lib/api.ts's metric methods, in
// ONE helper, and a contract test asserts every one of them uses it.
//
// NO UI WRITES THIS PARAM — deliberate, and the same posture as the
// old-engine escape hatch that carried the chart-engine migration (retired
// in v0.9.844; its flag name is banned from this tree on purpose, so it is
// described rather than named here). It is a probe an operator types by
// hand; nothing in the product hands it out, so no page can get stuck in it
// and no shared link carries it by accident.
// (Which also means the URL-as-source-of-truth rule is satisfied trivially:
// the URL is the only writer.)

export type MetricSourceParam = 'vm' | 'ch';

/** The query-string key. Mirrors internal/api's metricSourceParam. */
export const METRIC_SOURCE_PARAM = 'metricsrc';

/**
 * parseMetricSource — pure. Reads the override out of a location search
 * string (leading `?` optional).
 *
 * An unrecognised value returns undefined rather than throwing: the
 * BACKEND is the authority on what is valid and answers 400 with a message
 * naming the accepted set. Rejecting here too would mean two error
 * surfaces to keep in sync, and the client-side one would be the silent
 * kind — a typo'd param would be dropped and the page would quietly read
 * the default backend while the operator believed otherwise.
 *
 * The blank/whitespace case matches the server's TrimSpace, so a
 * hand-typed `?metricsrc=` never becomes a 400.
 */
export function parseMetricSource(search: string): MetricSourceParam | undefined {
  if (!search) return undefined;
  const raw = new URLSearchParams(search.startsWith('?') ? search.slice(1) : search)
    .get(METRIC_SOURCE_PARAM);
  const v = raw?.trim();
  return v === 'vm' || v === 'ch' ? v : undefined;
}

/**
 * currentMetricSource reads the override from the live page URL.
 *
 * Read at CALL time, not module load: BrowserRouter navigations mutate
 * location without re-evaluating modules, so a cached value would go stale
 * the moment the operator navigated away from the trial URL — and it would
 * go stale in the dangerous direction, keeping VM selected on a page whose
 * URL no longer says so.
 */
export function currentMetricSource(): MetricSourceParam | undefined {
  if (typeof window === 'undefined') return undefined;
  return parseMetricSource(window.location.search);
}

/**
 * withMetricSource stamps the override onto an already-built API URL.
 *
 * Takes a string rather than a params object because the metric methods in
 * api.ts build their query strings four different ways (qs(), manual
 * template literals, URLSearchParams). Wrapping the finished URL is the
 * one shape that fits all of them — and a shape every call site can be
 * checked for by a contract test.
 *
 * `src` is injectable so the function is pure under test; production
 * callers omit it.
 */
export function withMetricSource(url: string, src = currentMetricSource()): string {
  if (!src) return url;
  // Idempotent: an explicit param already on the URL wins. Nothing builds
  // one today, but a future caller that did would otherwise get two
  // conflicting values and Go's Query().Get() would silently take the
  // first.
  if (new URLSearchParams(url.includes('?') ? url.slice(url.indexOf('?') + 1) : '')
    .has(METRIC_SOURCE_PARAM)) return url;
  return `${url}${url.includes('?') ? '&' : '?'}${METRIC_SOURCE_PARAM}=${src}`;
}

/**
 * METRIC_SOURCE_LABELS — how each pinned backend is named on screen.
 *
 * The /metrics source badge alone reads IDENTICALLY whether VM is the
 * install-wide default or a one-request probe, and an operator who cannot
 * tell those apart may conclude they already migrated. So the trial badge
 * names the backend it pinned rather than just saying "trial" — the
 * `?metricsrc=ch` direction (escaping to ClickHouse while VM is the
 * default) has no source badge at all, and "deneme modu" on its own would
 * leave it ambiguous.
 */
export const METRIC_SOURCE_LABELS: Record<MetricSourceParam, string> = {
  vm: 'VictoriaMetrics',
  ch: 'ClickHouse',
};
