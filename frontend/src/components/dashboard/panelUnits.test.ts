import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

// v0.9.787 — birim, panelin MARKINA göre kaybolmamalı.
//
// PanelRenderer aynı veriyi iki motora dağıtıyor. cfg.unit çizgi dalına
// geçiriliyordu ama spanmetric panelinin DashboardViz dalına
// geçirilmiyordu — aynı panelin çizgi hali "142 ms", bar hali çıplak
// "142" yazıyordu. Birim bileşene ulaşmadığında hem y-ekseni etiketleri
// hem hover okuması sessizce birimsiz kalır (her iki motor da unit'i
// fmtSmart'a veriyor; prop yoksa '' düşer).
//
// Kapı TEK satırı değil SINIFI korur: grafik çizen HER çağrı yeri unit
// taşımalı. Yeni bir çağrı yeri eklendiğinde aynı hata sessizce geri
// gelemesin — bu dosyanın var olma sebebi zaten bir call-site'ın
// kardeşinden sapmasıydı.
//
// v0.9.796 → v0.9.808 → v0.9.844 — motor dağılımı ÜÇ KEZ değişti, kapı her
// seferinde dürüstçe güncellendi. Bugünkü dağılım TEK hat ve artık
// gerçekten tek: beş markın BEŞİ de → DashChart → CorePanelMulti, üç panel
// tipinden de çağrılır. Eski SVG motorunun (DashboardViz) kaçış dalı
// v0.9.844'te söküldü, dosyası silindi — o hattın birim kapıları da bu
// yüzden buradan kalktı (silinmiş bir çağrı yerini taramak yeşil yalan
// üretir). Sökümün kendi kapısı panelViz.test.ts'te.
describe('PanelRenderer — grafik çağrıları birim taşır', () => {
  const src = readFileSync(
    resolve(__dirname, './PanelRenderer.tsx'), 'utf8',
  ).replace(/\/\/.*$/gm, '');

  // Kendi kendini doğrulayan kapılar: çağrı yeri BULAMAZSA da kızarır
  // (bileşen yeniden adlandırılıp kapı sessizce boşa düşmesin).
  const dashSites = src.match(/<DashChart\b[^>]*\/>/g) ?? [];
  const coreSite  = src.match(/<DashCorePanelLazy\b[\s\S]*?\/>/g) ?? [];

  // ── Tek hat (beş mark) ──────────────────────────────────────────────
  it('üç panel tipi de DashChart çağırır (metric + spanmetric + promql)', () => {
    expect(dashSites.length).toBeGreaterThanOrEqual(3);
  });

  it('HER DashChart çağrısı unit prop geçirir', () => {
    const naked = dashSites.filter(s => !/\bunit=/.test(s));
    expect(naked, 'unit geçirmeyen DashChart çağrısı').toEqual([]);
  });

  // Mark da taşınmalı: viz geçirilmeyen bir DashChart sessizce 'line'
  // varsayılanına düşer, yani operatörün bar seçtiği panel çizgi çizer —
  // v0.9.790'da metric panelinde birebir bu olmuştu.
  it('HER DashChart çağrısı viz prop geçirir', () => {
    const naked = dashSites.filter(s => !/\bviz=\{viz\}/.test(s));
    expect(naked, 'viz geçirmeyen DashChart çağrısı').toEqual([]);
  });

  // ── Birimin KAYNAĞI ─────────────────────────────────────────────────
  // Panel seviyesindeki çağrılar cfg.unit okur; DashChart kendi unit
  // prop'unu motora ilerletir. İkisi de meşru; elle yazılmış bir sabit
  // ya da '' değil.
  it('birim kaynağı cfg.unit ya da ilerletilen unit — elle yazılmış sabit değil', () => {
    for (const s of [...dashSites, ...coreSite]) {
      expect(s, s).toMatch(/unit=\{(cfg\.unit|unit)\}/);
    }
  });

  // v2 motoruna giden tek nokta: birim de mark da orada.
  it('CorePanelMulti çağrısı hem unit hem viz taşır', () => {
    expect(coreSite.length).toBe(1);
    expect(coreSite[0]).toMatch(/unit=\{unit\}/);
    expect(coreSite[0]).toMatch(/viz=\{toCoreViz\(viz\)\}/);
  });
});
