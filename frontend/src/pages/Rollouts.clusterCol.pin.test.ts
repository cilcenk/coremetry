// Rollouts.clusterCol.pin.test.ts — v0.10.565 (operatör-raporlu, prod
// ekran görüntüsü): Rollouts listesinde cluster adı Workload hücresinin
// soluk ekindeydi ve kırpılıyordu. Sözleşme: cluster KENDİ kolonunda,
// Workload'dan hemen sonra; workload eki yalnız namespace taşır.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const src = readFileSync(resolve(__dirname, 'Rollouts.tsx'), 'utf8');

describe('Rollouts cluster kolonu', () => {
  it('COLS: cluster, workload ile kind arasında', () => {
    const w = src.indexOf("{ id: 'workload', label: 'Workload'");
    const c = src.indexOf("{ id: 'cluster', label: 'Cluster'");
    const k = src.indexOf("{ id: 'kind', label: 'Tür'");
    expect(w).toBeGreaterThan(0);
    expect(c).toBeGreaterThan(w);
    expect(k).toBeGreaterThan(c);
  });
  it('hücre: çözülmüş ad + title, ellipsis', () => {
    expect(src).toContain('<td title={cname} style={{ overflow: \'hidden\', textOverflow: \'ellipsis\', whiteSpace: \'nowrap\' }}>{cname}</td>');
  });
  it('workload eki artık cluster TEKRARLAMAZ (kırpılma kaynağı)', () => {
    expect(src).toContain('<span className="field-hint"> · {r.namespace}</span>');
    expect(src).not.toContain('· {r.namespace} · {cname}');
  });
});
