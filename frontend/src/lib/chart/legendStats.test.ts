import { describe, it, expect } from 'vitest';
import { seriesStats, isAdditiveUnit, resolveLegendCollapsed } from './legendStats';

// v0.9.103 (Grafana-parity #1) — lejant istatistik çekirdeği.

describe('seriesStats', () => {
  it('düz seri: last/min/max/mean/sum/count', () => {
    expect(seriesStats([10, 20, 30])).toEqual({ last: 30, min: 10, max: 30, mean: 20, sum: 60, count: 3 });
  });
  it('null/undefined/NaN atlanır; last = son DOLU örnek', () => {
    expect(seriesStats([10, null, 30, undefined, NaN])).toEqual(
      { last: 30, min: 10, max: 30, mean: 20, sum: 40, count: 2 });
  });
  it('sonda null → last önceki dolu değer', () => {
    expect(seriesStats([5, 8, null]).last).toBe(8);
  });
  it('tamamı boş → null istatistik, sum 0', () => {
    expect(seriesStats([null, undefined, NaN])).toEqual(
      { last: null, min: null, max: null, mean: null, sum: 0, count: 0 });
    expect(seriesStats([])).toEqual({ last: null, min: null, max: null, mean: null, sum: 0, count: 0 });
  });
  it('0 değerleri sayılır (null değil)', () => {
    expect(seriesStats([0, 0, 4])).toEqual({ last: 4, min: 0, max: 4, mean: 4 / 3, sum: 4, count: 3 });
  });
  it('negatif değerler', () => {
    expect(seriesStats([-5, 5])).toEqual({ last: 5, min: -5, max: 5, mean: 0, sum: 0, count: 2 });
  });
});

describe('isAdditiveUnit', () => {
  it('toplanabilir: boş/hız/oran/adet/bytes → Sum göster', () => {
    for (const u of ['', ' req/s', 'rps', ' ops', 'count', 'errors', ' MB', ' B', 'GB', 'KB', 'bytes']) {
      expect(isAdditiveUnit(u), `additive: "${u}"`).toBe(true);
    }
  });
  it('toplanamaz: yüzde/süre → Sum gizle', () => {
    for (const u of ['%', ' %', ' ms', ' s', 'sec', 'ns', 'µs', ' min', ' h']) {
      expect(isAdditiveUnit(u), `non-additive: "${u}"`).toBe(false);
    }
  });
  it('req/s süre sanılıp elenmez (rate önce)', () => {
    expect(isAdditiveUnit('req/s')).toBe(true);
    expect(isAdditiveUnit(' ms')).toBe(false);
  });
  it('bilinmeyen birim → kapalı (conservative)', () => {
    expect(isAdditiveUnit('widgets')).toBe(false);
  });

  // v0.9.851 — UCUM bayt ailesi. YAZIM boşluğuydu: 'MB'/'GB'/'bytes' zaten
  // toplanabilirdi, yani "bayt toplanır" kararı çoktan verilmişti; yalnız
  // OTel'in kendi yazdığı biçim ('By') desende yoktu. İKİ YÖN de burada,
  // çünkü tek yönlü bir test deseni gevşetip 'ms'yi de toplanabilir yapan
  // bir düzenlemeyi yakalamaz.
  const byteCases = ['By', 'by', ' By ', 'KiBy', 'MiBy', 'GiBy', 'TiBy', 'kBy', 'MBy'];
  for (const u of byteCases) {
    it(`UCUM bayt "${u}" → toplanabilir`, () => {
      expect(isAdditiveUnit(u)).toBe(true);
    });
  }

  it('süre birimleri HÂLÂ toplanamaz (mevcut pinler korunuyor)', () => {
    for (const u of ['ms', 's', 'ns', 'µs', 'min', 'h', '%']) {
      expect(isAdditiveUnit(u), `non-additive: "${u}"`).toBe(false);
    }
  });

  it("bayt deseni FAZLA yakalamıyor — 'by' geçen rastgele birim toplanmaz", () => {
    // Desen TAM eşleşme: içinde 'by' geçen bir ad ('bytes' hariç, o zaten
    // ayrı daldan geçiyor) bayt sanılmaz.
    for (const u of ['byz', 'flyby', 'by/s2', 'ruby']) {
      expect(isAdditiveUnit(u), `should not match: "${u}"`).toBe(false);
    }
  });
});

// v0.9.483 (operatör: "Series tablosu varsayılan kapalı olsun") — açılış
// durumu çözümü: kullanıcı seçimi > panel default'u > seri-sayısı eşiği.
describe('resolveLegendCollapsed', () => {
  it('kalıcı kullanıcı seçimi her şeyi ezer', () => {
    expect(resolveLegendCollapsed(false, true, 20, 8)).toBe(false);  // kullanıcı AÇTI
    expect(resolveLegendCollapsed(true, false, 2, 8)).toBe(true);    // kullanıcı KAPATTI
  });
  it('kayıt yokken panel default\'u kazanır', () => {
    expect(resolveLegendCollapsed(null, true, 4, 8)).toBe(true);
    expect(resolveLegendCollapsed(undefined, false, 20, 8)).toBe(false);
  });
  it('kayıt ve default yokken eski eşik davranışı korunur', () => {
    expect(resolveLegendCollapsed(null, undefined, 9, 8)).toBe(true);
    expect(resolveLegendCollapsed(null, undefined, 8, 8)).toBe(false);
    expect(resolveLegendCollapsed(undefined, undefined, 4, 8)).toBe(false);
  });
});
