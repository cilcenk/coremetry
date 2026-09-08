// cosreChart.compare.pin.test.ts — v0.10.547 kaynak pini: source=metric → /metric-red
// (VM); compare → aynı kaynaktan kaydırılmış pencere, kesikli 'muted' seri; kırılım
// karşılaştırmayla birlikte istenmez.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const src = readFileSync(resolve(__dirname, 'CosreChart.tsx'), 'utf8');

describe('CosreChart compare/source', () => {
  it('metric kaynağı + kesikli karşılaştırma serisi', () => {
    expect(src).toContain("api.serviceMetricRED(spec.service, from, to, 300)");
    expect(src).toContain("fetchWindow(w.from - sh, w.to - sh)");
    expect(src).toContain("name: compareLabelTR(shiftS), role: 'muted', dashed: true");
    expect(src).toContain("groupBy && !shiftS ? { groupBy: [groupBy] } : {}");
    expect(src).toContain("enabled: !!spec.service && shiftS > 0");
  });
});
