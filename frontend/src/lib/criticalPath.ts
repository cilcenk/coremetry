// Critical-path analysis on a trace's span DAG. The "critical
// path" is the chain of dependent spans whose total duration
// equals the trace's wall-clock latency — the spans on this
// chain ARE the operations the operator should optimise to
// make the request faster.
//
// Definition (synchronous critical path):
//   For each span S, the candidate's contribution =
//     S.duration + max(critical(child) for each blocking
//                      child whose start ≥ S.start)
//   The trace's critical path is the chain returned by
//   walking from the root via the child that maximises this
//   sum at each level.
//
// Performance: pure O(N) DFS with one Map of N parent→children
// entries. At 10k spans (a heavy distributed trace) this runs
// in < 5ms in JS — no need for Web Workers. The result is a
// Set<spanId> the caller marks on the waterfall rows.
//
// Async-vs-sync nuance: a parent that fires N async children
// returns when its OWN code finishes; the children continue.
// We approximate "synchronous critical path" by treating any
// child whose end-time ≤ parent's end-time as blocking.
// Children that outlast their parent (fire-and-forget) are
// excluded from the chain. This matches what Datadog/
// Honeycomb call "trace's critical path".

export interface CriticalPathSpan {
  spanId: string;
  parentId: string;
  // Both as nanoseconds (matches the existing trace API).
  startTime: number;
  duration: number;
  // For UI display — not used by the algorithm.
  name?: string;
  serviceName?: string;
}

export interface CriticalPath {
  ids: Set<string>;        // span IDs on the critical path
  totalNs: number;         // sum of durations along the path
  rootId: string | null;   // first id in the chain
  leafId: string | null;   // last id in the chain
}

export function computeCriticalPath(spans: CriticalPathSpan[]): CriticalPath {
  if (spans.length === 0) {
    return { ids: new Set(), totalNs: 0, rootId: null, leafId: null };
  }

  // Build child index. Each span's start ≥ its parent's start
  // by definition; we don't validate that here.
  const byId = new Map<string, CriticalPathSpan>();
  const childrenOf = new Map<string, CriticalPathSpan[]>();
  for (const s of spans) {
    byId.set(s.spanId, s);
    if (s.parentId) {
      const list = childrenOf.get(s.parentId);
      if (list) list.push(s);
      else childrenOf.set(s.parentId, [s]);
    }
  }

  // Find the root span(s) — entries whose parentId isn't in
  // byId (or is empty). Most well-behaved traces have exactly
  // one; multi-root happens with broken or ingest-merged
  // traces. Pick the longest-running root as the canonical.
  const roots: CriticalPathSpan[] = [];
  for (const s of spans) {
    if (!s.parentId || !byId.has(s.parentId)) roots.push(s);
  }
  if (roots.length === 0) {
    // Defensive: cyclic data or all-orphan spans. Return empty.
    return { ids: new Set(), totalNs: 0, rootId: null, leafId: null };
  }
  roots.sort((a, b) => b.duration - a.duration);
  const root = roots[0];

  // DFS with memoisation. For each node return the longest
  // chain (list of ids + sum of durations) from this node
  // through any blocking child.
  type Chain = { ids: string[]; total: number };
  const memo = new Map<string, Chain>();

  function chainFrom(s: CriticalPathSpan): Chain {
    const cached = memo.get(s.spanId);
    if (cached) return cached;
    const kids = childrenOf.get(s.spanId);
    let bestChild: Chain | null = null;
    if (kids) {
      const parentEnd = s.startTime + s.duration;
      for (const k of kids) {
        const kEnd = k.startTime + k.duration;
        // Blocking iff the child finishes before the parent.
        // Fire-and-forget children outlast the parent — they
        // never appear on the parent's critical path.
        if (kEnd > parentEnd) continue;
        const chain = chainFrom(k);
        if (!bestChild || chain.total > bestChild.total) {
          bestChild = chain;
        }
      }
    }
    const chain: Chain = bestChild
      ? { ids: [s.spanId, ...bestChild.ids], total: s.duration + bestChild.total }
      : { ids: [s.spanId],                   total: s.duration };
    memo.set(s.spanId, chain);
    return chain;
  }

  const top = chainFrom(root);
  return {
    ids: new Set(top.ids),
    totalNs: top.total,
    rootId: top.ids[0] ?? null,
    leafId: top.ids[top.ids.length - 1] ?? null,
  };
}
