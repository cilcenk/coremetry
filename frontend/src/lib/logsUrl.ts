// logsUrl — pure filter ⇄ URL mapping for /logs (v0.8.546).
//
// Extracted so the round-trip is testable without a DOM. The page keeps a
// sig-guard: writeUrl pre-stores urlSig(state) and the URL→state import
// no-ops when the incoming params hash to the same sig. That guard only
// holds if all three of {sig, write, read} agree on the SAME field set —
// when they drift, the page either loops or silently clobbers state
// (the v0.8.253/256/265 bug class). Keeping them in one file, derived from
// one shape, is what makes that agreement checkable.
//
// The bug that forced this out (operator-facing): `severity` was a live
// filter that never round-tripped. Pressing the ERROR chip and hitting
// Share handed the recipient a link that opened on All levels — a silent
// wrong link, not a broken one.

export interface LogsUrlFilter {
  service: string;
  cluster: string;
  search: string;
  severity: number; // OTel severity-number floor; 0 = all levels
  traceId: string;
  spanId: string;
  hasTrace: boolean;
}

// The identity of a URL-bearing view. Every field the URL carries must be
// here, or the sig-guard stops noticing that field's changes.
export function logsUrlSig(f: LogsUrlFilter, filtersRaw: string, colsRaw: string): string {
  return JSON.stringify([
    f.service, f.cluster, f.search, f.severity, f.traceId, f.spanId, f.hasTrace,
    filtersRaw, colsRaw,
  ]);
}

// State → params. Empty/zero fields are DELETED rather than written blank,
// so a default view produces a clean /logs URL.
export function writeLogsParams(
  prev: URLSearchParams, f: LogsUrlFilter, filtersRaw: string, colsRaw: string,
): URLSearchParams {
  const p = new URLSearchParams(prev);
  const setOrDel = (k: string, v: string) => { if (v) p.set(k, v); else p.delete(k); };
  setOrDel('service', f.service);
  setOrDel('cluster', f.cluster);
  setOrDel('q', f.search);
  p.delete('search'); // legacy alias of q — never write both
  setOrDel('severity', f.severity > 0 ? String(f.severity) : '');
  setOrDel('traceId', f.traceId);
  setOrDel('spanId', f.spanId);
  setOrDel('hasTrace', f.hasTrace ? '1' : ''); // v0.8.406
  setOrDel('filters', filtersRaw);
  setOrDel('cols', colsRaw);
  return p;
}

// Params → state. Garbage severity resolves to 0 (all levels) rather than
// NaN, which would poison both the query and the sig.
export function readLogsParams(p: URLSearchParams): LogsUrlFilter {
  const sev = Number(p.get('severity'));
  return {
    service:  p.get('service') ?? '',
    cluster:  p.get('cluster') ?? '',
    search:   p.get('q') ?? p.get('search') ?? '',
    severity: Number.isFinite(sev) && sev > 0 ? sev : 0,
    traceId:  p.get('traceId') ?? '',
    spanId:   p.get('spanId')  ?? '',
    hasTrace: p.get('hasTrace') === '1', // v0.8.406
  };
}
