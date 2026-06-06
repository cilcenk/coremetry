import { describe, expect, it } from 'vitest';
import {
  metricQuery,
  defaultUnit,
  describeMetricQuery,
  encodeMetricQuery,
  decodeMetricQuery,
  metricExploreHref,
  type MetricQuery,
} from './metricQuery';

// v0.8.47 ("every metric is a doorway" Phase A) — the canonical MetricQuery
// descriptor + its lossless URL codec. The same object draws a panel and opens
// the explorer, so the round-trip MUST be exact (deep links restore state).

describe('metricQuery normalizer', () => {
  it('fills defaults from metric+agg', () => {
    const mq = metricQuery({ metric: 'calls_total', agg: 'rate' });
    expect(mq.source).toBe('spanmetrics');
    expect(mq.unit).toBe('rps');
    expect(mq.viz).toBe('line');
    expect(mq.filters).toEqual({});
  });
  it('respects explicit fields over defaults', () => {
    const mq = metricQuery({ metric: 'duration_milliseconds_bucket', agg: 'p99', viz: 'stat', unit: 'ms', source: 'tracemetrics' });
    expect(mq.source).toBe('tracemetrics');
    expect(mq.viz).toBe('stat');
    expect(mq.unit).toBe('ms');
  });
});

describe('defaultUnit', () => {
  it('maps aggregations to natural units', () => {
    expect(defaultUnit('rate')).toBe('rps');
    expect(defaultUnit('error_rate')).toBe('%');
    expect(defaultUnit('p99')).toBe('ms');
    expect(defaultUnit('avg')).toBe('ms');
    expect(defaultUnit('count')).toBe('count');
    expect(defaultUnit('sum')).toBe('count');
  });
});

// v0.8.48 ("every metric is a doorway" Phase B) — describeMetricQuery renders
// the human-legible PromQL-style projection shown in the panel's "View query"
// affordance. Display-only (not a parser input); filters render sorted so the
// string is stable for two panels carrying the same matcher set.
describe('describeMetricQuery', () => {
  it('renders agg(metric{filters}) by (groupBy)', () => {
    const mq = metricQuery({
      metric: 'calls_total',
      agg: 'rate',
      filters: { 'service.name': 'checkout', status: 'ERROR' },
      groupBy: ['service.name'],
    });
    expect(describeMetricQuery(mq)).toBe(
      'rate(calls_total{service.name="checkout", status="ERROR"}) by (service.name)',
    );
  });
  it('omits the matcher when there are no filters and the by-clause when no groupBy', () => {
    const mq = metricQuery({ metric: 'calls_total', agg: 'count' });
    expect(describeMetricQuery(mq)).toBe('count(calls_total)');
  });
  it('sorts filter keys for a stable string regardless of insertion order', () => {
    const a = metricQuery({ metric: 'm', agg: 'sum', filters: { b: '2', a: '1' } });
    const b = metricQuery({ metric: 'm', agg: 'sum', filters: { a: '1', b: '2' } });
    expect(describeMetricQuery(a)).toBe('sum(m{a="1", b="2"})');
    expect(describeMetricQuery(a)).toBe(describeMetricQuery(b));
  });
});

describe('encode/decode round-trip', () => {
  it('restores a descriptor exactly (incl. unicode filters + range)', () => {
    const mq: MetricQuery = {
      source: 'spanmetrics',
      metric: 'duration_milliseconds_bucket',
      agg: 'p99',
      unit: 'ms',
      filters: { 'service.name': 'çöp-sürücü', 'http.route': '/v1/ödeme', status: 'ERROR' },
      groupBy: ['service.name', 'http.route'],
      viz: 'line',
      step: '1m',
      range: { preset: '6h' } as MetricQuery['range'],
    };
    const back = decodeMetricQuery(encodeMetricQuery(mq));
    expect(back).toEqual(mq);
  });

  it('base64url has no +/= chars (URL-safe)', () => {
    const enc = encodeMetricQuery(metricQuery({ metric: 'calls_total', agg: 'rate', filters: { a: 'b/c+d=e' } }));
    expect(enc).not.toMatch(/[+/=]/);
  });

  it('metricExploreHref carries the descriptor on /explore?m=', () => {
    const href = metricExploreHref(metricQuery({ metric: 'calls_total', agg: 'rate' }));
    expect(href.startsWith('/explore?m=')).toBe(true);
    const decoded = decodeMetricQuery(href.split('?m=')[1]);
    expect(decoded?.metric).toBe('calls_total');
  });

  it('rejects garbage gracefully', () => {
    expect(decodeMetricQuery('not-valid-base64-$$$')).toBeNull();
    expect(decodeMetricQuery('')).toBeNull();
    expect(decodeMetricQuery(null)).toBeNull();
  });
});
