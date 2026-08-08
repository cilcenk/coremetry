import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync } from 'node:fs';
import { resolve } from 'node:path';
import { seriesRoleColor } from '@/lib/chart/seriesRole';
import { visibleRangeStats } from '@/lib/chart/visibleStats';
import { seriesColor } from '@/lib/chartFmt';

// CorePanel sözleşme testleri (FAZ 2B). Bileşenin kendisi jsdom+canvas
// ister (uPlot 2D context) — o katman FAZ 2 self-review'da bilinçli borç.
// Burada korunanlar: saf çekirdekler + TEK-SARMALAYICI tekeli.

describe('seriesRoleColor', () => {
  it('semantik roller token döndürür — tema-canlı', () => {
    expect(seriesRoleColor('errors', 'error')).toBe('var(--err)');
    expect(seriesRoleColor('ok', 'success')).toBe('var(--ok)');
    expect(seriesRoleColor('other', 'muted')).toBe('var(--text3)');
  });

  it('data rolü mevcut deterministik palete düşer — YENİDEN yazılmadı', () => {
    expect(seriesRoleColor('payments', 'data')).toBe(seriesColor('payments'));
    expect(seriesRoleColor('payments')).toBe(seriesRoleColor('payments'));
  });

  it('rol etiketten TAHMİN EDİLMEZ: "error" adlı seri data rolünde palet alır', () => {
    // "error-budget-service" bir VERİ serisidir; kırmızıya zorlamak yanlış.
    expect(seriesRoleColor('error', 'data')).toBe(seriesColor('error'));
    expect(seriesRoleColor('error', 'data')).not.toBe('var(--err)');
  });
});

describe('visibleRangeStats', () => {
  const t = [10, 20, 30, 40, 50];
  const v = [1, null, 3, 4, 5];

  it('tam pencere = tüm dizi', () => {
    const s = visibleRangeStats(t, v, 0, 100);
    expect(s.count).toBe(4);
    expect(s.sum).toBe(13);
  });

  it('zoom penceresi kırpar — sınırlar DAHİL', () => {
    const s = visibleRangeStats(t, v, 20, 40);
    expect(s.count).toBe(2); // 20→null atlanır, 30 ve 40 girer
    expect(s.min).toBe(3);
    expect(s.max).toBe(4);
    expect(s.last).toBe(4);
  });

  it('null istatistiğe girmez — 0 sayılmaz', () => {
    const s = visibleRangeStats(t, [null, null, 2, null, null], 0, 100);
    expect(s.count).toBe(1);
    expect(s.mean).toBe(2); // null'lar 0 sayılsaydı mean 0.4 olurdu
  });

  it('pencere dışı → boş istatistik', () => {
    const s = visibleRangeStats(t, v, 60, 90);
    expect(s.count).toBe(0);
    expect(s.last).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// TEK-SARMALAYICI TEKELİ. dataFrame.test.ts'teki köprü tekelinin kardeşi:
// UPlotChart/UPlotConfigBuilder importu YALNIZ CorePanel.tsx'te yasal.
// Bir sayfa doğrudan import ederse dört-kopya-chart hastalığı @grafana/ui
// katmanında yeniden başlar (v0.9.97 engine.ts'in kapattığı borç).
// ---------------------------------------------------------------------------
// v0.9.704 — self-review'ın beş doğrulanmış kusuru için kaynak kapıları.
// Saf test CorePanel'i render EDEMEZ (uPlot canvas ister); kapılar
// düzeltmelerin kaynakta durduğunu çiviler — bugün altı kez işe yarayan
// desen (mutasyonla doğrulanmış).
describe('CorePanel self-review düzeltmeleri', () => {
  const src = readFileSync(
    resolve(__dirname, './CorePanel.tsx'), 'utf8',
  ).replace(/\/\/.*$/gm, '');

  it('🔴 onZoom ms→sn böler', () => {
    expect(src).toMatch(/posToVal\(u\.select\.left, 'x'\) \/ 1000/);
  });

  it('🟠 config bağımlılığında vis/onZoom kimliği YOK — overlaySig var', () => {
    // v0.9.721 — İLK eşleşme yeterli değildi: defaultHidden (v0.9.720)
    // ikinci bir names.join bağımlılığı ekledi ve kapı ONU yakalayıp
    // yanlış kızardı (v0.9.720 kırmızı testle push edildi — bu düzeltme
    // o ihlalin kapanışı). Artık TÜM diziler taranır: config dizisi
    // (overlaySig'li) VAR olmalı, HİÇBİRİ vis/onZoom kimliği taşımamalı.
    const deps = src.match(/\}, \[[^\]]*\]\);/g) ?? [];
    expect(deps.length).toBeGreaterThan(0);
    expect(deps.some(d => d.includes('overlaySig'))).toBe(true);
    for (const d of deps) {
      expect(d).not.toContain(' vis,');
      expect(d).not.toContain('onZoom,');
    }
  });

  it('🟠 görünürlük setSeries ile — config rebuild değil', () => {
    expect(src).toMatch(/setSeries\(i \+ 1, \{ show \}/);
  });

  it('🟠 legend kalıcılığı legendCollapseKey ailesinde', () => {
    expect(src).toMatch(/legendCollapseKey\(storageKey\)/);
    expect(src).toMatch(/setItem\(legendCollapseKey/);
  });

  // v0.9.711 — klavye erişimi (self-review WCAG bulgusu). Kapılar
  // davranışın kaynağını çiviler: satırlar fokuslanabilir + Enter/Space
  // yolları var + menü ESC/dış-tık kapanışı bağlı.
  it('♿ legend satırları klavyeden erişilir', () => {
    expect(src).toMatch(/tabIndex=\{0\}/);
    expect(src).toMatch(/onKeyDown/);
    expect(src).toMatch(/e\.key === 'Enter'/);
    expect(src).toMatch(/e\.key === ' '/);
  });

  it('♿ menü ESC + dış-tık ile kapanır', () => {
    expect(src).toMatch(/menuOpen\) return;/);
    expect(src).toMatch(/mousedown/);
  });

  it('doktrin: spanNulls connectNulls üzerinden, varsayılan sıkı', () => {
    expect(src).toMatch(/spanNulls: connectNulls \?\? false/);
  });
});

// ---------------------------------------------------------------------------
// v0.9.785 — bars markı + config bağımlılık bütünlüğü.
//
// Aynı 🟠 kimlik dersinin (v0.9.704) ÖTEKİ ucu: orada fazla bağımlılık
// config'i gereksiz yıkıyordu, burada EKSİK bağımlılık değişikliği yutuyor.
// bars↔line geçişi seri ADLARINI değiştirmediği için names.join() imzası
// sabit kalır — `viz` dizide olmasa panel sessizce çizgi kalırdı. Aynı tuzak
// dashed ve connectNulls için ZATEN canlıydı (bu sürümde kapandı).
// ---------------------------------------------------------------------------
describe('CorePanel bars markı (v0.9.785)', () => {
  const src = readFileSync(
    resolve(__dirname, './CorePanel.tsx'), 'utf8',
  ).replace(/\/\/.*$/gm, '');

  it('import @grafana/ui alias adlarından — @grafana/schema YASAK', () => {
    // GraphDrawStyle @grafana/ui'den DERLENMEZ (index.d.ts:305 onu
    // DrawStyle olarak yeniden dışa aktarır) ve @grafana/schema
    // package.json'da YOK — hayalet bağımlılık, prod build'de patlar.
    expect(src).toMatch(/DrawStyle, PointVisibility,?\s*\n?\s*\} from '@grafana\/ui'/);
    expect(src).not.toMatch(/@grafana\/schema/);
    expect(src).not.toMatch(/\bGraphDrawStyle\b/);
  });

  it('bars dalı: drawStyle + showPoints + genişlik TAVANI birlikte', () => {
    expect(src).toMatch(/drawStyle: DrawStyle\.Bars/);
    // showPoints geçilmezse uPlot çubukların üstüne nokta basar.
    expect(src).toMatch(/showPoints: PointVisibility\.Auto/);
    expect(src).toMatch(/barWidthFactor: 0\.6/);
    // v0.9.245 dersi: tavansız [0.85, Infinity] "barlar çok büyük".
    // (Çıplak /Infinity/ ARAMAYIN — legend penceresi xWin ?? [-Infinity,
    // Infinity] kullanıyor ve kapı ONU yakalayıp yanlış kızarır; v0.9.720
    // ile aynı aşırı-geniş-regex hatası.)
    expect(src).toMatch(/barMaxWidth: 40/);
    expect(src).not.toMatch(/barMaxWidth: Infinity/);
    expect(src).not.toMatch(/size: \[0\.85/);
  });

  it('line dalı korunur: viz varsayılan line, eski değerler yerinde', () => {
    expect(src).toMatch(/viz = 'line'/);
    expect(src).toMatch(/lineWidth: bars \? 1 : 1\.5/);
    // v0.9.788 — area/stacked dalları eklendi; line ve bars uçları
    // (12 / 35 + dashed ghost'un 0'ı) BAYT BAYT yerinde kalmalı.
    expect(src).toMatch(
      /fillOpacity: bars \? 35 : area \? 60 : stacked \? 28 : \(dashed\?\.\[i\] \? 0 : 12\)/);
  });

  it('🔴 config bağımlılığı viz + dashed + connectNulls TAŞIR', () => {
    const deps = src.match(/\}, \[[^\]]*\]\);/g) ?? [];
    const cfg = deps.filter(d => d.includes('overlaySig'));
    expect(cfg.length, 'config useMemo dizisi bulunamadı').toBe(1);
    expect(cfg[0]).toContain('viz');
    // dizi KİMLİĞİ değil İÇERİK imzası — inline [] her render'da yeni.
    expect(cfg[0]).toContain("dashed?.join(',')");
    expect(cfg[0]).toContain('connectNulls');
  });
});

// ---------------------------------------------------------------------------
// v0.9.788 — yığılmış alan + area.
//
// Buradaki kapıların hepsi TEK bir kusur sınıfını hedefler: türetilmiş
// (kümülatif) verinin ham veriyle karışması. Tooltip'in u.data'dan okuması,
// bant listesinin config imzasına katılmaması, exemplar'ın ham y ile
// kümülatif çizgi arasında asılı kalması — üçü de "aynı matris sanmak".
// ---------------------------------------------------------------------------
describe('CorePanel yığılmış alan (v0.9.788)', () => {
  const src = readFileSync(
    resolve(__dirname, './CorePanel.tsx'), 'utf8',
  ).replace(/\/\/.*$/gm, '');

  it('viz union DÖRT mark taşır', () => {
    expect(src).toMatch(/viz\?: 'line' \| 'bars' \| 'area' \| 'stacked'/);
  });

  it('🔴 HAM VERİ KANALI: kümülatif matris aligned\'a yazılmaz', () => {
    // drawData ayrı bir türetme; uPlot ONU alır, aligned ham kalır.
    expect(src).toMatch(/const drawData = useMemo\(/);
    expect(src).toMatch(/stacked \? stackData\(aligned\.data, hiddenIdx\) : aligned\.data/);
    expect(src).toMatch(/<UPlotChart data=\{drawData\}/);
    // CSV + lejant istatistikleri HÂLÂ ham matristen.
    expect(src).toMatch(/alignedToCsv\(aligned\.names, aligned\.data/);
  });

  it('🔴 tooltip HAM değeri ref\'ten okur — u.data\'dan DEĞİL', () => {
    expect(src).toMatch(/rawRef\.current = aligned\.data/);
    expect(src).toMatch(/rawRef\.current\[i \+ 1\] as \(number \| null\)\[\] \| undefined/);
    // Eski (yalan söyleyen) okuma geri gelmemeli.
    expect(src).not.toMatch(/u\.data\[i \+ 1\]/);
  });

  it('🟠 bant imzası overlaySig\'e katılır — poll tick\'i uPlot\'u yıkmasın', () => {
    expect(src).toMatch(/const overlaySig = JSON\.stringify\(\[[\s\S]*?stackedBands,?[\s\S]*?\]\)/);
    // stacked iken prop bands imzadan da düşer (sahte rebuild üretirdi).
    expect(src).toMatch(/stacked \? null : \(bands \?\? null\)/);
  });

  it('bant listesi saf çekirdekten; stacked iken prop bands YOK SAYILIR', () => {
    expect(src).toMatch(/stackBands\(aligned\.names\.length, hiddenIdx\)/);
    expect(src).toMatch(/if \(stacked\) \{[\s\S]*?b\.addBand\(\{ series: sb\.series \}\)/);
    // Fill VERİLMEZ: uPlot üst serinin kendi dolgusuna düşer.
    expect(src).not.toMatch(/addBand\(\{ series: sb\.series, fill/);
  });

  it('pxAlign stacked\'te kapalı (uPlot\'ta false ≡ 0) — dikiş yok', () => {
    expect(src).toMatch(/pxAlign: stacked \? false : undefined/);
  });

  it('stacked dolgusu DÜZ: gradient katman sınırını bulanıklaştırır', () => {
    expect(src).toMatch(
      /gradientMode: stacked \? GraphGradientMode\.None : GraphGradientMode\.Opacity/);
  });

  it('exemplar ◆ stacked\'te bastırılır — çizim VE tık isabeti birlikte', () => {
    expect(src).toMatch(/if \(!stacked && exemplarsRef\.current\?\.some/);
    expect(src).toMatch(/u && !stacked && exemplarClickRef\.current/);
  });

  it('🟠 gizleme yeniden hesaptır ama config bağımlılığı DEĞİL', () => {
    // hiddenIdx bir VERİ memo'su besler; ' vis,' pini (v0.9.704) yukarıdaki
    // testte zaten tüm dizilerde taranıyor — burada kaynağını çiviliyoruz.
    expect(src).toMatch(/const hiddenIdx = useMemo\(/);
    expect(src).toMatch(/\[stacked, aligned, hiddenIdx\]\)/);
  });
});

describe('CorePanel tekeli', () => {
  const SRC = resolve(__dirname, '../..');

  function* walk(dir: string): Generator<string> {
    for (const e of readdirSync(dir, { withFileTypes: true })) {
      const p = resolve(dir, e.name);
      if (e.isDirectory()) yield* walk(p);
      else if (/\.(ts|tsx)$/.test(e.name) && !/\.test\./.test(e.name)) yield p;
    }
  }

  it('UPlotChart/UPlotConfigBuilder yalnız CorePanel.tsx içinden', () => {
    const offenders: string[] = [];
    for (const f of walk(SRC)) {
      if (f.endsWith('components/chart/CorePanel.tsx')) continue;
      const src = readFileSync(f, 'utf8');
      if (/from '@grafana\/ui'/.test(src) && /UPlotChart|UPlotConfigBuilder/.test(src)) {
        offenders.push(f);
      }
    }
    expect(offenders, 'CorePanel dışında @grafana/ui chart primitifi').toEqual([]);
  });
});
