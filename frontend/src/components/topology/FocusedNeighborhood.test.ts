import { describe, expect, it } from 'vitest';
import { assignFocusColumns } from './FocusedNeighborhood';
import type { GraphEdge } from '@/lib/types';

// v0.8.39 — Operator-reported: the focused topology graph "won't branch" at
// 2 hops — every node piled into ONE vertical column instead of fanning out
// (focus centre, callers left, deps right). Root cause: the old single
// bidirectional BFS summed the ±1 steps along the path, so a node reached as
// caller(-1) → that caller's OTHER dependency(+1) landed at column 0,
// dumping every sibling of the focus into the focus's own column (against
// real data ~26 nodes piled at 0). assignFocusColumns walks the two
// directions SEPARATELY (upstream IN-only → -hop, downstream OUT-only →
// +hop), so callers fan strictly left, deps strictly right, and only the
// focus is at column 0. These cases would fail on the pre-fix path-sum code.

const e = (source: string, target: string): GraphEdge => ({
  source, target, calls: 1, errors: 0, errorRate: 0, rate: 0, avgMs: 1, p99Ms: 1,
});

describe('assignFocusColumns', () => {
  it('keeps a caller-sibling OFF the focus column at 2 hops (the bug)', () => {
    const edges = [
      e('caller', 'focus'),     // caller is upstream of focus
      e('caller', 'sibling'),   // sibling shares the caller — NOT on a focus up/down path
      e('focus', 'dep'),        // dep is downstream of focus
      e('dep', 'deepdep'),      // 2-hop downstream
    ];
    const col = assignFocusColumns(edges, 'focus', 2);

    expect(col.get('focus')).toBe(0);
    expect(col.get('caller')).toBe(-1);   // upstream → left
    expect(col.get('dep')).toBe(1);       // downstream → right
    expect(col.get('deepdep')).toBe(2);   // 2-hop downstream
    // the sibling is reachable only via caller(-1)→sibling, i.e. col 0 under
    // the old path-sum — it must NOT be pinned to the focus column.
    expect(col.get('sibling')).not.toBe(0);
    // in fact a pure up/down walk never reaches it → excluded entirely.
    expect(col.has('sibling')).toBe(false);
    // INVARIANT: nothing but the focus sits at column 0.
    expect([...col.values()].filter(v => v === 0)).toEqual([0]);
  });

  it('fans callers left (negative) and deps right (positive) by hop depth', () => {
    const edges = [e('gp', 'caller'), e('caller', 'focus'), e('focus', 'dep'), e('dep', 'gd')];
    const col = assignFocusColumns(edges, 'focus', 2);
    expect(col.get('caller')).toBe(-1);
    expect(col.get('gp')).toBe(-2);
    expect(col.get('dep')).toBe(1);
    expect(col.get('gd')).toBe(2);
  });

  it('bounds the walk by hops', () => {
    const col = assignFocusColumns([e('focus', 'a'), e('a', 'b'), e('b', 'c')], 'focus', 1);
    expect(col.get('a')).toBe(1);
    expect(col.has('b')).toBe(false); // beyond 1 hop
  });

  it('a cycle takes the closer side by absolute hop distance', () => {
    // focus → a → focus (a is both a 1-hop dep and a 1-hop caller); |+1| == |-1|,
    // downstream is assigned first so a settles on +1 — never 0.
    const col = assignFocusColumns([e('focus', 'a'), e('a', 'focus')], 'focus', 2);
    expect(col.get('focus')).toBe(0);
    expect(Math.abs(col.get('a')!)).toBe(1);
    expect(col.get('a')).not.toBe(0);
  });
});
