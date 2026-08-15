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

  // v0.9.364 — rootOnly konjonktu: Details'taki panel üstündeki rootOnly
  // grafiklerle aynı giriş-span popülasyonunu çizer. Değerler CH'deki
  // küçük-harf literal'lerdir ('server','consumer') — endpoints.go/slo.go
  // ile aynı; büyük harfe drift ederse sessizce boş heatmap olur.
  const rootCases: Array<[
    name: string,
    args: [string, string?, string?, boolean?],
    want: { k: string; op: string; v: string[] }[],
  ]> = [
    ['rootOnly adds entry-span kind conjunct', ['checkout', undefined, undefined, true], [
      { k: 'service.name', op: '=', v: ['checkout'] },
      { k: 'kind', op: 'IN', v: ['server', 'consumer'] },
    ]],
    ['rootOnly composes with cluster + operation', ['checkout', 'eu-1', 'GET /cart', true], [
      { k: 'service.name', op: '=', v: ['checkout'] },
      { k: 'kind', op: 'IN', v: ['server', 'consumer'] },
      { k: 'k8s.cluster.name', op: '=', v: ['eu-1'] },
      { k: 'name', op: '=', v: ['GET /cart'] },
    ]],
    ['rootOnly=false stays kind-free (Overview contract)', ['checkout', undefined, undefined, false], [
      { k: 'service.name', op: '=', v: ['checkout'] },
    ]],
  ];
  it.each(rootCases)('%s', (_name, args, want) => {
    expect(heatmapFilters(...args)).toEqual(want);
  });

  // env(a), v0.9.1041 — the global Topbar picker as a deployment.environment
  // conjunct (→ deploy_env column), so the distribution narrows with the RED
  // charts above it. Composes after every other conjunct; empty = no-op.
  const envCases: Array<[
    name: string,
    args: [string, string?, string?, boolean?, string?],
    want: { k: string; op: string; v: string[] }[],
  ]> = [
    ['env adds deployment.environment conjunct', ['checkout', undefined, undefined, false, 'uat'], [
      { k: 'service.name', op: '=', v: ['checkout'] },
      { k: 'deployment.environment', op: '=', v: ['uat'] },
    ]],
    ['env composes with rootOnly + operation', ['checkout', undefined, 'GET /cart', true, 'prep'], [
      { k: 'service.name', op: '=', v: ['checkout'] },
      { k: 'kind', op: 'IN', v: ['server', 'consumer'] },
      { k: 'name', op: '=', v: ['GET /cart'] },
      { k: 'deployment.environment', op: '=', v: ['prep'] },
    ]],
    ['empty env stays deploy_env-free', ['checkout', undefined, undefined, false, ''], [
      { k: 'service.name', op: '=', v: ['checkout'] },
    ]],
  ];
  it.each(envCases)('%s', (_name, args, want) => {
    expect(heatmapFilters(...args)).toEqual(want);
  });

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
