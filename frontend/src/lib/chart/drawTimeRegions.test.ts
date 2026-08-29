// drawTimeRegions.test.ts — v0.10.168: çizim fonksiyonu sahte uPlot/canvas ile.
// Sözleşme: etiket SOLA hizalı x1+4'te yazılır (uPlot eksen çiziminden sızan
// textAlign='right' etkisiz), şerit+etiket şerit numarası kadar aşağıda, dolgu
// birleşik aralıkla tek kat, sn bölgeleri xUnit=1000 ile ms eksenine oturur.
import { describe, it, expect } from 'vitest';
import type uPlot from 'uplot';
import { drawTimeRegions } from './overlays';

// Renkler LİTERAL: resolveVar yalnız `var(--x)` için DOM'a gider; node ortamında getComputedStyle yok.
const C = '#d29922';

type Call = { fn: string; args: unknown[]; textAlign: string; alpha: number };

function fakeU(xMinMs: number, xMaxMs: number) {
  const calls: Call[] = [];
  const bbox = { left: 100, top: 20, width: 1000, height: 300 };
  const ctx = {
    textAlign: 'right', textBaseline: 'middle', font: '', fillStyle: '', globalAlpha: 1,
    save() { calls.push({ fn: 'save', args: [], textAlign: this.textAlign, alpha: this.globalAlpha }); },
    restore() { calls.push({ fn: 'restore', args: [], textAlign: this.textAlign, alpha: this.globalAlpha }); },
    fillRect(...args: unknown[]) { calls.push({ fn: 'fillRect', args, textAlign: this.textAlign, alpha: this.globalAlpha }); },
    fillText(...args: unknown[]) { calls.push({ fn: 'fillText', args, textAlign: this.textAlign, alpha: this.globalAlpha }); },
    measureText(s: string) { return { width: s.length * 6 }; },
  };
  const u = {
    ctx, bbox,
    scales: { x: { min: xMinMs, max: xMaxMs } },
    valToPos: (v: number) => bbox.left + ((v - xMinMs) / (xMaxMs - xMinMs)) * bbox.width,
  } as unknown as uPlot;
  return { u, calls };
}

describe('drawTimeRegions (v0.10.168)', () => {
  const xMin = 1_700_000_000_000, xMax = 1_700_003_600_000; // 1 saat, ms
  it('etiket sola hizalı, x1+4\'te; sızan textAlign=right etkisiz', () => {
    const { u, calls } = fakeU(xMin, xMax);
    drawTimeRegions(u, [{ fromSec: 1_699_990_000, toSec: 1_700_010_000, label: 'trace_op ×175', color: C }], 1000);
    const t = calls.filter(c => c.fn === 'fillText');
    expect(t.length).toBe(1);
    expect(t[0].textAlign).toBe('left');
    expect(t[0].args[1]).toBe(100 + 4); // x1 = bbox.left (pencereye kırpılmış) + 4
  });
  it('üç tam-pencere bölge: dolgu TEK fillRect (tam yükseklik), şerit+etiket 3 satır', () => {
    const { u, calls } = fakeU(xMin, xMax);
    const rg = [0, 1, 2].map(i => ({ fromSec: 1_699_000_000 + i, toSec: 1_700_010_000, label: `a${i}`, color: C }));
    drawTimeRegions(u, rg, 1000);
    const fills = calls.filter(c => c.fn === 'fillRect' && c.args[3] === 300);
    expect(fills.length).toBe(1);
    const strips = calls.filter(c => c.fn === 'fillRect' && c.args[3] === 3);
    expect(strips.map(s => s.args[1])).toEqual([20, 32, 44]);
    const ys = calls.filter(c => c.fn === 'fillText').map(c => c.args[2]);
    expect(ys).toEqual([33, 45, 57]);
  });
  it('pencere dışı bölge hiç çizilmez; xUnit=1 (saniye motoru) aynı davranır', () => {
    const { u, calls } = fakeU(xMin, xMax);
    drawTimeRegions(u, [{ fromSec: 1_600_000_000, toSec: 1_600_000_100, label: 'eski', color: C }], 1000);
    expect(calls.filter(c => c.fn !== 'save' && c.fn !== 'restore').length).toBe(0);
    const s = fakeU(1_700_000_000, 1_700_003_600);
    drawTimeRegions(s.u, [{ fromSec: 1_700_000_100, toSec: 1_700_000_200, label: 'x', color: C }]);
    expect(s.calls.filter(c => c.fn === 'fillText').length).toBe(1);
  });
});
