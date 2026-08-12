// traceNameCol — dar ekran denetimi D5 / B1 regresyon testi (v0.9.983)
//
// Orijinal belirti: 366px'lik bir telefonda trace şelalesinde isim
// kolonu 320px alıyor, BAR alanına 46–146px kalıyordu. Span süreleri
// birbirinden ayırt edilemiyordu — şelalenin tek varlık sebebi bu.
//
// Kök sebep: `depthMin` konteyner genişliğinden BAĞIMSIZDI (yalnız ağaç
// derinliğinin fonksiyonu, 220–320px) ve dıştaki `Math.max` onu her
// koşulda dayatıyordu.
//
// Bu test iki şeyi birden çiviliyor ve ikincisi en az birincisi kadar
// önemli: (1) dar ekranda isim kolonu konteynerin yarısını aşmıyor,
// (2) ≥640px'te dönen değer ESKİ FORMÜLLE BİT BİT AYNI. (2) olmadan
// düzeltme "masaüstünü de oynattı mı" sorusunu cevaplayamazdı; kanonik
// disiplin internal/api/cache_key_test.go.
import { describe, it, expect } from 'vitest';
import { nameColWidth, NAME_MIN, NAME_MAX, INDENT_PX, NAME_UNMEASURED } from './traceNameCol';

// v0.9.983 ÖNCESİ formül, referans olarak birebir korunuyor.
function legacyNameColWidth(containerWidth: number, maxDepth: number): number {
  if (containerWidth <= 0) return 380;
  const target = Math.round((containerWidth - 6) * 0.4);
  const depthMin = Math.min(320, 220 + maxDepth * INDENT_PX);
  return Math.max(NAME_MIN, depthMin, Math.min(target, NAME_MAX, containerWidth * 0.65));
}

describe('nameColWidth — B1', () => {
  // Raporun istediği tablo + kenar durumları.
  const CASES: { cw: number; depth: number; note: string }[] = [
    { cw: 366, depth: 2, note: 'iPhone 14, sığ ağaç' },
    { cw: 366, depth: 12, note: 'iPhone 14, derin ağaç — ESKİDEN 46px bar' },
    { cw: 320, depth: 12, note: 'en dar cihaz' },
    { cw: 640, depth: 8, note: 'telefon/tablet sınırı' },
    { cw: 1200, depth: 2, note: 'masaüstü, sığ' },
    { cw: 1200, depth: 12, note: 'masaüstü, derin' },
    { cw: 1920, depth: 20, note: 'geniş monitör' },
  ];

  it.each(CASES)('$cw px / derinlik $depth ($note) — isim kolonu bar alanını yutmuyor', ({ cw, depth }) => {
    const w = nameColWidth(cw, depth);
    // Taban NAME_MIN her koşulda geçerli; onun üstünde kolon konteynerin
    // yarısını AŞMAMALI.
    expect(w).toBeLessThanOrEqual(Math.max(NAME_MIN, cw * 0.5));
    expect(w).toBeGreaterThanOrEqual(NAME_MIN);
    expect(w).toBeLessThanOrEqual(NAME_MAX);
  });

  it.each(CASES.filter(c => c.cw >= 640))(
    '$cw px / derinlik $depth — MASAÜSTÜ değeri eski formülle birebir aynı',
    ({ cw, depth }) => {
      expect(nameColWidth(cw, depth)).toBe(legacyNameColWidth(cw, depth));
    });

  it('düzeltme YALNIZ <640px\'te bağlayıcı (sınırın ispatı)', () => {
    // Yeni sınır `containerWidth * 0.5`; 320px'lik derinlik tabanını ancak
    // konteyner 640'ın altındayken keser. Sınırın tam üstü/altı:
    expect(nameColWidth(640, 12)).toBe(legacyNameColWidth(640, 12));
    expect(nameColWidth(639, 12)).not.toBe(legacyNameColWidth(639, 12));
  });

  it('eski formül 366px\'te GERÇEKTEN kırıktı (bulgunun kanıtı)', () => {
    // Bu assert testin gerekçesini taşıyor: kaldırılırsa "düzeltme neyi
    // düzeltti" sorusunun cevabı kaybolur.
    const cw = 366;
    expect(cw - legacyNameColWidth(cw, 12)).toBeLessThan(60);   // ESKİ: 46px bar
    expect(cw - nameColWidth(cw, 12)).toBeGreaterThanOrEqual(cw * 0.5); // YENİ: ≥ yarı
  });

  it('ölçülmemiş konteyner ilk render değerini döndürüyor', () => {
    expect(nameColWidth(0, 5)).toBe(NAME_UNMEASURED);
    expect(nameColWidth(-1, 5)).toBe(NAME_UNMEASURED);
  });

  it('derinlik arttıkça kolon monoton büyür ama tavanı aşmaz', () => {
    const widths = [0, 1, 4, 8, 16, 32].map(d => nameColWidth(1200, d));
    expect(widths).toEqual([...widths].sort((a, b) => a - b));
    expect(Math.max(...widths)).toBeLessThanOrEqual(NAME_MAX);
  });
});
