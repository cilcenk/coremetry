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

// v0.8.x — node-size encoding (Uptrace service-graph adapt, slice 2). The card
// width encodes outgoing throughput; these pin the two pure helpers that the
// canvas + dagre layout both read so a busy service renders WIDER than a quiet
// one and one hot node can't dwarf the canvas (CLAUDE.md #11).

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

describe('nodeSizeMetric (outgoing + rate rollup)', () => {
  const nodes = [{ id: 'a' }, { id: 'b' }, { id: 'c' }];

  it('sums the rate of edges where the node is the SOURCE', () => {
    const { metric, max } = nodeSizeMetric(nodes, [
      { source: 'a', target: 'b', rate: 30 },
      { source: 'a', target: 'c', rate: 12 },
      { source: 'b', target: 'c', rate: 5 },
    ]);
    expect(metric.get('a')).toBe(42); // 30 + 12 — busiest source
    expect(metric.get('b')).toBe(5);
    expect(metric.get('c')).toBe(0);  // pure sink (only a target) → 0
    expect(max).toBe(42);
  });
  it('seeds every node at 0 and reports max=0 when there are no edges', () => {
    const { metric, max } = nodeSizeMetric(nodes, []);
    expect(metric.get('a')).toBe(0);
    expect(metric.get('b')).toBe(0);
    expect(metric.get('c')).toBe(0);
    expect(max).toBe(0);
  });
  it('skips edges whose source is not in the node set', () => {
    const { metric, max } = nodeSizeMetric(nodes, [
      { source: 'ghost', target: 'a', rate: 99 },
      { source: 'a', target: 'b', rate: 7 },
    ]);
    expect(metric.has('ghost')).toBe(false);
    expect(metric.get('a')).toBe(7);
    expect(max).toBe(7);
  });
  it('ignores non-finite edge rates', () => {
    const { metric } = nodeSizeMetric(nodes, [
      { source: 'a', target: 'b', rate: NaN },
      { source: 'a', target: 'c', rate: 8 },
    ]);
    expect(metric.get('a')).toBe(8);
  });
});
