import { describe, it, expect } from 'vitest';
import { fitColumnWidths, type FitColumnInput } from './dataTable';

// v0.9.1030 regresyonu — operatör bildirimi: /inbox'ta Assignee tarafında
// tablo "bozuluyor", sayfa iframe gibi iç-kaydırmalı görünüyor.
//
// Kök: table-layout:fixed bir tablo, kolon genişliklerinin TOPLAMI
// width:100%'ü aşarsa büyür; is-fit kap masaüstünde overflow:visible
// (D2.1) olduğundan taşma #content'e çıkıp SAYFAYI yatay kaydırıyordu.
// Geniş monitörde sürüklenip px olarak kalıcılaşan düzen, dar laptopta
// garanti taşmaydı (v0.9.660 Users vakasının yapısal hâli).
//
// Sözleşme: kaydırmayan kapta beyan+kalıcı genişlikler kabı AŞAMAZ —
// fitColumnWidths oransal küçültür (minWidth tabanlı, taban kilitlemeli),
// sığan küme için null döner (davranış aynen kalır).

const c = (id: string, px: number | null, min = 48): FitColumnInput => ({ id, px, min });

describe('fitColumnWidths (v0.9.1030)', () => {
  it('sığan küme → null (beyan genişlikler aynen kalır)', () => {
    expect(fitColumnWidths([c('a', 200), c('b', 300)], 34, 600)).toBeNull();
    expect(fitColumnWidths([c('a', 200), c('b', 366)], 34, 600)).toBeNull(); // tam sınır
  });

  it('ölçüm yok (containerPx ≤ 0) → null — fail-open', () => {
    expect(fitColumnWidths([c('a', 5000)], 0, 0)).toBeNull();
    expect(fitColumnWidths([c('a', 5000)], 0, -1)).toBeNull();
  });

  it('taşan küme oransal küçülür ve toplam kaba sığar', () => {
    // Inbox şekli: 34px checkbox + sabit kolonlar geniş monitör düzeninde.
    const out = fitColumnWidths([c('prio', 160), c('svc', 380), c('assignee', 660)], 34, 634)!;
    expect(out).not.toBeNull();
    const total = 34 + out.prio + out.svc + out.assignee;
    expect(total).toBeLessThanOrEqual(634);
    // Oranlar korunur (floor toleransı ±1px).
    expect(out.svc / out.prio).toBeCloseTo(380 / 160, 1);
    expect(out.assignee / out.svc).toBeCloseTo(660 / 380, 1);
  });

  it('minWidth tabanı kilitlenir, kalan pay kilitsizlere yeniden oranlanır', () => {
    // b'nin oransal payı tabanın altına düşer → b=min'e kilit, a kalanı alır.
    const out = fitColumnWidths([c('a', 800, 48), c('b', 100, 90)], 0, 450)!;
    expect(out.b).toBe(90);
    expect(out.a).toBeLessThanOrEqual(360);
    expect(out.a).toBeGreaterThan(300); // 800→~360: kilitsiz pay tek başına a'ya
    expect(out.a + out.b).toBeLessThanOrEqual(450);
  });

  it('tabanlar bile sığmıyorsa herkes tabanda (sınırlı taşma)', () => {
    const out = fitColumnWidths([c('a', 500, 120), c('b', 500, 120)], 0, 200)!;
    expect(out).toEqual({ a: 120, b: 120 });
  });

  it("flex kolon ('auto', px:null) çıktıya girmez ama min payını rezerve eder", () => {
    // avail = 600 - 34 - 100(flex min) = 466 < 500 → küçültme tetiklenir.
    const out = fitColumnWidths([c('fixed', 500), c('detail', null, 100)], 34, 600)!;
    expect(out).not.toBeNull();
    expect(out.detail).toBeUndefined();
    expect(out.fixed).toBeLessThanOrEqual(466);
    // Aynı küme flex payı olmasa sığardı:
    expect(fitColumnWidths([c('fixed', 500)], 34, 600)).toBeNull();
  });

  it('genişlikler tamsayı (colgroup px değerleri)', () => {
    const out = fitColumnWidths([c('a', 333), c('b', 334)], 0, 500)!;
    for (const v of Object.values(out)) expect(Number.isInteger(v)).toBe(true);
  });
});
