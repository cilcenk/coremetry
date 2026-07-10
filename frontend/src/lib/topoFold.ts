import type { ServiceMap, ServiceMapEdge, ServiceMapNode } from './types';

// topoFold — pure core of the collapsible namespace group boxes on the
// flow graph (v0.8.447, operatör-seçimi Varyant C). A collapsed
// namespace's member services fold into ONE synthetic super-node; the
// edges that touched members re-target it and parallel edges bundle
// into one (counts summed, p99 conservative-max). The transform runs
// BEFORE layout, so the existing BFS/barycenter placement, zoom/pan
// and hover code see a plain (smaller) graph — zero layout risk, per
// the v0.8.279 "evolve in place" rule. Kept free of React so the
// merge semantics are table-tested in topoFold.test.ts.

// Synthetic node id prefix. Safe: real service names never carry it,
// and the existing kind-prefixes (db:/queue:/ext:) are on kind'd
// nodes which are never folded.
export const NS_NODE_PREFIX = 'nsgroup:';

export function nsNodeId(ns: string): string { return NS_NODE_PREFIX + ns; }
export function isNsNode(service: string): boolean { return service.startsWith(NS_NODE_PREFIX); }
export function nsOfNodeId(service: string): string { return service.slice(NS_NODE_PREFIX.length); }

export interface FoldedGroup {
  ns: string;
  members: string[];   // folded-away service names, sorted
  spanCount: number;
  errorRate: number;   // 0..1, span-weighted across members
}

export interface FoldResult {
  data: ServiceMap;
  groups: FoldedGroup[];
  // merged-edge key "caller|callee" → how many raw edges bundled in.
  // Only keys with >1 land here; the renderer annotates the tooltip.
  bundled: Map<string, number>;
}

// foldTopology — replace each collapsed namespace's members with one
// super-node. Rules mirroring the hull rules (v0.8.437): kind'd dep
// nodes (db/queue/ext) are never grouped; a namespace folds only when
// ≥2 of its members are present in the CURRENT graph (folding a single
// node just renames it — noise).
export function foldTopology(
  data: ServiceMap,
  nsOf: (service: string) => string | undefined,
  collapsed: ReadonlySet<string>,
): FoldResult {
  if (collapsed.size === 0) return { data, groups: [], bundled: new Map() };

  // service → collapsed ns (only for foldable members).
  const memberNs = new Map<string, string>();
  const byNs = new Map<string, ServiceMapNode[]>();
  for (const n of data.nodes) {
    if (n.kind) continue;
    const ns = nsOf(n.service);
    if (!ns || !collapsed.has(ns)) continue;
    memberNs.set(n.service, ns);
    (byNs.get(ns) ?? byNs.set(ns, []).get(ns)!).push(n);
  }
  // Drop <2-member folds.
  for (const [ns, members] of byNs) {
    if (members.length >= 2) continue;
    byNs.delete(ns);
    for (const m of members) memberNs.delete(m.service);
  }
  if (byNs.size === 0) return { data, groups: [], bundled: new Map() };

  const groups: FoldedGroup[] = [...byNs.entries()]
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([ns, members]) => {
      const spanCount = members.reduce((s, m) => s + m.spanCount, 0);
      const errs = members.reduce((s, m) => s + m.spanCount * m.errorRate, 0);
      return {
        ns,
        members: members.map(m => m.service).sort(),
        spanCount,
        errorRate: spanCount > 0 ? errs / spanCount : 0,
      };
    });

  const nodes: ServiceMapNode[] = data.nodes.filter(n => !memberNs.has(n.service));
  for (const g of groups) {
    nodes.push({ service: nsNodeId(g.ns), spanCount: g.spanCount, errorRate: g.errorRate });
  }

  // Re-target + bundle edges. Self-loops (both ends inside the same
  // folded group) drop — intra-namespace traffic is what the operator
  // collapsed away.
  const endpoint = (s: string) => {
    const ns = memberNs.get(s);
    return ns ? nsNodeId(ns) : s;
  };
  const merged = new Map<string, ServiceMapEdge>();
  const bundled = new Map<string, number>();
  for (const e of data.edges) {
    const caller = endpoint(e.caller), callee = endpoint(e.callee);
    if (caller === callee) continue;
    const key = `${caller}|${callee}`;
    const ex = merged.get(key);
    if (!ex) {
      merged.set(key, { ...e, caller, callee });
      continue;
    }
    bundled.set(key, (bundled.get(key) ?? 1) + 1);
    ex.traceCount += e.traceCount;
    ex.spanCount += e.spanCount;
    ex.errorCount += e.errorCount;
    ex.isNew = ex.isNew || e.isNew;
    // RED enrichment (MV path only): rates add; p99 merges as max —
    // the same conservative roll-up the edges MV readers use. avgMs
    // would need a weighted mean to stay honest; a bundle drops it.
    if (ex.rate != null || e.rate != null) ex.rate = (ex.rate ?? 0) + (e.rate ?? 0);
    if (ex.p99Ms != null || e.p99Ms != null) ex.p99Ms = Math.max(ex.p99Ms ?? 0, e.p99Ms ?? 0);
    ex.avgMs = undefined;
    ex.errorRate = ex.spanCount > 0 ? ex.errorCount / ex.spanCount : 0;
  }

  return {
    data: { ...data, nodes, edges: [...merged.values()] },
    groups,
    bundled,
  };
}

// defaultCollapsed — the auto posture when the URL carries no ?nsfold=.
// Small maps stay fully expanded; once the graph is genuinely crowded,
// the big namespaces (the operator's "çok fazla deployment" case) start
// folded and the header/super-node click writes the explicit state.
export const AUTO_FOLD_MIN_GRAPH = 30; // service nodes (kind'd deps excluded)
export const AUTO_FOLD_MIN_MEMBERS = 8;

export function defaultCollapsed(
  data: ServiceMap,
  nsOf: (service: string) => string | undefined,
): Set<string> {
  const counts = new Map<string, number>();
  let serviceNodes = 0;
  for (const n of data.nodes) {
    if (n.kind) continue;
    serviceNodes++;
    const ns = nsOf(n.service);
    if (ns) counts.set(ns, (counts.get(ns) ?? 0) + 1);
  }
  if (serviceNodes <= AUTO_FOLD_MIN_GRAPH) return new Set();
  return new Set([...counts.entries()]
    .filter(([, c]) => c >= AUTO_FOLD_MIN_MEMBERS)
    .map(([ns]) => ns));
}

// ?nsfold= codec. Absent/empty → null (auto default applies);
// literal '-' → explicitly nothing collapsed; else comma list.
// Members are percent-encoded (v0.8.447 review-fix): namespace burada
// çoğunlukla k8s adı ama deriver serbest-metin `service.namespace`
// resource attr'ını AYNEN geçirir — virgül taşıyan bir isim listeyi
// bozar, '-' adlı bir isim boş-küme sentineliyle çakışırdı.
// encodeURIComponent [a-z0-9-] üzerinde kimliktir, eski URL'ler
// geriye dönük aynen çözülür.
export function parseNsFold(raw: string | null): Set<string> | null {
  if (raw == null || raw === '') return null;
  if (raw === '-') return new Set();
  return new Set(raw.split(',').filter(Boolean).map(decodeURIComponent));
}

export function encodeNsFold(collapsed: ReadonlySet<string>): string {
  if (collapsed.size === 0) return '-';
  return [...collapsed].sort()
    // encodeURIComponent '-' karakterine dokunmaz (unreserved) — tek
    // üyeli {'-'} kümesi sentinelle çakışmasın diye elle kaçır.
    .map(ns => (ns === '-' ? '%2D' : encodeURIComponent(ns)))
    .join(',');
}
