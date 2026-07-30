// heatmapFilters (v0.8.421) — the /api/spans/heatmap filter payload for
// the Service page's latency-distribution panel, in the FilterExpr WIRE
// shape the backend actually unmarshals: `{k, op, v: string[]}`
// (internal/chstore/filterexpr.go json tags).
//
// Review-confirmed critical: since the v0.8.252 split this panel sent
// `{key, op, value}` — json.Unmarshal filled zero-value FilterExprs
// (Key="", Values=nil), the compiler skipped them, and EVERY filter was
// a silent no-op: the heatmap showed the whole cluster's spans no
// matter which service (or cluster pivot, or v0.8.415 operation scope)
// was selected. The shape lives in one pure helper now, pinned by a
// table-driven vitest so it cannot drift from the wire contract again.
import type { FilterExpr } from '@/lib/types';

export function heatmapFilters(
  service: string, cluster?: string, operation?: string, rootOnly?: boolean,
): FilterExpr[] {
  const f: FilterExpr[] = [{ k: 'service.name', op: '=', v: [service] }];
  // v0.9.364 — Details sekmesindeki panel v0.9.348'den beri rootOnly RED
  // grafiklerinin ALTINDA duruyor ama dağılımı TÜM span türlerinden
  // çiziyordu: üstteki grafikler giriş noktalarını (server+consumer)
  // gösterirken heatmap client/internal span'leri de karıştırıyordu —
  // api-gateway'de giden çağrıların gecikmesi servisin kendi dağılımıymış
  // gibi okunuyordu. `kind` filterexpr.go wellKnown kolonu, değerler
  // CH'deki küçük-harf literal'ler (endpoints.go/slo.go ile aynı).
  if (rootOnly) f.push({ k: 'kind', op: 'IN', v: ['server', 'consumer'] });
  // Hit the resource-attr key directly. The OTLP ingest path materialises
  // k8s.cluster.name as a span attr, so a single predicate is enough (no
  // coalesce across resource + span attrs needed at query time).
  if (cluster) f.push({ k: 'k8s.cluster.name', op: '=', v: [cluster] });
  // `name` maps to the span-name column in the filter compiler
  // (filterexpr.go) — same predicate the Traces page uses.
  if (operation) f.push({ k: 'name', op: '=', v: [operation] });
  return f;
}
