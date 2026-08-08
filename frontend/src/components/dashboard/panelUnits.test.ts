import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

// v0.9.787 — birim, panelin MARKINA göre kaybolmamalı.
//
// PanelRenderer aynı veriyi iki motora dağıtıyor: 'line' uPlot tabanlı
// DashLineChart'a, diğer dört mark SVG tabanlı DashboardViz'e. cfg.unit
// çizgi dalına geçiriliyordu ama spanmetric panelinin DashboardViz
// dalına geçirilmiyordu — aynı panelin çizgi hali "142 ms", bar hali
// çıplak "142" yazıyordu. Birim bileşene ulaşmadığında hem y-ekseni
// etiketleri hem hover okuması sessizce birimsiz kalır (DashboardViz
// unit'i fmtSmart'a veriyor; prop yoksa '' düşer).
//
// Kapı TEK satırı değil SINIFI korur: DashboardViz'in HER çağrı yeri
// unit taşımalı. Üçüncü bir çağrı yeri eklendiğinde aynı hata sessizce
// geri gelemesin — bu dosyanın var olma sebebi zaten bir call-site'ın
// kardeşinden sapmasıydı.
describe('PanelRenderer — DashboardViz birim taşır', () => {
  const src = readFileSync(
    resolve(__dirname, './PanelRenderer.tsx'), 'utf8',
  ).replace(/\/\/.*$/gm, '');

  // Kendi kendini doğrulayan kapı: çağrı yeri BULAMAZSA da kızarır
  // (bileşen yeniden adlandırılıp kapı sessizce boşa düşmesin).
  const sites = src.match(/<DashboardViz\b[^>]*\/>/g) ?? [];

  // v0.9.790 — üçüncü çağrı yeri geldi (metric paneli de viz'e göre
  // dallanıyor). Alt sınır onunla birlikte yükseldi: kapı "en az iki" de
  // kalsaydı, metric dalının dispatch'i sessizce geri alındığında bu dosya
  // hâlâ yeşil kalırdı.
  it('en az üç çağrı yeri var (promql + spanmetric + metric)', () => {
    expect(sites.length).toBeGreaterThanOrEqual(3);
  });

  it('HER çağrı yeri unit prop geçirir', () => {
    const naked = sites.filter(s => !/\bunit=/.test(s));
    expect(naked, 'unit geçirmeyen DashboardViz çağrısı').toEqual([]);
  });

  it('birim kaynağı cfg.unit — elle yazılmış sabit değil', () => {
    for (const s of sites) {
      expect(s).toMatch(/unit=\{cfg\.unit\}/);
    }
  });
});
