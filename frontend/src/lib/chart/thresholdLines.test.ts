// thresholdLines.test.ts — v0.10.314: tek kapı sözleşmesi + dashboard
// çizgi panellerinin (metric/promql/spanmetric) bant eşiğini bu kapıdan
// geçirdiğinin kaynak pini.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { severityThresholdLines, bandThresholdLines } from './thresholdLines';

describe('severityThresholdLines', () => {
  it('err → var(--err), varsayılan warn; etiket taşınır; boş → []', () => {
    expect(severityThresholdLines(undefined)).toEqual([]);
    expect(severityThresholdLines([{ value: 500, label: 'SLO', severity: 'err' }, { value: 300 }])).toEqual([
      { value: 500, label: 'SLO', color: 'var(--err)' },
      { value: 300, label: undefined, color: 'var(--warn)' },
    ]);
  });
});

describe('bandThresholdLines', () => {
  it('green taban çizgisiz; amber warn, red err; değere göre sıralı; NaN düşer', () => {
    expect(bandThresholdLines(undefined)).toEqual([]);
    expect(bandThresholdLines([
      { value: 90, color: 'red' }, { value: 0, color: 'green' }, { value: 70, color: 'amber' }, { value: NaN, color: 'red' },
    ])).toEqual([
      { value: 70, label: 'amber ≥', color: 'var(--warn)' },
      { value: 90, label: 'red ≥', color: 'var(--err)' },
    ]);
  });
  it('hex yok — yalnız token', () => {
    for (const l of bandThresholdLines([{ value: 1, color: 'amber' }, { value: 2, color: 'red' }])) {
      expect(l.color).toMatch(/^var\(--/);
    }
  });
});

describe('kaynak pini — tek kapı', () => {
  const root = resolve(__dirname, '../../');
  const renderer = readFileSync(resolve(root, 'components/dashboard/PanelRenderer.tsx'), 'utf8');
  const mlc = readFileSync(resolve(root, 'components/MultiLineChart.tsx'), 'utf8');
  it('DashChart üç sitede bands={cfg.thresholds} geçirir ve bandThresholdLines kullanır', () => {
    expect(renderer.split('bands={cfg.thresholds}').length - 1).toBe(3);
    expect(renderer).toContain('bandThresholdLines(');
  });
  it('MultiLineChart severity eşiklerini kapıdan geçirir; yerel var(--err) eşlemesi yok', () => {
    expect(mlc).toContain('severityThresholdLines(');
    expect(mlc).not.toMatch(/severity === 'err' \? 'var\(--err\)'/);
  });
});
