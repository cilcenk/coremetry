import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { groupLeaves } from '@/lib/urlState';
import { stripScope } from '@/components/traces/volumeSeries';
import type { FilterGroup } from '@/lib/types';

// v0.10.655 — operatör (prod): "filtreli sorguda 34 trace çıkıyor ama
// histogram milyon gösteriyor". /traces şeridi metric-batch'e biner; o yüzey
// filterGroup bilmiyordu ve sayfa gruplu kipte düz filtreleri de bilerek
// göndermiyordu → grafik tüm evren, tablo grup. Sözleşme: gruplu kipte
// filterGroup şeride gider; kind kararı grubun yapraklarını da görür.

const g: FilterGroup = {
  join: 'AND',
  filters: [{ k: 'db.system', op: '=', v: ['oracle'] }, { k: 'db.name', op: '=', v: ['orders'] }],
  groups: [{ join: 'OR', filters: [
    { k: 'peer.service', op: '=', v: ['ora-1'] },
    { k: 'server.address', op: '=', v: ['ora-1'] },
  ] }],
};

describe('groupLeaves', () => {
  it('kök + iç grup yaprakları düz listede (OR anlamı taşınmaz, yalnız anahtarlar)', () => {
    expect(groupLeaves(g).map(f => f.k)).toEqual(['db.system', 'db.name', 'peer.service', 'server.address']);
  });
  it('null / boş grup → []', () => {
    expect(groupLeaves(null)).toEqual([]);
    expect(groupLeaves({ join: 'AND', filters: [] })).toEqual([]);
  });
  it('db.* yaprağı olan grup şerit kapsamını "spans" yapar (giriş span kısıtı histogramı boşaltırdı, v0.10.323)', () => {
    expect(stripScope(groupLeaves(g), '')).toBe('spans');
    expect(stripScope([{ k: 'deployment.environment' }], '')).toBe('entry');
  });
});

describe('BAĞLANMA (Traces.tsx + api.ts)', () => {
  const page = readFileSync(new URL('./Traces.tsx', import.meta.url), 'utf8');
  const api = readFileSync(new URL('../lib/api.ts', import.meta.url), 'utf8');
  it('gruplu kipte şerit isteği filterGroup taşır ve effect advGroupParam\'a bağlı', () => {
    expect(page).toContain("filterGroup: grouped ? (advGroupParam || undefined) : undefined");
    expect(page).toContain('filterGroup: common.filterGroup');
    const i = page.indexOf('api.spanMetricBatch({');
    const deps = page.slice(i, page.indexOf(']);', i));
    expect(deps).toContain('advGroupParam');
  });
  it('kind kararı grubun yapraklarını görür', () => {
    expect(page).toContain('groupLeaves(grouped ? advGroup : null)');
  });
  it('istemci gövdeye filterGroup geçirir', () => {
    expect(api).toContain('filterGroup?: string;');
    expect(api).toContain('filterGroup: body.filterGroup');
  });
});
