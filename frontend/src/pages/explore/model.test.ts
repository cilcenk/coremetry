import { describe, it, expect } from 'vitest';
import {
  blankQuery, exemplarDescriptor, pinnedService, pinnedOperation,
  seriesGroupLabel, type BuilderQuery,
} from './model';
import type { FilterExpr } from '@/lib/types';

// explore-v2 Phase-3 — pins the exemplar-eligibility gate and the SLO/deploy
// service-pin extraction. exemplarDescriptor mirrors the backend planner
// (chstore/metricresolve.go tierDimColumn + spanmetricStateAgg): a query the
// rollups can't serve MUST return null, or the panel fires a resolveMetric
// that falls back to the raw-spans path and returns no exemplars anyway —
// a wasted billion-row-window query per render cycle.

const f = (k: string, v: string, op: FilterExpr['op'] = '='): FilterExpr =>
  ({ k, op, v: [v] });

const q = (over: Partial<BuilderQuery>): BuilderQuery =>
  ({ ...blankQuery('A'), ...over });

describe('exemplarDescriptor', () => {
  it('accepts a plain p95 duration query and maps it onto spanmetrics', () => {
    const d = exemplarDescriptor(q({
      agg: 'p95', metric: 'duration_ms', scope: 'checkout',
      splitBy: ['name'], filters: [f('kind', 'server')],
    }));
    expect(d).not.toBeNull();
    expect(d!.source).toBe('spanmetrics');
    expect(d!.metric).toBe('duration_milliseconds_bucket');
    expect(d!.agg).toBe('p95');
    expect(d!.filters).toEqual({ 'service.name': 'checkout', kind: 'server' });
    expect(d!.groupBy).toEqual(['name']);
  });

  it('maps count-shaped aggs onto calls_total with no groupBy when splitBy empty', () => {
    const d = exemplarDescriptor(q({ agg: 'rate' }));
    expect(d).not.toBeNull();
    expect(d!.metric).toBe('calls_total');
    expect(d!.groupBy).toBeUndefined();
  });

  // Table of rejections — every row is a query the rollups cannot serve.
  const rejects: Array<[string, Partial<BuilderQuery>]> = [
    ['metric source',            { source: 'metric', metric: 'jvm.gc.pause', agg: 'avg' }],
    ['DSL present',              { dsl: 'duration > 500ms' }],
    ['p999 (not on rollups)',    { agg: 'p999', metric: 'duration_ms' }],
    ['min (not on rollups)',     { agg: 'min', metric: 'duration_ms' }],
    ['non-duration field',       { agg: 'p95', metric: 'http.response_size' }],
    ['off-dim filter',           { filters: [f('db.system', 'postgresql')] }],
    ['non-eq operator',          { filters: [{ k: 'kind', op: 'IN', v: ['server', 'client'] }] }],
    ['off-dim splitBy',          { splitBy: ['db.system'] }],
    ['contradictory dup filter', { filters: [f('kind', 'server'), f('kind', 'client')] }],
    // gap-2 → Explore: a genuine OR / nested group can't be expressed as the
    // resolver's equality-only filter map, so it must fall to raw spanMetric.
    ['grouped OR filter', {
      filterGroup: { join: 'OR', filters: [f('kind', 'server'), f('kind', 'client')] },
    }],
  ];
  for (const [name, over] of rejects) {
    it(`rejects: ${name}`, () => {
      expect(exemplarDescriptor(q(over))).toBeNull();
    });
  }

  it('accepts a duplicate filter that agrees (no information lost in the map)', () => {
    const d = exemplarDescriptor(q({ filters: [f('kind', 'server'), f('kind', 'server')] }));
    expect(d).not.toBeNull();
  });

  // A flat-AND group is inert (byte-identical to the flat chip path), so it
  // must NOT disqualify the resolver — exemplars keep working for it.
  it('accepts a flat-AND filterGroup (inert; exemplars still resolve)', () => {
    const d = exemplarDescriptor(q({
      agg: 'p95', metric: 'duration_ms',
      filterGroup: { join: 'AND', filters: [f('kind', 'server')] },
    }));
    expect(d).not.toBeNull();
  });
});

describe('pinnedService / pinnedOperation', () => {
  it('scope slot wins', () => {
    expect(pinnedService(q({ scope: 'checkout' }))).toBe('checkout');
  });
  it('single service.name = chip pins', () => {
    expect(pinnedService(q({ filters: [f('service.name', 'cart')] }))).toBe('cart');
  });
  it('two service chips = ambiguous = no pin', () => {
    expect(pinnedService(q({
      filters: [f('service.name', 'cart'), f('service.name', 'checkout')],
    }))).toBe('');
  });
  it('non-eq service chip does not pin', () => {
    expect(pinnedService(q({ filters: [f('service.name', 'cart', 'LIKE')] }))).toBe('');
  });
  it('operation pin from a single name = chip', () => {
    expect(pinnedOperation(q({ filters: [f('name', 'GET /cart')] }))).toBe('GET /cart');
    expect(pinnedOperation(q({}))).toBe('');
  });
});

describe('seriesGroupLabel', () => {
  it('matches the PanelStack label derivation (key tail + value, comma-joined)', () => {
    const query = q({ splitBy: ['service.name', 'name'] });
    expect(seriesGroupLabel(query, ['cart', 'GET /cart'], 'desc'))
      .toBe('name=cart, name=GET /cart');
  });
  it('falls back to the query desc when there is no group', () => {
    expect(seriesGroupLabel(q({}), [], 'count')).toBe('count');
  });
});
