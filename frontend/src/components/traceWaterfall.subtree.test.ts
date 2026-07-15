// traceWaterfall.subtree.test.ts — v0.8.537.
//
// Alt+click subtree toggling rests on two pure helpers, kept out of the
// .tsx so importing them here pulls no React in.
//
//   collectSubtreeIds — every REAL span id at/under a root. Iterative:
//     the render DFS recurses, and a deep trace would blow the stack.
//   groupParentOf     — pulls the real parent id back out of a synthetic
//     "group:<parent>:<i>:<key>" row id, which is how Alt+expand clears
//     group rows inside a subtree without a second index.
import { describe, it, expect } from 'vitest';
import { collectSubtreeIds, groupParentOf } from './traceWaterfall.tree';

type Kid = { spanId: string; startTime: number };

// Minimal stand-in for the component's Map<string, SpanRow[]> — only
// spanId is read by collectSubtreeIds.
function mkChildren(edges: Record<string, string[]>): Map<string, Kid[]> {
  const m = new Map<string, Kid[]>();
  for (const [parent, kids] of Object.entries(edges)) {
    m.set(parent, kids.map((k, i) => ({ spanId: k, startTime: i })));
  }
  return m;
}

describe('collectSubtreeIds', () => {
  it('walks a straight chain and includes the root', () => {
    const children = mkChildren({ a: ['b'], b: ['c'], c: ['d'], d: [] });
    expect(new Set(collectSubtreeIds(children as never, 'a')))
      .toEqual(new Set(['a', 'b', 'c', 'd']));
  });

  it('collects every branch, not just the first', () => {
    const children = mkChildren({
      root: ['l', 'r'], l: ['l1', 'l2'], r: ['r1'], l1: [], l2: [], r1: [],
    });
    expect(new Set(collectSubtreeIds(children as never, 'root')))
      .toEqual(new Set(['root', 'l', 'r', 'l1', 'l2', 'r1']));
  });

  it('starts from an inner node, excluding its ancestors and their other branches', () => {
    const children = mkChildren({
      root: ['l', 'r'], l: ['l1'], r: ['r1'], l1: [], r1: [],
    });
    expect(new Set(collectSubtreeIds(children as never, 'l')))
      .toEqual(new Set(['l', 'l1']));
  });

  it('returns just the leaf itself', () => {
    const children = mkChildren({ a: ['leaf'], leaf: [] });
    expect(collectSubtreeIds(children as never, 'leaf')).toEqual(['leaf']);
  });

  it('tolerates an id the map has never seen', () => {
    const children = mkChildren({ a: [] });
    expect(collectSubtreeIds(children as never, 'ghost')).toEqual(['ghost']);
  });

  // The reason the implementation is a stack and not recursion. A
  // recursive walk overflows here; this test is what keeps it that way.
  it('survives a 5000-deep chain without blowing the stack', () => {
    const edges: Record<string, string[]> = {};
    for (let i = 0; i < 5000; i++) edges[`s${i}`] = [`s${i + 1}`];
    edges.s5000 = [];
    expect(collectSubtreeIds(mkChildren(edges) as never, 's0')).toHaveLength(5001);
  });
});

describe('groupParentOf', () => {
  it('extracts the real parent id from a synthetic group row id', () => {
    // Synthetic ids are built as `group:${parentId}:${i}:${key}` where key
    // is serviceName + '\x01' + displayName.
    expect(groupParentOf('group:abc123:0:cart-svc\x01GET /items')).toBe('abc123');
  });

  it('returns null for a real span id', () => {
    expect(groupParentOf('4bf92f3577b34da6')).toBeNull();
  });

  it('is not fooled by a real id that merely contains the word group', () => {
    expect(groupParentOf('grouped')).toBeNull();
  });

  it('survives a malformed synthetic id with no second separator', () => {
    expect(groupParentOf('group:abc123')).toBeNull();
  });

  it('keeps the parent intact when the group key itself contains colons', () => {
    // displayName routinely carries colons (e.g. "GET /a:b"), so the cut
    // must be the FIRST separator, not the last.
    expect(groupParentOf('group:abc123:2:svc\x01GET /a:b:c')).toBe('abc123');
  });
});
