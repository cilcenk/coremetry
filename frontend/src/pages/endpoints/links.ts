import { encodeRange, encodeFilters, buildQuery } from '@/lib/urlState';
import type { TimeRange } from '@/lib/types';

// links.ts — the /endpoints family's outbound pivots (v0.9.839).
//
// Both generators lived inside Endpoints.tsx while the drill-down was a
// drawer and a modal in the same file. The drill-down is now its own
// PAGE, and both surfaces still need the identical links: duplicating
// them is how two pivots off the same row drift into asking different
// questions. One module, one definition.
//
// The parameters take only the IDENTITY ({service, path}) — never a
// whole EndpointRow. The detail page must build these links from a deep
// link that has no row behind it, and a signature that demanded a row
// would force a fabricated one.

/**
 * tracesLink — /traces filtered to this endpoint.
 *
 * v0.9.307 — env/cluster ride the pivot. Without them a row read under
 * env=uat opened an UNFILTERED trace list: the pivot silently widened
 * the question it was launched from.
 *
 * v0.9.1372 — iki değişiklik, ikisi de operatör isteği:
 *
 * (a) `search=<path>` YERİNE yapısal `http.route = <path>` filtresi.
 *     `search` bir SERBEST METİN eşleşmesi: span adında VEYA
 *     özniteliklerde geçen her şeyi tutuyordu, yani `/api/v1/pay`
 *     araması `/api/v1/payment-retry`i de getiriyordu ve pivot,
 *     başlatıldığı satırdan farklı bir soru soruyordu. Doğru kodlama
 *     zaten bu dosyada, on satır aşağıda duruyordu (exploreLink'in
 *     `http.route` filtresi) — iki pivot aynı satırdan çıkıp farklı
 *     evrene gidiyordu.
 *
 * (b) `rootOnly=false` YERİNE `rootOnly=auto`. Operatör root seçili bir
 *     liste istiyor (endpoint'in trace'i = onu çağıran akışın tamamı),
 *     ama bu deployment'ta bir kısım endpoint root span DEĞİL — mesaj
 *     tüketicilerinin ve iç servislerin ortasında yaşıyorlar ve root
 *     filtresi onlarda her zaman sıfır döndürür. `auto` niyeti taşıyor,
 *     kararı /traces sonuca bakarak veriyor (`traces/rootOnlyFallback`).
 */
export function tracesLink(
  r: { service: string; path: string }, range: TimeRange, env?: string, cluster?: string,
): string {
  const filters = encodeFilters([{ k: 'http.route', op: '=', v: [r.path] }]);
  return `/traces?${buildQuery([
    ['service', r.service],
    ['filters', filters],
    ['range', encodeRange(range)],
    ['env', env ?? ''],
    ['cluster', cluster ?? ''],
    ['view', 'list'],
    ['rootOnly', 'auto'],
  ])}`;
}

/**
 * exploreLink — "Open in Explore →" (v0.9.307, brief N6b).
 *
 * Zero new queries: http.route is already a resolver tier dimension
 * (TIER_DIM_KEYS), so Explore answers this from the spanmetrics
 * rollups rather than raw spans. The URL is the SAME legacy
 * ?result=metric shape OperationsTable already emits — no new scheme
 * invented, and seedFromLegacyParams decodes it unchanged.
 */
export function exploreLink(
  r: { service: string; path: string }, range: TimeRange, agg: string,
  env?: string, cluster?: string,
): string {
  const filters = encodeFilters([
    { k: 'service.name', op: '=', v: [r.service] },
    { k: 'http.route', op: '=', v: [r.path] },
  ]);
  return `/explore?${buildQuery([
    ['range', encodeRange(range)],
    ['filters', filters],
    ['agg', agg],
    ['field', 'duration_ms'],
    ['result', 'metric'],
    ['env', env ?? ''],
    ['cluster', cluster ?? ''],
  ])}`;
}
