import { describe, it, expect } from 'vitest';
import { rankPaletteResults } from './paletteScore';

// v0.10.126 — operatör: "Global search'te endpoint veya servisler en
// altta çıkıyor, en üstte çıkmalarını isterim". Sıra: servis → endpoint
// → sayfa; sayfa tekrarları (aynı rota) tekilleşir.
describe('rankPaletteResults', () => {
  const scored = [
    { kind: 'page', label: 'Problems', to: '/problems', score: 50 },
    { kind: 'page', label: 'Problems', to: '/problems', score: 500 },
    { kind: 'page', label: 'Incidents', to: '/incidents', score: 50 },
    { kind: 'service', label: 'card-web-api', to: '/services/card-web-api', score: 1000 },
    { kind: 'service', label: 'card-web-api-bff', to: '/services/card-web-api-bff', score: 500 },
  ];
  const endpoints = [
    { kind: 'endpoint', label: 'POST /BSAWEB/application/preApproval', to: '/endpoints?x=1', score: 0 },
    { kind: 'endpoint', label: 'POST /BSAWEB/mortgageApplication/preApproval', to: '/endpoints?x=2', score: 0 },
  ];
  it('servis → endpoint → sayfa; sayfa tekrarları rotaya göre tekilleşir', () => {
    const out = rankPaletteResults(scored, endpoints);
    expect(out.map(r => r.kind)).toEqual(['service', 'service', 'endpoint', 'endpoint', 'page', 'page']);
    expect(out[0].label).toBe('card-web-api');
    expect(out.filter(r => r.to === '/problems')).toHaveLength(1);
    expect(out.find(r => r.to === '/problems')!.score).toBe(500);
  });
  it('endpoint yokken ve sayfa yokken bozulmaz; tavan uygulanır', () => {
    expect(rankPaletteResults(scored.filter(r => r.kind === 'service'), [])).toHaveLength(2);
    expect(rankPaletteResults([], endpoints)).toHaveLength(2);
    const many = Array.from({ length: 80 }, (_, i) => ({ kind: 'page', label: `p${i}`, to: `/p${i}`, score: 1 }));
    expect(rankPaletteResults(many, [])).toHaveLength(50);
  });
  it('bilinmeyen tür (trace/action) sayfaların önünde kalır', () => {
    const out = rankPaletteResults([{ kind: 'page', label: 'Traces', to: '/traces', score: 200 }, { kind: 'trace', label: 'abc', score: 999 }], []);
    expect(out.map(r => r.kind)).toEqual(['trace', 'page']);
  });
});
