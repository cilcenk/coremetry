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

// logsRangeParam — v0.9.853 (UX denetimi K3). The ONE producer of the time
// window token /logs actually reads.
//
// The bug it kills: Trace detail's main "≡ Logs" button shipped
// `?from=<ns>&to=<ns>`. readLogsParams above has no from/to — /logs takes its
// window from `?range=` ONLY. So the params rode along as dead weight and the
// page fell back to the sticky range: on any trace older than the sticky
// window the operator got "no logs" for a trace whose logs exist. The last
// step of the alert→service→endpoint→trace→logs journey, silently broken.
//
// Unit discipline (v0.6.36 class): span times and traceLogWindow are Unix
// NANOSECONDS; `custom:` is MILLISECONDS. The conversion lives here once
// instead of being re-derived at each call site — SpanDetail had the only
// correct copy (v0.8.484) and Trace.tsx had none.
//
// `padNs` widens the window symmetrically (ingest lag / clock skew). Floor the
// low edge and ceil the high edge so rounding never NARROWS the window.
// Returns '' when the window is unusable, so callers can drop the param
// rather than emit a `custom:` token decodeRange would reject anyway.
export function logsRangeParam(
  fromNs?: number | null, toNs?: number | null, padNs = 0,
): string {
  if (!fromNs || !toNs || !Number.isFinite(fromNs) || !Number.isFinite(toNs)) return '';
  const fromMs = Math.floor((fromNs - padNs) / 1e6);
  const toMs = Math.ceil((toNs + padNs) / 1e6);
  // Mirror decodeRange's acceptance test (urlState.ts): anything it would
  // reject must not be emitted.
  if (!(fromMs > 0) || !(toMs > fromMs)) return '';
  return `custom:${fromMs}-${toMs}`;
}

// ── Tek-doküman kalıcı linki (v0.9.1248, Kibana artığı) ──────────
//
// Biçim: ?doc=<tsNs>.<id>[&docsvc=<service>]. Çözüm YENİ UÇ İSTEMEZ:
// mevcut /api/logs/context (ts pivotu, n=5) penceresinde id aranır —
// tek sınırlı sorgu, ES disiplini korunur. id her iki backend'de de
// içerik-türevi ve OTURUMLAR ARASI kararlı (ES: _id FNV'si; CH:
// cityHash64) — 2^53 üstü değerler JSON'da yuvarlanır ama iki taraf da
// AYNI yuvarlanmış sayıyı gördüğünden eşitlik tutarlıdır; tam sayılar
// üstel gösterime düşmez (~1.8e19 < 1e21), '.' ayracı güvenli.

export function buildDocPermalink(
  l: { timestamp: number; id: number; serviceName?: string }, env: string,
): string {
  let qs = `doc=${l.timestamp}.${l.id}`;
  if (l.serviceName) qs += `&docsvc=${encodeURIComponent(l.serviceName)}`;
  if (env) qs += `&env=${encodeURIComponent(env)}`;
  return `/logs?${qs}`;
}

// parseDocParam — '?doc=' değeri → {ts, id} | null. SAF; bozuk/eksik/
// sayı-dışı her girdi null (link çözümü sessizce çöp üretmez).
export function parseDocParam(raw: string | null | undefined): { ts: number; id: number } | null {
  if (!raw) return null;
  const dot = raw.indexOf('.');
  if (dot <= 0 || dot === raw.length - 1) return null;
  const ts = Number(raw.slice(0, dot));
  const id = Number(raw.slice(dot + 1));
  if (!Number.isFinite(ts) || !Number.isFinite(id) || ts <= 0) return null;
  return { ts, id };
}
