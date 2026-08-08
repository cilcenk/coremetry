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
      // v0.9.789 — bucket-tık callback'i AYNI kuralda: çağıranlar her
      // render'da taze ok fonksiyonu veriyor (ServiceCharts useCallback'li
      // ama CorrelationContextDrawer inline), kimliği bir dep dizisine
      // girerse uPlot her poll tick'inde destroy/recreate olur.
      expect(d).not.toContain('onBucketClick,');
      expect(d).not.toContain('onBucketClick]');
      // v0.9.792 — pin durumu da AYNI kuralda: tooltip'in donma anahtarı bir
      // çizim girdisi değil. Bir dep dizisine sızarsa her pin/unpin uPlot'u
      // destroy/recreate ederdi — pinlemek grafiği yok edip yeniden çizerdi.
      expect(d).not.toContain('pinRef');
      expect(d).not.toContain('pinnedIdx');
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
    // v0.9.798 — VURGU dalı eklendi (Overview "Toplam" çizgisi 2.5px).
    // Kapının koruduğu şey değişmedi: bars 1, VURGUSUZ taban 1.5.
    expect(src).toMatch(/lineWidth: bars \? 1 : emphasis\?\.\[i\] \? 2\.5 : 1\.5/);
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

// ---------------------------------------------------------------------------
// v0.9.789 — bucket-tık ("spike → exemplar") v2 motorunda.
//
// Kanca v0.7.22'den beri MultiLineChart'ın uPlot hook'undaydı ve v2'ye geçen
// her panel onu SESSİZCE kaybediyordu — bu yüzden ServiceCharts'ın error-rate
// ve latency panelleri eski gövdede tutuluyordu. Buradaki kapılar üç şeyi
// çiviler: (a) jest önceliği (◆ > bucket > panel eylemi), (b) pencere
// hesabının SAF modülden gelmesi (iki motor tek fonksiyon), (c) afordans.
// ---------------------------------------------------------------------------
describe('CorePanel bucket-tık (v0.9.789)', () => {
  const src = readFileSync(
    resolve(__dirname, './CorePanel.tsx'), 'utf8',
  ).replace(/\/\/.*$/gm, '');

  it('prop tipte + callback REF üzerinden okunur', () => {
    expect(src).toMatch(/onBucketClick\?: \(fromNs: number, toNs: number\) => void/);
    expect(src).toMatch(/const bucketClickRef = useRef\(onBucketClick\)/);
    expect(src).toMatch(/bucketClickRef\.current = onBucketClick/);
  });

  it('🔴 pencere hesabı SAF modülden — CorePanel kendi kopyasını yazmaz', () => {
    expect(src).toMatch(/import \{ bucketWindowNs \} from '@\/lib\/chart\/bucketWindow'/);
    // ms ekseni (dataFrame köprüsü) → unitsPerSec 1000.
    expect(src).toMatch(/bucketWindowNs\(xs, xMs, 1000\)/);
    // Yerel kopya izleri: v1'in adım taraması + ns yuvarlaması burada
    // OLMAMALI. (Çıplak /stepSec/ ARAMAYIN — tooltip'in KENDİ stepSec'i
    // var, fmtTooltipTime çözünürlüğü için; kapı onu yakalayıp yanlış
    // kızarır. v0.9.720/785'in aşırı-geniş-regex dersi.)
    expect(src).not.toMatch(/let stepSec = 60/);
    expect(src).not.toMatch(/nearestI/);
    expect(src).not.toMatch(/1e9/);
  });

  it('🔴 ◆ isabeti ÖNCELİKLİ kalır: exemplar dalı bucket dalından ÖNCE', () => {
    const ex = src.indexOf('exemplarClickRef.current(hit.traceId)');
    const bk = src.indexOf('bucketClickRef.current');
    // (bucketClickRef ilk olarak ref tanımında geçiyor — tık gövdesindeki
    //  kullanımı arıyoruz, o yüzden `if (bucketClickRef.current) {` aranır.)
    const bkGate = src.indexOf('if (bucketClickRef.current) {');
    expect(ex).toBeGreaterThan(0);
    expect(bkGate).toBeGreaterThan(0);
    expect(bk).toBeGreaterThan(0);
    expect(ex).toBeLessThan(bkGate);
  });

  it('sürükleme + çift-tık kapıları bucket-tık için de geçerli', () => {
    // 5px drag eşiği tık gövdesinin BAŞINDA — üç eylem de aynı kapıdan geçer.
    expect(src).toMatch(/Math\.hypot\(e\.clientX - d\.x, e\.clientY - d\.y\) > 5\) return;/);
    // Çift-tık zoom-geri: bekleyen bucket-tık zamanlayıcısını iptal eder.
    expect(src).toMatch(/setTimeout\(\(\) => cb\(w\.fromNs, w\.toNs\), 250\)/);
    expect(src).toMatch(/onDoubleClick=\{\(e\) => \{\s*if \(clickTimerRef\.current\)/);
  });

  it('çizim alanı DIŞINDAKİ tık pencere üretmez', () => {
    expect(src).toMatch(/if \(left < 0 \|\| left > u\.over\.clientWidth\) return null;/);
    expect(src).toMatch(/if \(top < 0 \|\| top > u\.over\.clientHeight\) return null;/);
  });

  it('afordans: pointer imleç + "tık → örnek trace" ipucu', () => {
    expect(src).toMatch(/cursor: \(onExpandClick \|\| onBucketClick\) && !fullscreen \? 'pointer'/);
    expect(src).toMatch(/tık → örnek trace/);
    // İpucu tıkı yutmamalı.
    expect(src).toMatch(/pointerEvents: 'none', opacity: 0\.75/);
  });
});

// ---------------------------------------------------------------------------
// v0.9.792 — tooltip sabitleme (Grafana-parite #2) v2 motorunda.
//
// Dört preset (OVC/TC/MLC/TSP) bu jesti taşıyordu, CorePanel taşımıyordu:
// v2'ye geçen her panel pin'i SESSİZCE kaybediyordu. Kapılar üç şeyi çiviler:
// (a) karar mantığının PAYLAŞILAN saf çekirdekten gelmesi (CorePanel kendi
// durum makinesini yazmaz), (b) pinliyken tooltip'in gerçekten donması —
// setCursor guard'ı + mouseleave istisnası, (c) çözme yollarının hepsi:
// ikinci tık, Esc, çift-tık, zoom, rebuild.
// ---------------------------------------------------------------------------
describe('CorePanel tooltip pin (v0.9.792)', () => {
  const src = readFileSync(
    resolve(__dirname, './CorePanel.tsx'), 'utf8',
  ).replace(/\/\/.*$/gm, '');

  it('🔴 karar SAF çekirdekten — CorePanel kendi durum makinesini yazmaz', () => {
    expect(src).toMatch(
      /import \{ decidePinGesture, applyPinStyle, clearPinStyle \} from '@\/lib\/chart\/tooltipPin'/);
    expect(src).toMatch(/const g = decidePinGesture\(\{/);
    // Yerel kopya izleri: eşik/`detail` kuralları burada TEKRAR yazılmamalı.
    expect(src).not.toMatch(/e\.detail > 1/);
  });

  it('İKİ jest de pinler: Shift+tık ve Alt+tık', () => {
    expect(src).toMatch(/shiftKey: e\.shiftKey, altKey: e\.altKey/);
  });

  it('🔴 pin zincirin BAŞINDA — modifiyeli tık bucket/exemplar\'a düşmez', () => {
    const pin = src.indexOf('decidePinGesture({');
    const ex = src.indexOf('exemplarClickRef.current(hit.traceId)');
    const bkGate = src.indexOf('if (bucketClickRef.current) {');
    expect(pin).toBeGreaterThan(0);
    expect(pin).toBeLessThan(ex);
    expect(pin).toBeLessThan(bkGate);
    // 'swallow' zinciri kesmeli; 'passthrough' düşerek devam etmeli.
    expect(src).toMatch(/if \(g\.action === 'swallow'\) return;/);
    expect(src).toMatch(/if \(g\.action === 'unpin'\) \{ unpinTooltip\(\); return; \}/);
  });

  it('dinleyiciler KOŞULSUZ — pin tık callback\'i olmayan panelde de çalışır', () => {
    expect(src).toMatch(/onPointerDown=\{\(e\) => \{ clickRef\.current = /);
    expect(src).toMatch(/onClick=\{\(e\) => \{/);
    // Eski koşullu bağlama geri gelmemeli.
    expect(src).not.toMatch(/onClick=\{\(onExpandClick \|\| onExemplarClick/);
    // Zincirin kendi kapısı gövdeye taşındı (pin'den SONRA).
    expect(src).toMatch(/if \(!onExpandClick && !onExemplarClick && !onBucketClick\) return;/);
    expect(src).toMatch(/if \(fullscreen\) return;/);
  });

  it('🔴 pinliyken tooltip DONUK: setCursor erken döner, mouseleave kapatmaz', () => {
    expect(src).toMatch(/if \(pinRef\.current != null\) return;\s*\n\s*const idx = u\.cursor\.idx;/);
    expect(src).toMatch(/onMouseLeave=\{\(\) => \{ if \(pinRef\.current == null && ttRef\.current\)/);
  });

  it('çözme yolları eksiksiz: Esc · çift-tık · zoom · rebuild', () => {
    expect(src).toMatch(/e\.key === 'Escape' && pinRef\.current != null/);
    expect(src).toMatch(/onDoubleClick=\{\(e\) => \{[\s\S]{0,200}?unpinTooltip\(\);/);
    // setSelect (drag-zoom) pencereyi kaydırır → pin bayatlar.
    expect(src).toMatch(/unpinTooltip\(\);\s*\n\s*onZoomRef\.current\(fromSec, toSec\);/);
    expect(src).toMatch(/\}, \[config\]\);/);
  });

  it('📌 göstergesi + "Shift+tık: sabitle" ipucu paylaşımlı dilde', () => {
    // 📌 satırı applyPinStyle'dan gelir (dört preset'le AYNI metin).
    expect(src).toMatch(/applyPinStyle\(tt\)/);
    expect(src).toMatch(/clearPinStyle\(ttRef\.current, 'display'\)/);
    // İpucu: note satırıyla aynı token'lar, tooltip'in ALTINDA.
    expect(src).toMatch(/Shift\+tık: sabitle/);
    expect(src).toMatch(/color:var\(--text3\);font-size:10px/);
    expect(src).toMatch(/\.join\(''\) \+ PIN_TIP_HTML;/);
  });
});

// ---------------------------------------------------------------------------
// v0.9.793 — focusedLabel (lejant hover vurgusu) + formül kesikli.
//
// İkisi de "v2 motoruna geçerken sessizce kaybolan davranış" sınıfından —
// QueryPanel:27 focusedLabel boşluğunu BELGELEYEREK taşıyordu. Kapılar
// üç şeyi çiviler: (a) odak uPlot'u YIKMADAN uygulanır (v0.9.704 dersi),
// (b) taban çizgi kalınlığı config'in kendisinden okunur — panelde ikinci
// bir "1.5" kopyası yok, (c) kesikli işareti ghost kanalıyla AYNI prop'a
// iner, ikinci bir çizim yolu doğmaz.
// ---------------------------------------------------------------------------
describe('CorePanel focusedLabel (v0.9.793)', () => {
  const src = readFileSync(
    resolve(__dirname, './CorePanel.tsx'), 'utf8',
  ).replace(/\/\/.*$/gm, '');

  it('prop tipte + TSP ile aynı sözleşme (string | null)', () => {
    expect(src).toMatch(/focusedLabel\?: string \| null;/);
  });

  it('🔴 REBUILD YOK: seri alpha/genişliği mutasyon + redraw ile uygulanır', () => {
    expect(src).toMatch(/if \(changed\) u\.redraw\(true\);/);
    expect(src).toMatch(/s\.alpha = st\.alpha/);
    expect(src).toMatch(/s\.width = st\.width/);
    // Odak bir config girdisi DEĞİL: config useMemo dizisine sızarsa her
    // lejant hover'ı uPlot'u destroy/recreate ederdi.
    const deps = src.match(/\}, \[[^\]]*\]\);/g) ?? [];
    const cfg = deps.filter(d => d.includes('overlaySig'));
    expect(cfg.length).toBe(1);
    expect(cfg[0]).not.toContain('focus');
  });

  it('🔴 hesap SAF çekirdekten — panel kendi alpha sabitini yazmaz', () => {
    expect(src).toMatch(
      /import \{ resolveFocusIdx, focusSeriesStyle \} from '@\/lib\/chart\/seriesFocus'/);
    expect(src).toMatch(/focusSeriesStyle\(/);
    expect(src).toMatch(/resolveFocusIdx\(aligned\.names, focusName\)/);
    expect(src).not.toMatch(/alpha: 0\.25/);
  });

  it('taban genişlik CONFIG\'den okunur — ikinci "1.5" kopyası yok', () => {
    expect(src).toMatch(/config\.getSeries\(\)\.map\(s => s\.props\.lineWidth\)/);
    // Config'deki tek taban ifadesi yerinde kalmalı (v0.9.785 kapısı;
    // v0.9.798'de vurgu dalı eklendi, taban 1.5 aynen duruyor).
    expect(src).toMatch(/lineWidth: bars \? 1 : emphasis\?\.\[i\] \? 2\.5 : 1\.5/);
  });

  it('iki sürücü tek kanal: kontrollü prop kazanır, yoksa iç lejant hover', () => {
    expect(src).toMatch(/const focusName = focusedLabel \?\? hoverName;/);
    expect(src).toMatch(/onMouseEnter=\{\(\) => setHoverName\(s\.name\)\}/);
    // ♿ klavye fokusu da aynı kanaldan (v0.9.711 erişim dersinin devamı).
    expect(src).toMatch(/onFocus=\{\(\) => setHoverName\(s\.name\)\}/);
    expect(src).toMatch(/onBlur=/);
  });
});

describe('CorePanelMulti kesikli işareti (v0.9.793)', () => {
  const src = readFileSync(
    resolve(__dirname, './corePanelEntry.tsx'), 'utf8',
  ).replace(/\/\/.*$/gm, '');

  it('item başına dashed + ghost AYNI diziyi doldurur (tek çizim yolu)', () => {
    expect(src).toMatch(/dashed\?: boolean;/);
    expect(src).toMatch(/dashed\.push\(!!it\.dashed\)/);
    expect(src).toMatch(/dashed\.push\(true\)/);
    // Prop yalnız GERÇEKTEN kesikli varsa geçer: boş bir dizi kimliği
    // config imzasını (dashed?.join(',')) gereksizce oynatırdı.
    expect(src).toMatch(/dashed=\{anyDash \? dashed : undefined\}/);
    expect(src).not.toMatch(/dashed=\{ghostItems\?\.length \? dashed : undefined\}/);
  });

  it('🟠 hizalama TEK geçişte — ikinci sayaç yok', () => {
    // Eski hâli `frames.map(() => false)` ile diziyi SONRADAN kuruyordu;
    // item başına işaret gelince o yol iki ayrı frame sayacı gerektirirdi.
    expect(src).not.toMatch(/frames\.map\(\(\) => false\)/);
  });
});

describe('Explore v2 formül paneli kesikli (v0.9.793)', () => {
  const src = readFileSync(
    resolve(__dirname, '../../pages/explore/QueryPanel.tsx'), 'utf8',
  ).replace(/\/\/.*$/gm, '');

  it('formül serisi kesikli + GroupTable odağı v2 motoruna bağlı', () => {
    expect(src).toMatch(/dashed: panel\.isFormula,/);
    expect(src).toMatch(/focusedLabel=\{focusedLabel\}/);
  });

  it('v2 "focusedLabel yok" şerhi kaldırıldı — belge koda uydu', () => {
    const raw = readFileSync(
      resolve(__dirname, '../../pages/explore/QueryPanel.tsx'), 'utf8');
    expect(raw).not.toMatch(/focusedLabel \(GroupTable hover vurgusu\) v2'de henüz yok/);
  });
});

// ---------------------------------------------------------------------------
// v0.9.789 — ölü kancaların silinmesi (geriye-uyum şimi YOK kuralı).
// colorOf / selectedOps / onLegendClick üçlüsünün repo genelinde tüketicisi
// kalmamıştı; MLC'nin v2 kapısını da yalnız onlar kapalı tutuyordu. Kapı
// kaynak taramasıdır: bir gün "lazım olur" diye geri sızarlarsa kızarır.
// ---------------------------------------------------------------------------
describe('MultiLineChart ölü kancaları (v0.9.789)', () => {
  const mlc = readFileSync(
    resolve(__dirname, '../MultiLineChart.tsx'), 'utf8',
  ).replace(/\/\/.*$/gm, '');

  it('üç ölü prop kaynakta YOK', () => {
    expect(mlc).not.toMatch(/\bcolorOf\b/);
    expect(mlc).not.toMatch(/\bselectedOps\b/);
    expect(mlc).not.toMatch(/\bonLegendClick\b/);
  });

  it('v2 kapısı yalnız bayrağa bakar — bucket-tık artık diskalifiye etmez', () => {
    expect(mlc).toMatch(/if \(!chartsV2\(\)\) return <MultiLineChartInner/);
    expect(mlc).not.toMatch(/canV2/);
    expect(mlc).toMatch(/onBucketClick=\{onBucketClick\}/);
  });

  it('🔴 v1 gövdesi de SAF modülü çağırır — iki motor tek hesap', () => {
    expect(mlc).toMatch(/import \{ bucketWindowNs \} from '@\/lib\/chart\/bucketWindow'/);
    expect(mlc).toMatch(/const \{ fromNs, toNs \} = bucketWindowNs\(xs, xSec\)/);
    expect(mlc).not.toMatch(/let stepSec = 60/);
  });

  it("'-ms' sync ad alanı korunur (v1 sn ↔ v2 ms karışmasın)", () => {
    expect(mlc).toMatch(/syncKey=\{syncKey \? `\$\{syncKey\}-ms` : undefined\}/);
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

// ── VURGU kanalı (v0.9.798) ─────────────────────────────────────────────
//
// Overview'ın "Toplam" çizgisi route kırılımının yanında duruyor ve
// ondan FARKLI bir şey ölçüyor (gözlem-ağırlıklı servis geneli). Aynı
// kalınlıkta çizilseydi 11 çizgiden biri gibi görünürdü.
//
// Kapının koruduğu asıl şey RENK TEKLİĞİ: rengi ÜÇ yer okuyor (uPlot
// config, tooltip satırı, lejant swatch'ı) ve üçü ayrışırsa operatör
// grafikte accent, lejantta başka renk görür.

describe('CorePanel vurgu kanalı (v0.9.798)', () => {
  const src = readFileSync(
    resolve(__dirname, './CorePanel.tsx'), 'utf8',
  ).replace(/\/\/.*$/gm, '');

  it('renk TEK saf çekirdekten — üç okuma yeri de seriesLineColor', () => {
    expect(src).toMatch(
      /import \{ seriesLineColor, type SeriesRole \} from '@\/lib\/chart\/seriesRole'/);
    // Üç çağrı: config lineColor + tooltip satırı + lejant swatch'ı.
    expect((src.match(/seriesLineColor\(/g) ?? []).length).toBe(3);
    // Eski doğrudan çağrı KALMAMALI: kalsaydı o yüzey vurguyu görmezdi.
    expect(src).not.toMatch(/resolveVar\(seriesRoleColor\(/);
  });

  it('🔴 config bağımlılığı emphasis TAŞIR (yoksa vurgu geç gelir)', () => {
    const deps = src.match(/\}, \[[^\]]*\]\);/g) ?? [];
    const cfg = deps.filter(d => d.includes('overlaySig'));
    expect(cfg.length, 'config useMemo dizisi bulunamadı').toBe(1);
    expect(cfg[0]).toContain("emphasis?.join(',')");
  });
});

describe('CorePanelMulti vurgu işareti (v0.9.798)', () => {
  const src = readFileSync(
    resolve(__dirname, './corePanelEntry.tsx'), 'utf8',
  ).replace(/\/\/.*$/gm, '');

  it('item başına emphasis, dashed ile AYNI tek geçişte', () => {
    expect(src).toMatch(/emphasis\?: boolean;/);
    expect(src).toMatch(/emphasis\.push\(!!it\.emphasis\)/);
    // Prop yalnız GERÇEKTEN vurgu varsa geçer (dashed sözleşmesi).
    expect(src).toMatch(/emphasis=\{anyEmph \? emphasis : undefined\}/);
  });

  it('hayalet ASLA vurgulu değil (soluk+kesikli olmasının tek sebebi geri planda kalması)', () => {
    expect(src).toMatch(/emphasis\.push\(false\)/);
  });
});
