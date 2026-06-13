// urlCodec tests (explore-v2 Phase 2).
//
// Two suites:
//  1. ?q= round-trip — encodeBuilder(decodeBuilder(x)) is lossless for the
//     builder model.
//  2. seedFromLegacyParams — table-driven over EVERY inbound legacy shape
//     (plan: these are a permanent decode surface; SavedViews and other
//     pages link with the old params forever).

import { describe, it, expect } from 'vitest';
import { encodeBuilder, decodeBuilder, seedFromLegacyParams, metricCatalogueHref } from './urlCodec';
import { defaultBuilderState, blankQuery, type BuilderState } from './model';
import { encodeMetricQuery, metricQuery } from '@/lib/metricQuery';
import { encodeFilters } from '@/lib/urlState';

describe('?q= codec round-trip', () => {
  it('default state survives', () => {
    const st = defaultBuilderState();
    expect(decodeBuilder(encodeBuilder(st))).toEqual(st);
  });

  it('full multi-query state survives', () => {
    const st: BuilderState = {
      queries: [
        { ...blankQuery('A'), agg: 'p95', scope: 'checkout',
          splitBy: ['service.name', 'name'],
          filters: [{ k: 'http.method', op: '=', v: ['GET'] }], dsl: 'duration > 10ms' },
        { ...blankQuery('B', 'metric'), metric: 'jvm.gc.pause', unit: 'ms', agg: 'p99',
          splitBy: ['host.name'], enabled: false },
      ],
      formula: 'A / B * 100',
      viz: 'bars',
      step: 60,
    };
    expect(decodeBuilder(encodeBuilder(st))).toEqual(st);
  });

  it('rejects garbage', () => {
    expect(decodeBuilder('not json')).toBeNull();
    expect(decodeBuilder('{"q":[]}')).toBeNull();
    expect(decodeBuilder(null)).toBeNull();
  });
});

describe('seedFromLegacyParams', () => {
  const seed = (qs: string) => seedFromLegacyParams(new URLSearchParams(qs));

  it('Services drill — ?range&filters&agg&field&result=metric', () => {
    const filters = encodeFilters([{ k: 'service.name', op: '=', v: ['checkout'] }]);
    const st = seed(`range=30m&filters=${encodeURIComponent(filters)}&agg=p95&field=duration_ms&result=metric`);
    expect(st).not.toBeNull();
    expect(st!.queries).toHaveLength(1);
    const a = st!.queries[0];
    expect(a.source).toBe('span');
    expect(a.agg).toBe('p95');
    expect(a.metric).toBe('duration_ms');
    // single-value service pin lifts into scope; chips stay empty
    expect(a.scope).toBe('checkout');
    expect(a.filters).toEqual([]);
  });

  it('question card — ?agg=error_rate&groupBy=service.name', () => {
    const st = seed('agg=error_rate&groupBy=service.name');
    expect(st!.queries[0].agg).toBe('error_rate');
    expect(st!.queries[0].splitBy).toEqual(['service.name']);
    expect(st!.viz).toBe('line');
  });

  it('question card — ?viz=heatmap', () => {
    const st = seed('viz=heatmap');
    expect(st!.viz).toBe('heatmap');
    expect(st!.queries[0].agg).toBe('count');
  });

  it('legacy topN / kpi viz map to line (no dedicated renderer pre-v2)', () => {
    expect(seed('viz=topN&agg=count')!.viz).toBe('line');
    expect(seed('viz=kpi&agg=count')!.viz).toBe('line');
    expect(seed('viz=bar&agg=count')!.viz).toBe('bars');
  });

  it('D5 — legacy viz=red becomes the 3-query A:rate B:error_rate C:p99 seed', () => {
    const filters = encodeFilters([
      { k: 'service.name', op: '=', v: ['payments'] },
      { k: 'http.method', op: '=', v: ['POST'] },
    ]);
    const st = seed(`viz=red&groupBy=service.name,name&filters=${encodeURIComponent(filters)}`);
    expect(st!.queries.map(q => [q.letter, q.agg])).toEqual([
      ['A', 'rate'], ['B', 'error_rate'], ['C', 'p99'],
    ]);
    for (const q of st!.queries) {
      expect(q.scope).toBe('payments');
      expect(q.filters).toEqual([{ k: 'http.method', op: '=', v: ['POST'] }]);
      expect(q.splitBy).toEqual(['service.name', 'name']);
    }
    expect(st!.viz).toBe('line');
  });

  it('viz=red without groupBy defaults the split to service.name', () => {
    expect(seed('viz=red')!.queries[0].splitBy).toEqual(['service.name']);
  });

  it('DependenciesTable drill — ?metric=<catalogue>&result=metric', () => {
    const st = seed('metric=jvm.gc.pause&result=metric');
    const a = st!.queries[0];
    expect(a.source).toBe('metric');
    expect(a.metric).toBe('jvm.gc.pause');
    expect(a.agg).toBe('avg');
  });

  it('advanced DSL — ?dsl&mode=advanced (metric result) keeps the DSL', () => {
    const st = seed(`dsl=${encodeURIComponent('duration > 500ms')}&mode=advanced&agg=count`);
    expect(st!.queries[0].dsl).toBe('duration > 500ms');
  });

  it('DSL without mode=advanced is ignored (legacy parity)', () => {
    const st = seed(`dsl=${encodeURIComponent('duration > 500ms')}&agg=count`);
    expect(st!.queries[0].dsl).toBe('');
  });

  it('?m= descriptor — metricExploreHref shape', () => {
    const mq = metricQuery({
      metric: 'duration_milliseconds', agg: 'p99',
      filters: { 'service.name': 'checkout', 'http.route': '/pay' },
      groupBy: ['http.route'], viz: 'bar', step: '30s',
    });
    const st = seed(`m=${encodeMetricQuery(mq)}`);
    const a = st!.queries[0];
    expect(a.source).toBe('span');
    expect(a.agg).toBe('p99');
    expect(a.metric).toBe('duration_ms');           // duration-shaped descriptor
    expect(a.scope).toBe('checkout');                // service filter → scope
    expect(a.filters).toEqual([{ k: 'http.route', op: '=', v: ['/pay'] }]);
    expect(a.splitBy).toEqual(['http.route']);
    expect(st!.viz).toBe('bars');
    expect(st!.step).toBe(30);
  });

  it('?q= canonical form wins over stray legacy params', () => {
    const enc = encodeBuilder({
      ...defaultBuilderState(),
      queries: [{ ...blankQuery('A'), agg: 'p99' }],
    });
    const st = seed(`q=${encodeURIComponent(enc)}&agg=count`);
    expect(st!.queries[0].agg).toBe('p99');
  });

  it('other surfaces own their shapes — null for traces/repeats/logs/metrics-source', () => {
    expect(seed('result=traces&dsl=x&mode=advanced')).toBeNull();
    expect(seed('result=repeats&groupBy=db.statement&minRepeats=5')).toBeNull();
    expect(seed('source=logs')).toBeNull();
    expect(seed('source=metrics&service=checkout&metric=jvm.gc.pause')).toBeNull();
  });

  it('no meaningful params → null (entry screen)', () => {
    expect(seed('')).toBeNull();
    expect(seed('range=30m')).toBeNull();
  });

  it('D5 — legacy compare param decodes the rest and drops compare', () => {
    const st = seed('agg=p95&compare=true');
    expect(st).not.toBeNull();
    expect(st!.queries[0].agg).toBe('p95');
  });
});

// Phase-5 (/metrics collapse): metricCatalogueHref is the ONE producer the
// Metrics catalogue, DependenciesTable and ServiceInfra all emit. It must
// round-trip through the SAME ?q= decoder Explore uses, or those three entry
// points land on an empty workspace.
describe('metricCatalogueHref → ?q= round-trip', () => {
  // Extract the q param the href carries and decode it the way Explore does.
  const decode = (href: string) => {
    const qs = href.slice(href.indexOf('?') + 1);
    return seedFromLegacyParams(new URLSearchParams(qs));
  };

  it('service + agg → metric-source query A with scope + agg', () => {
    const st = decode(metricCatalogueHref('jvm.gc.pause', { service: 'checkout', agg: 'p99' }));
    expect(st).not.toBeNull();
    expect(st!.queries).toHaveLength(1);
    const a = st!.queries[0];
    expect(a.source).toBe('metric');
    expect(a.metric).toBe('jvm.gc.pause');
    expect(a.scope).toBe('checkout');
    expect(a.agg).toBe('p99');
  });

  it('bare name → metric-source query A, agg defaults to avg, no scope', () => {
    const st = decode(metricCatalogueHref('process.cpu.utilization'));
    const a = st!.queries[0];
    expect(a.source).toBe('metric');
    expect(a.metric).toBe('process.cpu.utilization');
    expect(a.agg).toBe('avg');
    expect(a.scope).toBe('');
  });
});
