// pinToDashboard.test.ts — v0.8.419 Data-Explorer parity DE4. Pins the
// BuilderQuery → dashboard Panel mapping so a builder edit can't silently
// change what a pinned panel renders.
import { describe, expect, it } from 'vitest';
import { isPinnable, queryToPanel } from './pinToDashboard';
import { blankQuery } from './model';
import type { MetricPanelConfig, SpanMetricPanelConfig } from '@/lib/types';

describe('queryToPanel — metric source', () => {
  const q = {
    ...blankQuery('A', 'metric'),
    metric: 'jvm.memory.used', unit: 'MB', agg: 'p95',
    scope: 'checkout', splitBy: ['host.name'],
    filters: [{ k: 'deployment.environment', op: '=' as const, v: ['prod'] }],
  };

  it('maps to a metric panel config verbatim', () => {
    const p = queryToPanel(q, { step: 60 })!;
    expect(p.type).toBe('metric');
    expect(p.width).toBe(2);
    const cfg = p.config as MetricPanelConfig;
    expect(cfg.metricName).toBe('jvm.memory.used');
    expect(cfg.service).toBe('checkout');
    expect(cfg.agg).toBe('p95');
    expect(cfg.groupBy).toBe('host.name');
    expect(cfg.step).toBe(60);
    expect(JSON.parse(cfg.filters!)).toEqual(q.filters);
  });

  it('auto step (0) stays absent so GRAN-C width-aware auto applies', () => {
    const cfg = queryToPanel(q, { step: 0 })!.config as MetricPanelConfig;
    expect(cfg.step).toBeUndefined();
  });

  it('no metric picked yet → not pinnable', () => {
    expect(queryToPanel({ ...q, metric: '' })).toBeNull();
    expect(isPinnable({ ...q, metric: '' })).toBe(false);
  });
});

describe('queryToPanel — span source', () => {
  const q = {
    ...blankQuery('B'),
    agg: 'p99', scope: 'payments', splitBy: ['name'],
    dsl: 'http.status_code >= 500',
    filters: [{ k: 'http.method', op: '=' as const, v: ['POST'] }],
  };

  it('folds the scope into a leading service.name filter', () => {
    const cfg = queryToPanel(q)!.config as SpanMetricPanelConfig;
    expect(JSON.parse(cfg.filters!)).toEqual([
      { k: 'service.name', op: '=', v: ['payments'] },
      { k: 'http.method', op: '=', v: ['POST'] },
    ]);
    expect(cfg.agg).toBe('p99');
    expect(cfg.groupBy).toBe('name');
    expect(cfg.dsl).toBe('http.status_code >= 500');
    // default field (duration_ms) is omitted, not repeated
    expect(cfg.field).toBeUndefined();
  });

  it('spanmetric panel type with default title from queryDesc', () => {
    const p = queryToPanel(q)!;
    expect(p.type).toBe('spanmetric');
    expect(p.title.length).toBeGreaterThan(0);
    expect(queryToPanel(q, { title: 'My tile' })!.title).toBe('My tile');
  });

  it('genuine OR filter group refuses the pin (no silent flatten)', () => {
    const grouped = {
      ...q,
      filterGroup: { join: 'OR' as const, filters: q.filters },
    };
    expect(isPinnable(grouped)).toBe(false);
    expect(queryToPanel(grouped)).toBeNull();
  });
});
