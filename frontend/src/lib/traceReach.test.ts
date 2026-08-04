import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { lastReachablePage, TRACE_STAGE2_MAX_IDS } from './traceReach';

// v0.9.645 — operatör: "next butonu çok gözükmüyor, daha iyi bir yerde
// olabilir mi? next last gibi butonlar".
//
// "Son sayfa" düğmesi HER ZAMAN sunulamaz: MV yolunun aşama-2 IN listesi
// sınırlı, ötesindeki sayfalar SUNULAMIYOR. v0.9.638 tam bu yüzden
// total'ı Pager'dan kesmişti — sayı asla sayfalama sınırı değil.

describe('lastReachablePage', () => {
  it('kesin ve ulaşılabilir sayıda son sayfayı verir', () => {
    expect(lastReachablePage(500, false, 50)).toBe(9);   // 10 sayfa
    expect(lastReachablePage(50, false, 50)).toBe(0);    // tek sayfa
    expect(lastReachablePage(51, false, 50)).toBe(1);
  });

  // TAVANLI sayıda gerçek son sayfa BİLİNMİYOR — düğme yalan söylerdi.
  it('tavanlı sayıda düğme çizilmez', () => {
    expect(lastReachablePage(10000, true, 50)).toBeUndefined();
  });

  // Sunulabilir tavanın ötesi: düğme operatörü BOŞLUĞA götürürdü.
  it('sunulamayan aralıkta düğme çizilmez', () => {
    expect(lastReachablePage(TRACE_STAGE2_MAX_IDS + 1, false, 50)).toBeUndefined();
    expect(lastReachablePage(TRACE_STAGE2_MAX_IDS, false, 50)).toBeDefined();
  });

  it('sayı yoksa / bozuksa düğme çizilmez', () => {
    expect(lastReachablePage(undefined, false, 50)).toBeUndefined();
    expect(lastReachablePage(0, false, 50)).toBeUndefined();
    expect(lastReachablePage(100, false, 0)).toBeUndefined();
  });
});

// EN ÖNEMLİSİ: sabit backend'den KOPYA. Ayrışırsa Last düğmesi
// sunulamayan bir sayfaya götürür — bugünkü tekrar eden hata sınıfı
// ("bir kural iki yerde, zamanla ayrışıyor") sessiz bir UX hatasına
// dönüşür.
describe('backend sabitiyle eşleşme', () => {
  const go = readFileSync(
    resolve(__dirname, '../../../internal/chstore/repo.go'), 'utf8',
  );
  const m = go.match(/traceStage2MaxIDs\s*=\s*(\d+)/);

  it('backend sabiti okunabiliyor', () => {
    expect(m).not.toBeNull();
  });

  it('frontend kopyası backend ile AYNI', () => {
    expect(TRACE_STAGE2_MAX_IDS).toBe(Number(m![1]));
  });
});
