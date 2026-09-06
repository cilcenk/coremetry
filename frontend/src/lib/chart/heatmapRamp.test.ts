import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { DEFAULT_RAMP_TOKENS, densityRamp, heatmapTimeLabel, hexToRgb } from './heatmapRamp';

// v0.10.505 (D8) — heatmap rampası tema tokenlarından; çok günlü eksende tarih.
describe('heatmapRamp', () => {
  it('hexToRgb kısa/uzun/geçersiz', () => {
    expect(hexToRgb('#388bfd')).toEqual([56, 139, 253]);
    expect(hexToRgb('fff')).toEqual([255, 255, 255]);
    expect(hexToRgb('var(--accent)')).toBeNull();
  });
  it('rampa tokenlardan türer, alfa tek yönlü artar, 0 şeffaf', () => {
    const r = densityRamp({ accent: '#0969da', warn: '#9a6700', err: '#cf222e' });
    expect(r).toHaveLength(6);
    expect(r[0]).toBe('rgba(0,0,0,0)');
    expect(r[1]).toBe('rgba(9,105,218,0.18)');
    expect(r[4]).toBe('rgba(154,103,0,0.8)');
    expect(r[5]).toBe('rgba(207,34,46,0.9)');
    const alphas = r.slice(1).map(s => Number(s.slice(s.lastIndexOf(',') + 1, -1)));
    for (let i = 1; i < alphas.length; i++) expect(alphas[i]).toBeGreaterThan(alphas[i - 1]);
  });
  it('çözülemeyen token koyu varsayılana düşer, siyaha değil', () => {
    const r = densityRamp({ accent: '', warn: '', err: '' });
    expect(r[1]).toBe(densityRamp(DEFAULT_RAMP_TOKENS)[1]);
  });
  it('etiket: 24 saat altı saat, üstü tarih + saat', () => {
    const ns = new Date(2026, 8, 6, 14, 5).getTime() * 1e6;
    expect(heatmapTimeLabel(ns, 6 * 3600 * 1e9)).toBe('14:05');
    expect(heatmapTimeLabel(ns, 3 * 24 * 3600 * 1e9)).toBe('06.09 14:05');
  });
});

// Kaynak pini: heatmap tema değişiminde yeniden çizer ve sarı sabitler kalmaz.
describe('LatencyHeatmap tema (v0.10.505)', () => {
  const src = readFileSync(resolve(__dirname, '../../components/LatencyHeatmap.tsx'), 'utf8');
  it('useThemeTick efekt bağımlılığında, rampa densityRamp ile', () => {
    expect(src).toContain('useThemeTick()');
    expect(src).toContain('densityRamp(');
    expect(src).toContain('themeTick]');
  });
  it('ham sarı/mavi sabitler yok', () => {
    expect(src).not.toMatch(/rgba\(250,204,21/);
    expect(src).not.toContain('#facc15');
    expect(src).not.toContain('rgba(63,140,253');
    expect(src).not.toContain('rgba(18,184,255');
  });
});
