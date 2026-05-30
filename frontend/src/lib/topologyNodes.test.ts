import { describe, expect, it } from 'vitest';
import { infraNodeLabel, infraNodeSystem } from './topologyNodes';

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
