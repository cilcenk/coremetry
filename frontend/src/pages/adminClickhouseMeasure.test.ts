import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

// v0.10.683 — /admin/clickhouse ölçüm paneli kaynak pini (mimari denetim 646
// öneri 1/2/8): panel mount, istemci ucu, query_log dürüstlüğü ("kapalı"
// çipi — sessiz sıfır değil), BatchSize çipi, parça tablosu useDataTable.
const page = readFileSync(resolve(__dirname, 'AdminClickhouse.tsx'), 'utf8');
const api = readFileSync(resolve(__dirname, '../lib/api.ts'), 'utf8');

describe('AdminClickhouse ölçüm paneli (v0.10.683)', () => {
  it('panel mount + istemci ucu', () => {
    expect(page).toContain('<MeasurePanel />');
    expect(page).toContain("queryFn: () => api.chMeasure()");
    expect(api).toContain("chMeasure: () => get<CHMeasureResponse>('/api/admin/clickhouse/measure')");
  });
  it('query_log kapalıysa söyler; BatchSize çipi; parça tablosu useDataTable', () => {
    expect(page).toContain('query_log kapalı — insert boyutu ölçülemiyor');
    expect(page).toContain('BatchSize {fmtNum(data.batchSize)} satır');
    expect(page).toContain("storageKey: 'ch-measure-parts'");
  });
  it('yoklama ≥ 10 s (CLAUDE.md)', () => {
    const i = page.indexOf("queryKey: ['ch-measure']");
    expect(page.slice(i, i + 200)).toContain('refetchInterval: 30_000');
  });
});
