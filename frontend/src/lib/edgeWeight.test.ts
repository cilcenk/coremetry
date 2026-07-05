import { describe, it, expect } from 'vitest';
import { edgeWeights } from './edgeWeight';
import type { ServiceMapEdge } from './types';

// v0.8.281 — focus-view edges all arrive with traceCount=0 (MV path has no
// per-trace attribution), which made every edge render at minimum thickness.
// Contract: trace-weighted when any traceCount exists (sampled global path,
// byte-identical to pre-281), span-weighted when none do (MV path), max
// floored at 1 for safe division.
const E = (traceCount: number, spanCount: number): ServiceMapEdge =>
  ({ caller: 'a', callee: 'b', traceCount, spanCount, errorCount: 0 });

describe('edgeWeights', () => {
  it('sampled graph (traceCounts present) weighs by traceCount', () => {
    const { weightOf, max } = edgeWeights([E(5, 100), E(2, 50)]);
    expect(weightOf(E(5, 100))).toBe(5);
    expect(max).toBe(5);
  });

  it('MV graph (all traceCounts zero) falls back to spanCount', () => {
    const { weightOf, max } = edgeWeights([E(0, 100), E(0, 50)]);
    expect(weightOf(E(0, 100))).toBe(100);
    expect(weightOf(E(0, 50))).toBe(50);
    expect(max).toBe(100);
  });

  it('mixed graph stays trace-weighted (old behaviour preserved)', () => {
    const { weightOf } = edgeWeights([E(3, 100), E(0, 999)]);
    expect(weightOf(E(0, 999))).toBe(0);
  });

  it('empty input: max floors at 1, undefined edge weighs 0', () => {
    const { weightOf, max } = edgeWeights([]);
    expect(max).toBe(1);
    expect(weightOf(undefined)).toBe(0);
  });
});
