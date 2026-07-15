// traceWaterfall.tree.ts — v0.8.537.
//
// Pure tree helpers behind TraceWaterfall's Alt+click subtree toggle.
// They live outside the .tsx on purpose: exporting non-components from a
// component file trips react-refresh/only-export-components, and keeping
// them here lets vitest import them without pulling React in.
import type { SpanRow } from '@/lib/types';

// Every REAL span id at or under rootId, walking the parent→children map.
// Iterative by design: the render DFS recurses, and a deep trace
// (thousands of nested spans) would blow the stack here for nothing.
// An id the map has never seen yields just itself.
export function collectSubtreeIds(children: Map<string, SpanRow[]>, rootId: string): string[] {
  const out: string[] = [];
  const stack = [rootId];
  while (stack.length) {
    const id = stack.pop()!;
    out.push(id);
    for (const k of children.get(id) ?? []) stack.push(k.spanId);
  }
  return out;
}

// "group:<parentSpanId>:<i>:<key>" → "<parentSpanId>"; null for a real
// span id. Synthetic group rows encode their real parent, which is what
// lets Alt+expand clear the group rows inside a subtree without a second
// index. The cut is the FIRST separator after the prefix: the trailing
// key carries a display name that routinely contains ':'. Safe because
// OTel span ids are 16 hex chars and never contain ':'.
export function groupParentOf(id: string): string | null {
  if (!id.startsWith('group:')) return null;
  const rest = id.slice('group:'.length);
  const cut = rest.indexOf(':');
  return cut < 0 ? null : rest.slice(0, cut);
}
