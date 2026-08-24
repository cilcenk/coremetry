import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync } from 'node:fs';
import { resolve } from 'node:path';
import {
  clampThanosWindow, clampSuffix,
  THANOS_MAX_WINDOW_HOURS, THANOS_MAX_WINDOW_NS, THANOS_MAX_WINDOW_LABEL,
} from './thanosWindow';

// v0.9.1370 — /clusters pencere tavanı, istemci yarısı.
// Operatör-bildirimi: "Infrastructure'da hangi aralığı seçersem seçeyim
// hep aynı zamanı gösteriyor." Go ikizi: internal/api/thanos_window_test.go.

const H = 3600 * 1e9;

describe('clampThanosWindow', () => {
  // Sabit çapa — now() okumuyoruz (v0.5.184 sınıfı tuzağın testteki hâli).
  const to = 1_700_000_000_000 * 1e6;

  const cases: [string, number, number, boolean][] = [
    // [ad, span (saat), beklenen span (saat), kelepçelendi mi]
    ['15dk', 0.25, 0.25, false],
    ['1s', 1, 1, false],
    ['6s — ESKİ tavan, artık kelepçe değil', 6, 6, false],
    ['6s+1dk — eskiden kırpılırdı', 6 + 1 / 60, 6 + 1 / 60, false],
    ['12s', 12, 12, false],
    ['24s — TAM tavan', 24, 24, false],
    ['24s+1dk — kelepçe başlar', 24 + 1 / 60, 24, true],
    ['7g', 24 * 7, 24, true],
    ['30g', 24 * 30, 24, true],
  ];

  it.each(cases)('%s', (_n, spanH, wantH, wantClamped) => {
    const from = to - spanH * H;
    const { cFrom, cTo, clamped } = clampThanosWindow(from, to);
    expect(clamped).toBe(wantClamped);
    expect(cTo - cFrom).toBeCloseTo(wantH * H, -3);
    // ÇAPA KORUNUR: kelepçe SPAN'i sınırlar, pencereyi "şimdi"ye kaydırmaz.
    expect(cTo).toBe(to);
  });

  it('geçmişe fırçalanmış pencere BUGÜNE kaymaz', () => {
    const pastTo = to - 90 * 24 * H;       // 90 gün önce
    const { cFrom, cTo, clamped } = clampThanosWindow(pastTo - 7 * 24 * H, pastTo);
    expect(clamped).toBe(true);
    expect(cTo).toBe(pastTo);              // ← asıl sözleşme
    expect(cFrom).toBe(pastTo - THANOS_MAX_WINDOW_NS);
  });

  it('ters/dejenere aralık kelepçe üretmez', () => {
    expect(clampThanosWindow(to, to).clamped).toBe(false);
    expect(clampThanosWindow(to + H, to).clamped).toBe(false);
  });
});

describe('clampSuffix — başlık sabitten TÜRETİLİR', () => {
  it('kelepçe varsa tavanı söyler', () => {
    expect(clampSuffix(true)).toBe(` (last ${THANOS_MAX_WINDOW_HOURS}h)`);
    expect(clampSuffix(true)).toContain(THANOS_MAX_WINDOW_LABEL);
  });
  it('kelepçe yoksa başlık HİÇ uzamaz', () => {
    expect(clampSuffix(false)).toBe('');
  });
  it('ns sabiti saat sabitiyle tutarlı', () => {
    expect(THANOS_MAX_WINDOW_NS).toBe(THANOS_MAX_WINDOW_HOURS * H);
  });
});

// KAPI: kelepçe kuralı ve başlık eki TEK GÖVDEDEN gelir.
//
// Kural üç sayfada satır satır kopyalanmıştı ve başlıklarda "(last 6h)"
// elle yazılıydı — tavan değişince başlıklar yalan söylerdi. Bu kapı
// kopyaların geri sızmasını engeller.
describe('tek gövde kapısı (v0.9.1370)', () => {
  const SRC = resolve(__dirname, '..');
  function walk(dir: string, out: string[] = []): string[] {
    for (const e of readdirSync(dir, { withFileTypes: true })) {
      const p = resolve(dir, e.name);
      if (e.isDirectory()) walk(p, out);
      else if (/\.tsx?$/.test(e.name) && !/\.test\.tsx?$/.test(e.name)) out.push(p);
    }
    return out;
  }
  const files = walk(SRC).filter(f => !f.endsWith('lib/thanosWindow.ts'));

  it('elle yazılmış 6h kelepçe aritmetiği KALMADI', () => {
    const offenders = files.filter(f => {
      const s = readFileSync(f, 'utf8').replace(/\/\/.*$/gm, '');
      // Aranan şey KELEPÇE ŞEKLİ, herhangi bir 6h aritmetiği değil:
      // TREND_WINDOWS gibi meşru bir picker rung'ı da `6 * 3600 * 1e9`
      // yazar ve onu yasaklamak kapıyı yanlış yere çeker (ilk yazımın
      // kusuru buydu — kapı meşru bir seçeneği suçladı).
      return /\bsixH\b/.test(s)                       // eski yerel sabit
        || /[<>]=?\s*6\s*\*\s*3600\s*\*\s*1e9/.test(s)  // pencere KARŞILAŞTIRMASI
        || /-\s*sixH\b/.test(s);
    });
    expect(offenders.map(f => f.replace(SRC, ''))).toEqual([]);
  });

  it('başlık eki elle yazılmıyor — "(last 6h)" / "son 6h" yok', () => {
    const offenders = files.filter(f => {
      const s = readFileSync(f, 'utf8').replace(/\/\/.*$/gm, '');
      return /\(last 6h\)/.test(s) || /son 6h/.test(s);
    });
    expect(offenders.map(f => f.replace(SRC, ''))).toEqual([]);
  });

  it('kapı gerçekten tarıyor — dosya kümesi boş değil', () => {
    expect(files.length).toBeGreaterThan(100);
  });
});
