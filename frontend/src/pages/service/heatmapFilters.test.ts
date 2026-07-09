// heatmapFilters.test.ts — v0.8.421 regression. The Service latency
// heatmap sent {key,op,value} filters since v0.8.252; the backend
// unmarshals {k,op,v} (filterexpr.go), so every filter — service,
// cluster pivot, v0.8.415 operation scope — was a silent no-op and the
// panel showed the whole cluster's spans. This pins the WIRE shape.
import { describe, expect, it } from 'vitest';
import { heatmapFilters } from './heatmapFilters';

describe('heatmapFilters wire shape (v0.8.421)', () => {
  const cases: Array<[
    name: string,
    args: [string, string?, string?],
    want: { k: string; op: string; v: string[] }[],
  ]> = [
    ['service only', ['checkout'], [
      { k: 'service.name', op: '=', v: ['checkout'] },
    ]],
    ['service + cluster pivot', ['checkout', 'eu-1'], [
      { k: 'service.name', op: '=', v: ['checkout'] },
      { k: 'k8s.cluster.name', op: '=', v: ['eu-1'] },
    ]],
    ['service + operation scope', ['checkout', undefined, 'GET /cart'], [
      { k: 'service.name', op: '=', v: ['checkout'] },
      { k: 'name', op: '=', v: ['GET /cart'] },
    ]],
    ['all three', ['checkout', 'eu-1', 'GET /cart'], [
      { k: 'service.name', op: '=', v: ['checkout'] },
      { k: 'k8s.cluster.name', op: '=', v: ['eu-1'] },
      { k: 'name', op: '=', v: ['GET /cart'] },
    ]],
  ];

  it.each(cases)('%s', (_name, args, want) => {
    expect(heatmapFilters(...args)).toEqual(want);
  });

  it('serialized payload uses k/op/v keys, never key/value', () => {
    const json = JSON.stringify(heatmapFilters('svc', 'c1', 'op1'));
    expect(json).toContain('"k":');
    expect(json).toContain('"v":');
    expect(json).not.toContain('"key":');
    expect(json).not.toContain('"value":');
  });
});
