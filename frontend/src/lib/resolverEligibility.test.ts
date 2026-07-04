import { describe, expect, it } from 'vitest';
import {
  TIER_DIM_KEYS, EXEMPLAR_AGGS, isResolverEligible, serviceRedDescriptors,
} from './resolverEligibility';
import {
  TIER_DIM_KEYS as MODEL_DIMS, EXEMPLAR_AGGS as MODEL_AGGS,
} from '@/pages/explore/model';
import type { MetricQuery, MetricAgg } from './metricQuery';

// GRAN-D (v0.8.249) — ServiceCharts' RED family moved from the 5m-MV
// spanMetricBatch read to /api/metrics/resolve (spanmetrics 1s/10s/1m tiers +
// width-aware step). Three contracts pinned here:
//   1. serviceRedDescriptors builds tier-eligible descriptors — an edit that
//      knocks them off the tiers would silently drop the charts back to the
//      5m fallback path (no error, just coarse lines).
//   2. isResolverEligible mirrors the resolver planner's gate and fails
//      CLOSED on anything the tiers can't express.
//   3. The constants extracted from pages/explore/model.ts (layering fix:
//      lib must not depend on one page's builder model) re-export as the
//      SAME objects, so Explore's exemplarDescriptor behaviour is unchanged.

describe('serviceRedDescriptors — service → RED descriptor table', () => {
  const red = serviceRedDescriptors('checkout');
  const cases: Array<[key: 'rps' | 'err' | 'p99', metric: string, agg: string, unit: string]> = [
    ['rps', 'calls_total', 'rate', 'rps'],
    ['err', 'calls_total', 'error_rate', '%'],
    ['p99', 'duration_milliseconds_bucket', 'p99', 'ms'],
  ];
  it.each(cases)('%s → %s/%s (%s), service-pinned, split by name', (key, metric, agg, unit) => {
    const mq = red[key];
    expect(mq.source).toBe('spanmetrics');
    expect(mq.metric).toBe(metric);
    expect(mq.agg).toBe(agg);
    expect(mq.unit).toBe(unit);
    expect(mq.filters).toEqual({ 'service.name': 'checkout' });
    expect(mq.groupBy).toEqual(['name']);
  });

  it('all three are resolver-eligible by construction', () => {
    for (const mq of Object.values(serviceRedDescriptors('payments'))) {
      expect(isResolverEligible(mq)).toBe(true);
    }
  });

  it('service names pass through verbatim (filters map — no DSL quoting)', () => {
    const red2 = serviceRedDescriptors('core-banking "eu-1"');
    expect(red2.rps.filters['service.name']).toBe('core-banking "eu-1"');
    expect(isResolverEligible(red2.rps)).toBe(true);
  });
});

describe('isResolverEligible — planner-gate boundaries', () => {
  const base = serviceRedDescriptors('checkout').rps;
  const mut = (patch: Partial<MetricQuery>): MetricQuery => ({ ...base, ...patch });

  it('rejects non-spanmetrics sources', () => {
    expect(isResolverEligible(mut({ source: 'tracemetrics' }))).toBe(false);
  });

  it('rejects metrics the tiers do not materialise (fails closed)', () => {
    expect(isResolverEligible(mut({ metric: 'traces_total' }))).toBe(false);
    expect(isResolverEligible(mut({ metric: 'duration_milliseconds_sum' }))).toBe(false);
  });

  it('rejects aggs the rollups cannot serve', () => {
    for (const agg of ['p999', 'min', 'max', 'last']) {
      expect(isResolverEligible(mut({ agg: agg as MetricAgg }))).toBe(false);
    }
  });

  it('accepts every rollup-served agg', () => {
    for (const agg of EXEMPLAR_AGGS) {
      expect(isResolverEligible(mut({ agg: agg as MetricAgg }))).toBe(true);
    }
  });

  it('rejects filter keys off the tier dimensions', () => {
    expect(isResolverEligible(mut({
      filters: { 'service.name': 'checkout', 'k8s.pod.name': 'checkout-abc' },
    }))).toBe(false);
  });

  it('rejects group-by keys off the tier dimensions', () => {
    expect(isResolverEligible(mut({ groupBy: ['deployment.environment'] }))).toBe(false);
  });

  it('accepts every tier dim as filter and as split (both spellings)', () => {
    for (const k of TIER_DIM_KEYS) {
      expect(isResolverEligible(mut({ filters: { [k]: 'x' }, groupBy: [k] }))).toBe(true);
    }
  });

  it('tolerates empty filters / absent groupBy', () => {
    expect(isResolverEligible(mut({ filters: {}, groupBy: undefined }))).toBe(true);
  });
});

describe('layering — model.ts re-exports the SAME constants', () => {
  it('TIER_DIM_KEYS is one object, not a copy', () => {
    expect(MODEL_DIMS).toBe(TIER_DIM_KEYS);
  });
  it('EXEMPLAR_AGGS is one object, not a copy', () => {
    expect(MODEL_AGGS).toBe(EXEMPLAR_AGGS);
  });
});
