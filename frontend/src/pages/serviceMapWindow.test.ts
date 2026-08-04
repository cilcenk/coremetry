// v0.9.616 — /service-map penceresi PAYLAŞILAN yardımcıdan gelmeli.
//
// Bug: sayfa kendi 5 girdilik preset tablosunu taşıyordu ve tabloda
// olmayan her preset sessizce 900s'e (15 dk) düşüyordu. Sayfanın KENDİ
// varsayılanı '30m' de tabloda YOKTU — yani hiç dokunulmamış
// /service-map, picker "Son 30 dakika" derken 15 DAKİKALIK veri
// gösteriyordu. Sessiz, hiçbir yerde itiraf edilmeyen bir yanlışlık.
//
// Test iki katmanlı: (1) paylaşılan yardımcı HER preset'i doğru
// çeviriyor mu — "unit-mixing needs both branches" dersi: bir
// dönüştürücünün yalnız birkaç dalını test etmek, kalan dalların
// sessizce bozulmasına izin verir. (2) sayfa yerel bir tablo geri
// getirmiyor mu.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { rangeToSince, PRESET_SECONDS } from '@/lib/utils';
import type { TimeRange } from '@/lib/types';

describe('service-map penceresi', () => {
  it('HER preset kendi süresine çevrilir — 15 dakikaya düşen yok', () => {
    for (const [preset, secs] of Object.entries(PRESET_SECONDS)) {
      const r = { preset } as TimeRange;
      const got = rangeToSince(r);
      expect(got.seconds, `${preset} yanlış çevrildi`).toBe(secs);
    }
  });

  it("sayfanın varsayılanı '30m' gerçekten 30 dakika", () => {
    // Bug'ın tam vakası: bu preset yerel tabloda yoktu → 900s.
    expect(rangeToSince({ preset: '30m' } as TimeRange).seconds).toBe(1800);
  });

  it('custom aralık kendi süresini korur (900s fallback yok)', () => {
    const toMs = 1_770_000_000_000;
    const r = { preset: 'custom', fromMs: toMs - 3 * 3600_000, toMs } as TimeRange;
    expect(rangeToSince(r).seconds).toBe(3 * 3600);
  });

  it('ServiceMap YEREL preset→saniye tablosu taşımıyor', () => {
    const src = readFileSync(new URL('./ServiceMap.tsx', import.meta.url), 'utf8');
    // Yerel tablo geri gelirse pencere yine ayrışır ve aynı sessiz
    // yanlışlık döner. DIFF_PRESETS ayrı bir şey (etiket listesi,
    // saniye taşımıyor) ve kasten hariç.
    expect(src, 'yerel PRESETS tablosu geri gelmiş').not.toMatch(/const PRESETS[^=]*=/);
    expect(src, 'pencere hâlâ paylaşılan yardımcıdan gelmiyor').toContain('rangeToSince(range)');
  });
});
