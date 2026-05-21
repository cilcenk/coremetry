// Method Hotspot aggregation — Dynatrace-style table view that
// collapses the flame tree by function name, summing self + total
// across every path the function appears on. The flame tree
// answers "where in the call graph is time spent"; the hotspot
// table answers "which functions are heavy, regardless of who
// called them" — the second is what an operator scans first
// when diagnosing a CPU regression.
//
// Self time is the sample count where the function was the
// leaf; total (inclusive) time is the sample count where the
// function appeared anywhere on the stack. Functions that
// recurse only contribute their total once per stack to avoid
// double-counting under recursive frames.

import type { FlameNode } from './types';

export interface MethodHotspot {
  // Display key — function name. Files differ for overloaded
  // names so we surface the most-common file:line as a hint
  // but the dedup key is the bare name (matches Dynatrace's
  // Method Hotspots which collapses overloads).
  name: string;
  self: number;
  total: number;
  // Sample of the location info (first file:line seen for the
  // name) — informational, not part of the dedup key.
  file?: string;
  line?: number;
  // How many distinct stack paths the name appeared on. High
  // count = called from many places (probably utility code);
  // low count + high self = candidate optimisation target.
  paths: number;
}

// flameToHotspots walks the tree once, accumulating per-name
// stats. Recursion-safe: per stack we add a name's self+total
// only if not already counted on the ancestors (visited Set
// passed by reference, copied at each fork).
export function flameToHotspots(root: FlameNode): MethodHotspot[] {
  const acc = new Map<string, MethodHotspot>();
  walkHotspots(root, new Set<string>(), acc);

  const list = [...acc.values()];
  // Drop the synthetic "root" node — it's always present and
  // always equals the total profile value.
  return list.filter(h => h.name !== 'root');
}

function walkHotspots(
  n: FlameNode,
  ancestorsOnThisStack: Set<string>,
  acc: Map<string, MethodHotspot>,
): void {
  let entry = acc.get(n.name);
  if (!entry) {
    entry = { name: n.name, self: 0, total: 0, file: n.file, line: n.line, paths: 0 };
    acc.set(n.name, entry);
  }
  // Self always accumulates (it's leaf-bound, no double count
  // risk).
  if (n.self) entry.self += n.self;

  // Total: only credit this name once per stack — if a
  // recursive call sees the same name above it, skip.
  const seen = ancestorsOnThisStack.has(n.name);
  if (!seen) {
    entry.total += n.value;
    entry.paths += 1;
  }

  if (!n.children || n.children.length === 0) return;
  const next = seen ? ancestorsOnThisStack : new Set(ancestorsOnThisStack).add(n.name);
  for (const c of n.children) walkHotspots(c, next, acc);
}

export type HotspotSort = 'self' | 'total' | 'paths';

export function sortHotspots(list: MethodHotspot[], by: HotspotSort): MethodHotspot[] {
  const sorted = [...list];
  sorted.sort((a, b) => {
    if (by === 'self') return b.self - a.self;
    if (by === 'total') return b.total - a.total;
    return b.paths - a.paths;
  });
  return sorted;
}
