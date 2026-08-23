import { encodeRange, encodeFilterGroup, encodeFilters } from '@/lib/urlState';
import type { TimeRange, FilterExpr } from '@/lib/types';

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


// operationTracesHref — v0.9.855 (UX denetimi K4). Pivot into /traces scoped
// to ONE operation (span name).
//
// Two bugs this replaces, both "the destination silently answers a DIFFERENT
// question than the one asked":
//
//  1. DEAD PARAM (K4). ⌘K's endpoint results linked to
//     `/traces?operation=<name>`. /traces has no `operation` reader, and its
//     State→URL effect rebuilds the whole query string from a whitelist — so
//     the param was not merely ignored, it was DELETED on the first state
//     write. The operator typed an endpoint name and got an unfiltered trace
//     list with a clean URL: "search is broken".
//  2. SUBSTRING SEARCH (v0.8.488, operator-reported). `search=` matches ANY
//     span in a trace, so an operation pivot dragged in unrelated traces that
//     merely touched a similarly-named span.
//
// The correct scope is an EXACT span-name filter, which is what the fixed
// sibling (OperationsTable) already emits. `rootOnly:false` because an
// operation is not necessarily the trace root — the /traces default would
// list nothing (v0.8.585 class).
export function operationTracesHref(p: {
  window: TracesPivot['window'];
  operation: string;
  service?: string;
  hasError?: boolean;
}): string {
  const filters: FilterExpr[] = [{ k: 'name', op: '=', v: [p.operation] }];
  return tracesPivotHref({
    window: p.window,
    service: p.service,
    hasError: p.hasError,
    filters: encodeFilters(filters),
    view: 'list',
    rootOnly: false,
  });
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

// dbTracesHref — v0.9.268. The /databases sibling of messagingTracesHref, and
// it exists for the same reason: the row's `instance` is not one attribute,
// it is whichever of SIX the MV managed to find first.
//
// db_summary_5m resolves instance as (internal/chstore/store.go:2494-2502):
//   peer_service → server.address → net.peer.name → db.host → db.name
//   → service_name → 'unknown'
//
// The old link filtered `peer.service` alone, so any row whose name came from
// a later rung landed on an empty trace list — which reads as "this database
// has no traces" rather than "the filter missed". Proven on live data: the
// clickhouse row carried 2201 spans in a 30-minute window and its link
// matched 0, because that instance is named from service_name (Coremetry's
// own self-telemetry calling ClickHouse), never from peer_service.
//
// service.name is a well-known filter key (filterexpr.go:30 → service_name),
// so all six rungs fit in one OR group.
export function dbTracesHref(p: {
  window: TracesPivot['window'];
  system: string;
  instance: string;
  /** MV's db_name dimension; 'default' is its own not-found sentinel. */
  dbName?: string;
  service?: string;
  hasError?: boolean;
}): string {
  const instanceFilters = [
    { k: 'peer.service', op: '=', v: [p.instance] },
    { k: 'server.address', op: '=', v: [p.instance] },
    { k: 'net.peer.name', op: '=', v: [p.instance] },
    { k: 'db.host', op: '=', v: [p.instance] },
    { k: 'db.name', op: '=', v: [p.instance] },
    { k: 'service.name', op: '=', v: [p.instance] },
  ];
  const root = {
    join: 'AND',
    filters: [
      { k: 'db.system', op: '=', v: [p.system] },
      // 'default' is the MV's fallback for a span with no db.name — pinning
      // it would filter on a literal no span carries.
      ...(p.dbName && p.dbName !== 'default'
        ? [{ k: 'db.name', op: '=', v: [p.dbName] }]
        : []),
    ],
    // 'unknown' is likewise the MV's own not-found label, not a real value.
    ...(p.instance && p.instance !== 'unknown'
      ? { groups: [{ join: 'OR', filters: instanceFilters }] }
      : {}),
  };
  // Same flat-AND trap as messagingTracesHref: encodeFilterGroup returns ''
  // for a group with no nested OR, which is exactly what this builds when the
  // instance is 'unknown'. Encoding into filterGroup alone would drop
  // db.system too and open an UNFILTERED trace list.
  const grouped = encodeFilterGroup(root as never);
  return tracesPivotHref({
    window: p.window,
    service: p.service,
    hasError: p.hasError,
    // A DB span is a CLIENT child — the rootOnly default would list nothing.
    rootOnly: false,
    view: 'list',
    ...(grouped
      ? { filterGroup: grouped }
      : { filters: encodeFilters(root.filters as never) }),
  });
}


// statementTracesHref — v0.7.67'de doğdu, v0.9.1324'te bu aileye TAŞINDI
// (§3.1 K2). "Bu ifadeyi çalıştıran trace'leri göster."
//
// Neden taşındı: eski hâli features/dependencies/panels/shared.tsx'te elle
// query string kuruyordu ve `range=` HİÇ yazmıyordu — yani bu dosyanın var
// oluş sebebinin (yukarıdaki dört-kez-gemiye-giden bug) birebir tekrarıydı.
// Daha da anlamlısı: aynı bileşen bir satır aşağıda HostLink'e pencereyi
// veriyordu (shared.tsx, v0.9.968), yani pencere ELDEYDİ, sadece bu linke
// ulaşmıyordu. Pencereyi zorunlu argüman yapmak o sınıfı imza düzeyinde
// kapatır.
//
// LIKE-öneki BİLİNÇLİ (v0.5.200 emsali, SlowQueries.tsx): Oracle V$SQL
// metni normalize edilmiş gelir, span'deki db.statement ise sürücünün
// bastığı ham metindir — tam eşleşme boş döner. İlk 60 karakter, en uzun
// güvenilir ortak önek. rootOnly=false çünkü db.statement'ı TAŞIYAN span
// çocuk CLIENT span'idir; /traces'in rootOnly varsayılanı hiçbir şey
// listelemezdi (v0.8.585 sınıfı).
//
// service isteğe bağlı: DetailDrawer'ın top-ops satırı DB tarafının
// toplamıdır ve tek bir servise ait değildir; orada kapsam yalnız
// ifadedir.
export const STATEMENT_LIKE_PREFIX_LEN = 60;

export function statementTracesHref(p: {
  window: TracesPivot['window'];
  statement: string;
  service?: string;
}): string {
  return tracesPivotHref({
    window: p.window,
    service: p.service,
    filters: encodeFilters([
      { k: 'db.statement', op: 'LIKE', v: [p.statement.slice(0, STATEMENT_LIKE_PREFIX_LEN)] },
    ]),
    view: 'list',
    rootOnly: false,
  });
}


// repeatsExploreHref — v0.9.1277 (Dynatrace-parite #6). Pivot into
// Explore's "Repeats" result mode: "hangi trace'lerde bu çağrı N+ kez
// tekrarlıyor".
//
// TUZAK — İFADE METNİ FİLTREYE GİRMEZ. Çağıran taraf (DB statement
// drawer) elinde NORMALİZE edilmiş SQL tutar (`SELECT * FROM t WHERE
// id = ?`); span'lerdeki `db.statement` ise sürücünün bastığı HAM
// metindir (bind placeholder'ları, whitespace, hint'ler). Tam-eşleşme
// bir filtre KESİNLİKLE boş döner — ve boş bir sonuç "bu desen yok"
// diye okunur, "yanlış anahtarla sordun" diye değil (v0.9.256 ile aynı
// yanılma sınıfı). Bu yüzden ifade FİLTREYE değil GRUPLAMAYA
// (`groupBy=db.statement`) girer: backend ham metni kendi gruplar,
// kapsamı ise servis + db.system daraltır.
//
// Zaman penceresi ZORUNLU argüman — bu dosyanın var oluş sebebi.
export function repeatsExploreHref(p: {
  window: TracesPivot['window'];
  /** Kapsam servisleri. Boş = servis filtresi yok (tüm servisler). */
  services?: string[];
  /** db.system daraltması (postgresql / oracle / …). */
  dbSystem?: string;
  /** Explore `groupBy=`; varsayılan db.statement (N+1 sorgu avcısı). */
  groupBy?: string[];
  /** Explore `minRepeats=`; varsayılan 5 (Explore'un kendi varsayılanı). */
  minRepeats?: number;
}): string {
  const filters: FilterExpr[] = [];
  const svcs = (p.services ?? []).filter(Boolean);
  // `=` backend'de TEK değer ister (filterexpr.go); çok servis IN olur.
  if (svcs.length === 1) filters.push({ k: 'service.name', op: '=', v: [svcs[0]] });
  else if (svcs.length > 1) filters.push({ k: 'service.name', op: 'IN', v: svcs });
  if (p.dbSystem) filters.push({ k: 'db.system', op: '=', v: [p.dbSystem] });

  const q = new URLSearchParams();
  q.set('result', 'repeats');
  if (filters.length) q.set('filters', encodeFilters(filters));
  q.set('groupBy', (p.groupBy ?? ['db.statement']).join(','));
  q.set('minRepeats', String(p.minRepeats ?? 5));
  q.set('range', rangeParam(p.window));
  return `/explore?${q.toString()}`;
}
