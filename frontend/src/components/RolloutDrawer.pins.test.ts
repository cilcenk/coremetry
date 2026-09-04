import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

// v0.10.338 — Operator-reported (rollout çekmecesi): (1) revizyon ve imaj
// "…" ile kırpılıyordu — globals.css'in genel `tbody td { nowrap; max-width:
// 320px; ellipsis }` kuralı; (2) aynı workload birden çok cluster'a çıkıyor
// ama çekmece cluster'ı söylemiyordu. Kaynak pini: kimlik hücreleri `.td-full`
// taşır ve o sınıf gerçekten sarar; çekmece küme adını çözer ve gösterir.

describe('RolloutDrawer (v0.10.338)', () => {
  const src = readFileSync(resolve(__dirname, 'RolloutDrawer.tsx'), 'utf8');
  const css = readFileSync(resolve(__dirname, '../styles/globals.css'), 'utf8');

  it('revizyon ve imaj hücreleri sarar (.td-full), kırpılmaz', () => {
    for (const label of ['revizyon', 'imaj', 'küme']) {
      const re = new RegExp(`<th[^>]*>${label}</th><td className="td-full`);
      expect(src, `${label} hücresi .td-full değil`).toMatch(re);
    }
    const rule = css.match(/tbody td\.td-full \{([^}]*)\}/);
    expect(rule, '.td-full kuralı globals.css\'te yok').not.toBeNull();
    expect(rule![1]).toMatch(/white-space:\s*normal/);
    expect(rule![1]).toMatch(/max-width:\s*none/);
    expect(rule![1]).toMatch(/overflow-wrap:\s*anywhere/);
  });

  it('tam kimlik kopyalanabilir', () => {
    expect(src).toMatch(/<CopyButton value=\{r\.revision\}/);
    expect(src).toMatch(/<CopyButton value=\{curImage\}/);
  });

  it('çekmece küme adını çözer ve başlıkta + Geçiş tablosunda gösterir', () => {
    expect(src).toContain('useEntityClusters()');
    expect(src).toContain('rolloutPlaceLabel(r, clusterName)');
    expect(src).toMatch(/\{clusterName \|\| id\.clusterId\} · \{id\.namespace\}/);
  });
});
