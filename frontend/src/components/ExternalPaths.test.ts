import { describe, expect, it } from 'vitest';
import { ellipsizePathMiddle } from './ExternalPaths';

// v0.9.1255 — Operator-reported: topolojide dış düğüm
// `esbprod.example.internal` yazıyordu, oysa anlamlı olan URL'in DEVAMIydı
// (.../NVI/KPS/YerlesimYeriSorgulama2). Kırılım eklendi — ama kırılımın
// SONDAN kırpan bir hücrede çizilmesi hiçbir şey çözmezdi: aynı ESB
// base'i paylaşan beş uç, beşi de "/tibcoESB/ExternalServic…" olarak
// görünürdü. Bu dosya, ayırt edici parçanın (kuyruk) hayatta kaldığını
// pinliyor.
describe('ellipsizePathMiddle', () => {
  it('sığan yolu değiştirmez', () => {
    expect(ellipsizePathMiddle('/a/b', 20)).toBe('/a/b');
    expect(ellipsizePathMiddle('/'.padEnd(20, 'x'), 20)).toHaveLength(20);
  });

  it('ortak ÖN EKİ olan iki yol kırpıldıktan sonra hâlâ ayırt edilebilir', () => {
    const a = '/tibcoESB/ExternalServices/NVI/KPS/YerlesimYeriSorgulama2';
    const b = '/tibcoESB/ExternalServices/NVI/KPS/KimlikNoSorgulama';
    const ea = ellipsizePathMiddle(a, 26);
    const eb = ellipsizePathMiddle(b, 26);
    expect(ea).not.toBe(eb);
    // Sondan kırpan bir uygulama burada EŞİT dönerdi — asıl regresyon bu.
    expect(a.slice(0, 26)).toBe(b.slice(0, 26));
  });

  it('görünen kuyruk GERÇEK bir sonektir (bütçenin 2/3ü)', () => {
    const p = '/tibcoESB/ExternalServices/NVI/KPS/YerlesimYeriSorgulama2';
    // Çekmece genişliği: son segmentin tamamı (22 karakter) sığar.
    expect(ellipsizePathMiddle(p, 46).endsWith('YerlesimYeriSorgulama2')).toBe(true);
    // Kart genişliği: bütçe son segmentten dar, ama görünen parça yine
    // yolun GERÇEK soneki — uydurma bir kısaltma değil.
    const dense = ellipsizePathMiddle(p, 26);
    expect(dense).toContain('…');
    expect(p.endsWith(dense.slice(dense.indexOf('…') + 1))).toBe(true);
    expect(p.startsWith(dense.slice(0, dense.indexOf('…')))).toBe(true);
  });

  it('bütçeyi aşmaz', () => {
    for (const max of [8, 12, 26, 46]) {
      const out = ellipsizePathMiddle('/a'.repeat(120), max);
      expect(out.length).toBeLessThanOrEqual(max);
    }
  });

  it('dejenere bütçelerde patlamaz', () => {
    expect(ellipsizePathMiddle('/abcdef', 1)).toBe('…');
    expect(ellipsizePathMiddle('/', 1)).toBe('/');
    expect(ellipsizePathMiddle('', 26)).toBe('');
  });
});
