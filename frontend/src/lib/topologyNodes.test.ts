import { describe, expect, it } from 'vitest';
import { infraNodeLabel, infraNodeSystem, mapNumber, nodeSizeMetric } from './topologyNodes';

// v0.7.31 — topic-aware queue nodes (queue:<system>:<topic>) so each Kafka
// topic is a distinct node and a broadcast topic (bsa.kafka.core.cache.refresh)
// stops collapsing the whole graph into one queue:kafka hairball. These pins
// guard the two parse sites that the format change touches (CLAUDE.md #11).

describe('infraNodeSystem', () => {
  it('extracts the system from every node form (the regression)', () => {
    // The old `slice(indexOf(":")+1)` wrongly yielded "kafka:topic" here.
    expect(infraNodeSystem('queue:kafka:bsa.kafka.core.cache.refresh')).toBe('kafka');
    expect(infraNodeSystem('queue:kafka@broker-1')).toBe('kafka');
    expect(infraNodeSystem('queue:kafka')).toBe('kafka');
    expect(infraNodeSystem('db:postgresql@10.0.1.5')).toBe('postgresql');
    expect(infraNodeSystem('db:postgresql')).toBe('postgresql');
  });
  it('returns empty for a name without a prefix colon', () => {
    expect(infraNodeSystem('accounts-api')).toBe('');
  });
});

describe('infraNodeLabel', () => {
  it('shows the topic for a topic-scoped queue', () => {
    expect(infraNodeLabel('queue:kafka:bsa.kafka.core.cache.refresh'))
      .toBe('bsa.kafka.core.cache.refresh');
  });
  it('shows system(+host) for a non-topic queue', () => {
    expect(infraNodeLabel('queue:kafka@broker-1')).toBe('kafka@broker-1');
    expect(infraNodeLabel('queue:kafka')).toBe('kafka');
  });
  it('strips the db:/ext: prefix (the kind icon conveys the type)', () => {
    expect(infraNodeLabel('db:postgresql@10.0.1.5')).toBe('postgresql@10.0.1.5');
    expect(infraNodeLabel('ext:stripe')).toBe('stripe');
  });
  it('leaves a bare service name untouched', () => {
    expect(infraNodeLabel('accounts-api')).toBe('accounts-api');
  });
});

// v0.8.x — node-size encoding (Uptrace service-graph adapt, slices 2-3). The
// card width encodes a per-node edge rollup; these pin the two pure helpers
// that the canvas + dagre layout both read so a busy service renders WIDER than
// a quiet one and one hot node can't dwarf the canvas (CLAUDE.md #11). Slice 3
// makes both axes user-controllable — mode (incoming|outgoing) × metric
// (rate|duration) — so the table below exercises all four combinations plus the
// call-weighted-duration guard.

describe('mapNumber', () => {
  it('maps the range linearly onto [outMin,outMax]', () => {
    expect(mapNumber(0, 0, 100, 140, 220)).toBe(140);   // floor
    expect(mapNumber(100, 0, 100, 140, 220)).toBe(220);  // ceil
    expect(mapNumber(50, 0, 100, 140, 220)).toBe(180);   // midpoint
  });
  it('CLAMPS out-of-range inputs so one hot node can not dwarf the canvas', () => {
    expect(mapNumber(500, 0, 100, 140, 220)).toBe(220);  // above max → MAX_W
    expect(mapNumber(-5, 0, 100, 140, 220)).toBe(140);   // below min → MIN_W
  });
  it('collapses a degenerate input range to outMin (no divide-by-zero)', () => {
    expect(mapNumber(7, 0, 0, 140, 220)).toBe(140);   // max=0 → all nodes MIN_W
    expect(mapNumber(7, 5, 5, 140, 220)).toBe(140);   // flat graph → MIN_W
    expect(mapNumber(7, 10, 0, 140, 220)).toBe(140);  // inverted range → MIN_W
  });
  it('treats a non-finite value as outMin', () => {
    expect(mapNumber(NaN, 0, 100, 140, 220)).toBe(140);
    expect(mapNumber(Infinity, 0, 100, 140, 220)).toBe(140);
  });
});

describe('nodeSizeMetric', () => {
  const nodes = [{ id: 'a' }, { id: 'b' }, { id: 'c' }];
  // a→b and a→c are a's two OUT edges; b→c is b's only OUT edge. As IN edges:
  // b has one (a→b), c has two (a→c, b→c), a has none.
  //   a→b: rate 30, avgMs 100, calls 10
  //   a→c: rate 12, avgMs  40, calls 30
  //   b→c: rate  5, avgMs 200, calls  4
  const edges = [
    { source: 'a', target: 'b', rate: 30, avgMs: 100, calls: 10 },
    { source: 'a', target: 'c', rate: 12, avgMs: 40, calls: 30 },
    { source: 'b', target: 'c', rate: 5, avgMs: 200, calls: 4 },
  ];

  // ── the four mode × metric combinations ─────────────────────────────────
  it('outgoing + rate → sums rate over edges where the node is the SOURCE', () => {
    const { metric, max } = nodeSizeMetric(nodes, edges, 'outgoing', 'rate');
    expect(metric.get('a')).toBe(42); // 30 + 12 — busiest source
    expect(metric.get('b')).toBe(5);  // b→c
    expect(metric.get('c')).toBe(0);  // pure sink (only a target) → 0
    expect(max).toBe(42);
  });
  it('incoming + rate → sums rate over edges where the node is the TARGET', () => {
    const { metric, max } = nodeSizeMetric(nodes, edges, 'incoming', 'rate');
    expect(metric.get('a')).toBe(0);  // pure source (never a target) → 0
    expect(metric.get('b')).toBe(30); // a→b
    expect(metric.get('c')).toBe(17); // a→c (12) + b→c (5)
    expect(max).toBe(30);
  });
  it('outgoing + duration → call-weighted avgMs over the SOURCE edges', () => {
    const { metric, max } = nodeSizeMetric(nodes, edges, 'outgoing', 'duration');
    // a: (100*10 + 40*30)/(10+30) = (1000+1200)/40 = 2200/40 = 55
    expect(metric.get('a')).toBe(55);
    // b: single edge → its own avgMs (200*4/4)
    expect(metric.get('b')).toBe(200);
    expect(metric.get('c')).toBe(0); // no OUT edges → 0
    expect(max).toBe(200);
  });
  it('incoming + duration → call-weighted avgMs over the TARGET edges', () => {
    const { metric, max } = nodeSizeMetric(nodes, edges, 'incoming', 'duration');
    expect(metric.get('a')).toBe(0);   // no IN edges → 0
    expect(metric.get('b')).toBe(100); // single IN edge a→b → 100
    // c: (40*30 + 200*4)/(30+4) = (1200+800)/34 = 2000/34 ≈ 58.82
    expect(metric.get('c')).toBeCloseTo(2000 / 34, 6);
    expect(max).toBe(100); // b's 100 > c's ~58.8
  });

  // ── the weighted-duration guard: Σcalls=0 must not divide-by-zero ────────
  it('duration with only zero-call edges yields 0, not NaN (the guard)', () => {
    const { metric, max } = nodeSizeMetric(nodes, [
      { source: 'a', target: 'b', rate: 9, avgMs: 500, calls: 0 },
      { source: 'a', target: 'c', rate: 0, avgMs: 999, calls: 0 },
    ], 'outgoing', 'duration');
    expect(metric.get('a')).toBe(0); // every contributing edge weighs 0 → 0
    expect(metric.get('b')).toBe(0);
    expect(metric.get('c')).toBe(0);
    expect(max).toBe(0);
  });

  // ── defaults + defensive edges (preserved from slice 2) ─────────────────
  it('defaults to outgoing + rate when mode/metric are omitted', () => {
    const def = nodeSizeMetric(nodes, edges);
    const explicit = nodeSizeMetric(nodes, edges, 'outgoing', 'rate');
    expect(def.metric.get('a')).toBe(explicit.metric.get('a'));
    expect(def.metric.get('b')).toBe(explicit.metric.get('b'));
    expect(def.max).toBe(explicit.max);
  });
  it('seeds every node at 0 and reports max=0 when there are no edges', () => {
    const { metric, max } = nodeSizeMetric(nodes, []);
    expect(metric.get('a')).toBe(0);
    expect(metric.get('b')).toBe(0);
    expect(metric.get('c')).toBe(0);
    expect(max).toBe(0);
  });
  it('skips edges whose relevant endpoint is not in the node set', () => {
    const { metric, max } = nodeSizeMetric(nodes, [
      { source: 'ghost', target: 'a', rate: 99, avgMs: 1, calls: 1 },
      { source: 'a', target: 'b', rate: 7, avgMs: 1, calls: 1 },
    ], 'outgoing', 'rate');
    expect(metric.has('ghost')).toBe(false);
    expect(metric.get('a')).toBe(7);
    expect(max).toBe(7);
  });
  it('ignores non-finite edge rates', () => {
    const { metric } = nodeSizeMetric(nodes, [
      { source: 'a', target: 'b', rate: NaN, avgMs: 1, calls: 1 },
      { source: 'a', target: 'c', rate: 8, avgMs: 1, calls: 1 },
    ], 'outgoing', 'rate');
    expect(metric.get('a')).toBe(8);
  });
});
