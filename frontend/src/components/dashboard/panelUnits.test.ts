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
// v0.9.796 → v0.9.808 — motor dağılımı İKİ KEZ değişti, kapı her ikisinde
// de dürüstçe güncellendi. Bugünkü dağılım TEK hat:
//   • beş markın BEŞİ de → DashChart (v2: CorePanelMulti) — üç panel
//     tipinden de çağrılır.
//   • DashboardViz yalnız DashChart'ın ?chartsV2=0 fallback dalında.
// Kapı yine iki hattı birden tarıyor: kaçış dalı da birim taşımalı, yoksa
// v2 kapatan operatör birimsiz eksen görür.
describe('PanelRenderer — grafik çağrıları birim taşır', () => {
  const src = readFileSync(
    resolve(__dirname, './PanelRenderer.tsx'), 'utf8',
  ).replace(/\/\/.*$/gm, '');

  // Kendi kendini doğrulayan kapılar: çağrı yeri BULAMAZSA da kızarır
  // (bileşen yeniden adlandırılıp kapı sessizce boşa düşmesin).
  const vizSites  = src.match(/<DashboardViz\b[^>]*\/>/g) ?? [];
  const dashSites = src.match(/<DashChart\b[^>]*\/>/g) ?? [];
  const coreSite  = src.match(/<DashCorePanelLazy\b[\s\S]*?\/>/g) ?? [];

  // ── Eski SVG motoru (yalnız ?chartsV2=0 kaçışı, v0.9.808) ───────────
  //
  // TEK çağrı yeri kaldı: DashChart'ın v2 kapalı fallback'i. Alt sınır
  // yine SINIFI koruyor — kaçış dalı silinirse kapı kızarır (tek adımlık
  // geri dönüş doktrini sessizce kaybolmasın).
  it('DashboardViz kaçış dalı duruyor (v2-kapalı yol)', () => {
    expect(vizSites.length).toBeGreaterThanOrEqual(1);
  });

  it('HER DashboardViz çağrısı unit prop geçirir', () => {
    const naked = vizSites.filter(s => !/\bunit=/.test(s));
    expect(naked, 'unit geçirmeyen DashboardViz çağrısı').toEqual([]);
  });

  // ── Yeni v2 hattı (line + bar + area) ───────────────────────────────
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
  // Panel seviyesindeki çağrılar cfg.unit okur; DashChart içindeki iki
  // dal kendi unit prop'unu ilerletir. İkisi de meşru; elle yazılmış bir
  // sabit ya da '' değil.
  it('birim kaynağı cfg.unit ya da ilerletilen unit — elle yazılmış sabit değil', () => {
    for (const s of [...vizSites, ...dashSites, ...coreSite]) {
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
