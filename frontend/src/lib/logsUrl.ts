import { encodeRange } from './urlState';
import type { TimeRange } from './types';

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

// ── logsHref — v0.9.1347 ──────────────────────────────────────────────────
//
// The single builder for a `/logs?…` deep link. Lives HERE, next to
// readLogsParams, because readLogsParams is the consumer contract: a producer
// in another file drifts from it, and "drifted from the reader" is the whole
// history of this page's links.
//
// THE WINDOW IS REQUIRED, unlike traceHref's. /logs QUERIES by window —
// it takes it from `?range=` and nowhere else (readLogsParams above has no
// from/to). A link that drops it lands on the sticky window, and an old
// trace's logs come back empty: "no logs" for logs that exist. That is
// operator-reported and it has shipped repeatedly — v0.8.484 (SpanDetail sent
// `&from=&to=`, params /logs does not read), v0.9.853 (Trace detail's main
// "≡ Logs" button had no window at all), v0.9.862 (the log-pattern anomaly
// link carried `q` only). Making it a required argument is the only fix that
// does not depend on the next author remembering.
//
// `null` is an explicit DECLINE, not a default: a surface with no window
// (a saved log search on /alerts belongs to a rule, not to a moment) says so
// in one greppable token. You cannot forget the window; you can only refuse
// it on the record.
//
// ── SERVICE SCOPE GOES IN `service`, NOT `q` (v0.9.1381) ─────────────────
//
// `service` is the page's service FILTER (an exact column match).
// `q` is the free-text search box.
//
// This block used to say both were correct for scoping to a service, and it
// justified that with v0.8.521. The justification was real but WRONGLY
// GENERALISED, and three call sites copied the generalisation.
//
// What v0.8.521 actually established: an ID-SHAPED `q` is matched against the
// column AS WELL as the body, so a trace pivot through `q` finds both the
// installations that log the id as a field and the ones that only have it in
// the body. That is still true and still load-bearing — the ClickHouse side
// implements it in the `isBareHexID` branch (internal/logstore/clickhouse.go),
// which promotes a bare 32/16-hex needle onto trace_id/span_id.
//
// What it did NOT establish: that `q` gets the same treatment for a SERVICE
// NAME. There is no such branch. On ClickHouse — the DEFAULT log backend
// (internal/config/config.go, main.go: `case "", "clickhouse"`) — the list
// path is `body LIKE '%<q>%'` (internal/chstore/repo.go) and the histogram
// path is `multiSearchAnyCaseInsensitive(body, [<q>])`. Both search the BODY.
//
// Measured on a live local CH (24h window, 41.315 rows), one service:
//     service_name = 'x'                 →  858 rows
//     body LIKE '%x%'                    →  366 rows
//     body LIKE '%service.name:"x"%'     →    0 rows
// and `countIf(body LIKE '%service.name%') = 0` across the whole table: the
// string cannot match, so the pivot returned HTTP 200 with an empty list and
// the operator read it as "no logs".
//
// Note the middle row too: even the spelling that DOES match is not the same
// question. 492 of those rows never mention their own service name in the
// body, so free-text service scoping silently under-reports by ~57%.
//
// So: scope to a service with `service`. Keep `q` for what the operator
// actually typed, and for the id-shaped pivots v0.8.521 was about.
export interface LogsPivot {
  /**
   * REQUIRED. The window /logs will query. Absolute ns bounds for an event
   * (a spike, a trace's span extent), a TimeRange or an already-encoded
   * `range=` string for the page's own window, or `null` to decline on the
   * record when the surface genuinely has no moment attached.
   */
  window: TimeRange | { fromNs: number; toNs: number } | string | null;
  /** Exact service-column filter. */
  service?: string;
  cluster?: string;
  /** Free-text search box (`q=`). See the service/q note above. */
  q?: string;
  /** OTel severity-number FLOOR; 0/undefined = all levels (13 = warn). */
  severity?: number;
  /** Exact trace-id column filter. See the service/q note above. */
  traceId?: string;
  spanId?: string;
  hasTrace?: boolean;
  /** Pre-encoded FilterExpr[] JSON. */
  filters?: string;
  /** Pre-encoded column list. */
  cols?: string;
  /** Env scope — keeps a SHARED link honest. */
  env?: string | null;
  /**
   * Symmetric ns padding for an ABSOLUTE window: ingest lag and clock skew
   * routinely put a log a minute either side of the span that caused it.
   * Ignored for a preset or a pre-encoded string, where it has no meaning.
   */
  padNs?: number;
}

function pivotRangeParam(w: LogsPivot['window'], padNs: number): string {
  if (!w) return '';
  // Already-encoded values pass through. A bare 'custom' is the one token
  // that would survive decodeRange while carrying no bounds — timeRangeToNs
  // then resolves it to a silent 24h (utils.ts:17-25), so drop it instead.
  if (typeof w === 'string') return w === 'custom' ? '' : w;
  if ('preset' in w) {
    if (w.preset === 'custom' && !(w.fromMs && w.toMs)) return '';
    return encodeRange(w);
  }
  // The ns branch goes through logsRangeParam, which owns the floor/ceil rule
  // and the decodeRange acceptance test. Note Math.round is NOT equivalent:
  // rounding the low edge UP or the high edge DOWN narrows the window below
  // what the caller asked for, which is the v0.9.963 rule.
  return logsRangeParam(w.fromNs, w.toNs, padNs);
}

export function logsHref(p: LogsPivot): string {
  const q = new URLSearchParams();
  // Key names and their empty-value handling mirror writeLogsParams above —
  // `q` (never the legacy `search`), `hasTrace=1`, severity written only when
  // it is a real floor.
  if (p.service) q.set('service', p.service);
  if (p.cluster) q.set('cluster', p.cluster);
  if (p.q) q.set('q', p.q);
  if (p.severity && p.severity > 0) q.set('severity', String(p.severity));
  if (p.traceId) q.set('traceId', p.traceId);
  if (p.spanId) q.set('spanId', p.spanId);
  if (p.hasTrace) q.set('hasTrace', '1');
  if (p.filters) q.set('filters', p.filters);
  if (p.cols) q.set('cols', p.cols);
  if (p.env) q.set('env', p.env);
  const range = pivotRangeParam(p.window, p.padNs ?? 0);
  if (range) q.set('range', range);
  return `/logs?${q.toString()}`;
}

// serviceLogQuery — v0.9.1386'da SİLİNDİ.
//
// `service.name:"<servis>"` üretiyordu ve bu yazım ClickHouse'da —
// VARSAYILAN arka uçta — hiçbir şeye eşleşmiyordu (ölçüm yukarıda).
// Son çağıranı v0.9.1386'da `service=`e geçince sıfır çağıranla kaldı.
//
// Bırakılmadı, çünkü kalsaydı tek işlevi bir sonraki yazarı aynı tuzağa
// çağırmak olurdu: adı "servise kapsamla" diyor, davranışı varsayılan
// kurulumda boş liste. Servis kapsamı için `service=` HER İKİ arka uçta
// da çalışıyor, yani bu yazımın meşru bir geleceği yok.
