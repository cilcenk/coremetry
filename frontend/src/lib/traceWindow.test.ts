import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { alignTraceWindow } from './traceWindow';

// v0.9.636 — /traces'te grafik ile tablo AYNI pencereyi göstermiyordu:
// backend ham liste alt sınırını 5dk'ya aşağı yuvarlıyor (v0.5.356,
// kova-örtüşme bölgesindeki trace'ler gizlenmesin diye), hacim şeridi
// yuvarlamıyordu. Tabloda, grafiğin sol kenarının SOLUNDA satırlar
// çıkıyordu.

const MIN = 60 * 1e9;
const BUCKET = 5 * MIN;

describe('alignTraceWindow', () => {
  it('5dk sınırına aşağı yuvarlar', () => {
    const from = 7 * BUCKET + 137 * 1e9; // kova ortasında bir an
    const { from: got } = alignTraceWindow(from, from + 60 * MIN);
    expect(got).toBe(7 * BUCKET);
    expect(got % BUCKET).toBe(0);
  });

  it('üst sınıra DOKUNMAZ — yalnız alt sınır hizalanır', () => {
    const from = 7 * BUCKET + 137 * 1e9;
    const to = from + 60 * MIN;
    expect(alignTraceWindow(from, to).to).toBe(to);
  });

  // >5dk KAPISI: dar pencerelerde 5dk'lık genişletme operatörün
  // algıladığı aralığa hükmederdi. Go tarafı da bu kapıyı taşıyor;
  // ayrışırlarsa hizalama hiçbir işe yaramaz.
  it('pencere 5dk veya daha darsa HİÇ yuvarlamaz', () => {
    const from = 7 * BUCKET + 137 * 1e9;
    for (const width of [1 * MIN, 3 * MIN, BUCKET]) {
      expect(alignTraceWindow(from, from + width).from).toBe(from);
    }
  });

  it('5dk’yı geçer geçmez yuvarlar', () => {
    const from = 7 * BUCKET + 137 * 1e9;
    expect(alignTraceWindow(from, from + BUCKET + 1).from).toBe(7 * BUCKET);
  });

  // IDEMPOTENT olmak ZORUNDA: backend hizalanmış değeri tekrar
  // yuvarlıyor. Değilse iki taraf sonsuza kadar birbirini kaydırırdı.
  it('idempotent — backend tekrar yuvarlayınca değişmez', () => {
    const from = 7 * BUCKET + 137 * 1e9;
    const to = from + 60 * MIN;
    const once = alignTraceWindow(from, to);
    const twice = alignTraceWindow(once.from, once.to);
    expect(twice).toEqual(once);
  });

  it('bozuk girdide olduğu gibi döner', () => {
    expect(alignTraceWindow(NaN, 5).from).toBeNaN();
    expect(alignTraceWindow(1, Infinity).from).toBe(1);
  });

  // Kayma en fazla bir kova kadar olmalı — operatöre söylediğimiz bedel
  // bu ("5 dakikaya kadar sola kayar"); daha fazlası sözleşme ihlali.
  it('kayma asla 5dk’yı aşmaz', () => {
    for (let off = 0; off < BUCKET; off += 17 * 1e9) {
      const from = 100 * BUCKET + off;
      const shift = from - alignTraceWindow(from, from + 60 * MIN).from;
      expect(shift).toBeGreaterThanOrEqual(0);
      expect(shift).toBeLessThan(BUCKET);
    }
  });
});

// EN ÖNEMLİ TEST: iki taraf AYRIŞMAMALI. Frontend hizalaması backend'in
// kuralının kopyası — Go tarafındaki >5dk kapısı ya da kova boyu
// değişirse burası da değişmeli, yoksa hizalama sessizce işlevsizleşir
// ve hata geri gelir.
describe('backend kuralıyla eşleşme', () => {
  const go = readFileSync(
    resolve(__dirname, '../../../internal/chstore/repo.go'), 'utf8',
  );
  const fn = go.slice(go.indexOf('func buildGetTracesWhere'));
  const body = fn.slice(0, fn.indexOf('\n}\n'));

  it('Go tarafı hâlâ 5 dakikaya Truncate ediyor', () => {
    expect(body).toContain('Truncate(5 * time.Minute)');
  });

  it('Go tarafı hâlâ >5dk kapısını taşıyor', () => {
    expect(body).toMatch(/Sub\(f\.From\)\s*>\s*5\s*\*\s*time\.Minute/);
  });
});
