import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';

// v0.10.656 — operatör: "/traces histogramında süre çizgisi üzerinde de kayan
// nokta olsun, diğer grafiklerdeki gibi". Nokta YOKTU çünkü TimeChart cursor
// ayarı `points: { show: true }` yazıyordu: uPlot `points.show`u fnOrSelf'ten
// geçirir ve dönen değerin HTMLElement olmasını bekler; `true` element değil
// → nokta hiç oluşturulmaz (CorePanel.tsx'in kendi notu, v0.9.704 civarı).
// Bu pin tuzağın geri gelmesini engeller; boyut CorePanel ile aynı.

describe('TimeChart imleç noktası (uPlot show tuzağı)', () => {
  const src = readFileSync(new URL('./TimeChart.tsx', import.meta.url), 'utf8');
  it('cursor.points `show: true` taşımaz (noktaları kapatır)', () => {
    expect(src).not.toMatch(/points:\s*\{\s*show:\s*true/);
  });
  it('nokta boyutu CorePanel ile aynı (10 px, 2 px kenar)', () => {
    expect(src).toContain('points: { size: 10, width: 2 }');
  });
});

describe('VolumeChart eksen biçimlendiricisi süreyle birlikte taşındı (v0.10.656)', () => {
  const src = readFileSync(new URL('../traces/VolumeChart.tsx', import.meta.url), 'utf8');
  it('fmtLeft = süre biçimlendiricisi; fmtRight verilmez (sayı kısaltması)', () => {
    expect(src).toContain('fmtLeft={fmtVolumeDuration}');
    expect(src).not.toContain('fmtRight={fmtVolumeDuration}');
  });
});
