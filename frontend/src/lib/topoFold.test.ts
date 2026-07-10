import { describe, it, expect } from 'vitest';
import {
  foldTopology, defaultCollapsed, parseNsFold, encodeNsFold,
  nsNodeId, isNsNode, nsOfNodeId,
  AUTO_FOLD_MIN_GRAPH, AUTO_FOLD_MIN_MEMBERS,
} from './topoFold';
import type { ServiceMap, ServiceMapEdge, ServiceMapNode } from './types';

// v0.8.447 — collapsible namespace group boxes, pure merge semantics.

const node = (service: string, over: Partial<ServiceMapNode> = {}): ServiceMapNode =>
  ({ service, spanCount: 100, errorRate: 0, ...over });
const edge = (caller: string, callee: string, over: Partial<ServiceMapEdge> = {}): ServiceMapEdge =>
  ({ caller, callee, traceCount: 10, spanCount: 100, errorCount: 0, ...over });
const map = (nodes: ServiceMapNode[], edges: ServiceMapEdge[]): ServiceMap =>
  ({ nodes, edges } as ServiceMap);

const NS: Record<string, string | undefined> = {
  a1: 'pay', a2: 'pay', a3: 'pay',
  b1: 'chan', b2: 'chan',
  free: undefined,
};
const nsOf = (s: string) => NS[s];

describe('foldTopology', () => {
  it('folds members into one super-node and re-targets edges', () => {
    const r = foldTopology(map(
      [node('a1'), node('a2'), node('b1'), node('b2'), node('free')],
      [edge('b1', 'a1'), edge('b2', 'a2'), edge('a1', 'free')],
    ), nsOf, new Set(['pay']));
    const names = r.data.nodes.map(n => n.service).sort();
    expect(names).toEqual(['b1', 'b2', 'free', nsNodeId('pay')]);
    expect(r.groups).toHaveLength(1);
    expect(r.groups[0].members).toEqual(['a1', 'a2']);
    // b1→nsgroup:pay, b2→nsgroup:pay, nsgroup:pay→free
    const keys = r.data.edges.map(e => `${e.caller}|${e.callee}`).sort();
    expect(keys).toEqual(['b1|nsgroup:pay', 'b2|nsgroup:pay', 'nsgroup:pay|free']);
  });

  it('bundles parallel edges: counts sum, p99 is max, rates add, avgMs drops', () => {
    const r = foldTopology(map(
      [node('a1'), node('a2'), node('free')],
      [
        edge('free', 'a1', { spanCount: 100, errorCount: 5, traceCount: 10, rate: 60, p99Ms: 200, avgMs: 40 }),
        edge('free', 'a2', { spanCount: 300, errorCount: 1, traceCount: 30, rate: 30, p99Ms: 900, avgMs: 80 }),
      ],
    ), nsOf, new Set(['pay']));
    expect(r.data.edges).toHaveLength(1);
    const e = r.data.edges[0];
    expect(e.spanCount).toBe(400);
    expect(e.errorCount).toBe(6);
    expect(e.traceCount).toBe(40);
    expect(e.rate).toBe(90);
    expect(e.p99Ms).toBe(900);
    expect(e.avgMs).toBeUndefined();
    expect(e.errorRate).toBeCloseTo(6 / 400);
    expect(r.bundled.get(`free|${nsNodeId('pay')}`)).toBe(2);
  });

  it('drops intra-group self-loops', () => {
    const r = foldTopology(map(
      [node('a1'), node('a2')],
      [edge('a1', 'a2'), edge('a2', 'a1')],
    ), nsOf, new Set(['pay']));
    expect(r.data.edges).toHaveLength(0);
  });

  it('super-node aggregates span-weighted error rate', () => {
    const r = foldTopology(map(
      [node('a1', { spanCount: 900, errorRate: 0 }), node('a2', { spanCount: 100, errorRate: 0.1 })],
      [],
    ), nsOf, new Set(['pay']));
    const g = r.groups[0];
    expect(g.spanCount).toBe(1000);
    expect(g.errorRate).toBeCloseTo(0.01);
    const sn = r.data.nodes.find(n => n.service === nsNodeId('pay'))!;
    expect(sn.spanCount).toBe(1000);
    expect(sn.errorRate).toBeCloseTo(0.01);
  });

  it('ignores single-member folds and kind`d dep nodes', () => {
    const r = foldTopology(map(
      // only b1 of chan present (<2), a-nodes kind'd → never folded
      [node('b1'), node('a1', { kind: 'db', subkind: 'postgresql' }), node('a2', { kind: 'external' })],
      [edge('b1', 'a1')],
    ), nsOf, new Set(['chan', 'pay']));
    expect(r.groups).toHaveLength(0);
    expect(r.data.nodes.map(n => n.service).sort()).toEqual(['a1', 'a2', 'b1']);
    expect(r.data.edges).toHaveLength(1);
  });

  it('no-op on empty collapsed set (same object back)', () => {
    const m = map([node('a1'), node('a2')], [edge('a1', 'a2')]);
    const r = foldTopology(m, nsOf, new Set());
    expect(r.data).toBe(m);
    expect(r.groups).toHaveLength(0);
  });
});

describe('defaultCollapsed', () => {
  const bigNs = (ns: string, n: number) =>
    Array.from({ length: n }, (_, i) => node(`${ns}-${i}`));
  const lookup = (s: string) => s.split('-')[0];

  it('stays empty for small graphs', () => {
    const nodes = [...bigNs('x', AUTO_FOLD_MIN_MEMBERS + 2), ...bigNs('y', 5)];
    expect(nodes.length).toBeLessThanOrEqual(AUTO_FOLD_MIN_GRAPH);
    expect(defaultCollapsed(map(nodes, []), lookup).size).toBe(0);
  });

  it('folds only big namespaces on crowded graphs; kind`d nodes not counted', () => {
    const nodes = [
      ...bigNs('x', AUTO_FOLD_MIN_MEMBERS),      // folds
      ...bigNs('y', AUTO_FOLD_MIN_MEMBERS - 1),  // stays
      ...bigNs('z', 20),                         // folds → graph > threshold
      node('db-1', { kind: 'db' }),
    ];
    const out = defaultCollapsed(map(nodes, []), lookup);
    expect(out).toEqual(new Set(['x', 'z']));
  });
});

describe('nsfold codec', () => {
  it('round-trips', () => {
    expect(parseNsFold(null)).toBeNull();
    expect(parseNsFold('')).toBeNull();
    expect(parseNsFold('-')).toEqual(new Set());
    expect(parseNsFold('pay,chan')).toEqual(new Set(['pay', 'chan']));
    expect(encodeNsFold(new Set())).toBe('-');
    expect(encodeNsFold(new Set(['chan', 'pay']))).toBe('chan,pay');
    expect(parseNsFold(encodeNsFold(new Set(['pay'])))).toEqual(new Set(['pay']));
  });
  it('node id helpers', () => {
    expect(isNsNode(nsNodeId('pay'))).toBe(true);
    expect(isNsNode('payments-api')).toBe(false);
    expect(nsOfNodeId(nsNodeId('pay'))).toBe('pay');
  });

  // v0.8.447 review-fix — deriver serbest-metin service.namespace'i aynen
  // geçirir: virgüllü isim listeyi bölüyordu, '-' adlı isim boş-küme
  // sentineliyle çakışıyordu. Üyeler artık percent-encoded.
  it('round-trips hostile namespace names (comma / percent / literal dash)', () => {
    for (const ns of ['acme,pay', '100%pay', '-', 'a-b', 'Türk ödeme']) {
      expect(parseNsFold(encodeNsFold(new Set([ns])))).toEqual(new Set([ns]));
    }
    const mixed = new Set(['acme,pay', '-', 'plain']);
    expect(parseNsFold(encodeNsFold(mixed))).toEqual(mixed);
    // Tek üyeli {'-'} kümesi sentinel '-' ile ÇAKIŞMAMALI.
    expect(encodeNsFold(new Set(['-']))).not.toBe('-');
    // Eski k8s-charset URL'ler geriye dönük aynı çözülür.
    expect(parseNsFold('pay,chan')).toEqual(new Set(['pay', 'chan']));
  });
});
