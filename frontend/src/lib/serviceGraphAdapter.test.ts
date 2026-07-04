import { describe, it, expect } from 'vitest';
import { serviceGraphToMap } from './serviceGraphAdapter';
import type { ServiceGraphResponse } from './types';

// v0.8.273 — operator-reported: /service-map Focus rendered an
// empty graph on the 1200-service test install because the page
// filtered the 200-trace SAMPLE. The focus view now renders from
// the MV-backed /api/servicegraph via this adapter; these cases
// pin the shape contract (id passthrough incl. db:/queue:
// synthetic prefixes, errorRate 0..100 → 0..1, kind 'service' →
// '' discriminator, edge field mapping).
describe('serviceGraphToMap', () => {
  const g: ServiceGraphResponse = {
    scope: 'neighborhood',
    focus: 'checkout',
    nodes: [
      { id: 'checkout', name: 'checkout', kind: 'service', calls: 1000, errors: 20, errorRate: 2, rate: 33 },
      { id: 'db:oracle', name: 'oracle', kind: 'db', system: 'oracle', calls: 400, errors: 0, errorRate: 0, rate: 13 },
    ] as ServiceGraphResponse['nodes'],
    edges: [
      { source: 'checkout', target: 'db:oracle', calls: 400, errors: 4, errorRate: 1, rate: 13, avgMs: 12, p99Ms: 90 },
    ] as ServiceGraphResponse['edges'],
  };

  it('maps nodes: id passthrough, errorRate scaled to fraction, service kind → ""', () => {
    const m = serviceGraphToMap(g);
    expect(m.nodes[0]).toMatchObject({ service: 'checkout', spanCount: 1000, errorRate: 0.02, kind: '' });
    expect(m.nodes[1]).toMatchObject({ service: 'db:oracle', kind: 'db', subkind: 'oracle' });
  });

  it('maps edges source/target → caller/callee with counts', () => {
    const m = serviceGraphToMap(g);
    expect(m.edges[0]).toMatchObject({ caller: 'checkout', callee: 'db:oracle', spanCount: 400, errorCount: 4 });
  });

  it('tolerates empty/missing arrays', () => {
    const m = serviceGraphToMap({ scope: 'global', nodes: [], edges: [] });
    expect(m.nodes).toEqual([]);
    expect(m.edges).toEqual([]);
    expect(m.totalSpans).toBe(0);
  });
});
