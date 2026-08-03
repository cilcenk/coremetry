// v0.9.592 — derecelendirme kapısının sözleşmesi.
//
// Bu dilim, depoda ÖLÜ bir derecelendirme affordance'ı bulunduğu için
// var: AIAnalysisPanel operatöre "Bu analiz yararlı mıydı?" diye
// soruyor, 👍/👎 gösteriyor, tıklayınca "Teşekkürler." yazıyor —
// ve hiçbir yere yazmıyor. Aynı hatayı yeni yüzeyde tekrarlamamak
// test edilmesi gereken bir sözleşme.
import { describe, it, expect } from 'vitest';
import { canRateVerdict } from './rcaVerdictView';

describe('canRateVerdict', () => {
  it('kimlik varsa çizilir', () => {
    expect(canRateVerdict('a1b2c3d4e5f60718')).toBe(true);
  });

  it('kimlik yoksa ÇİZİLMEZ — tıklanınca hiçbir yere yazmayan düğme olurdu', () => {
    // undefined: sunucu alanı hiç göndermedi (eski sürüm gövdesi).
    expect(canRateVerdict(undefined)).toBe(false);
    // null: JSON'da açıkça null.
    expect(canRateVerdict(null)).toBe(false);
    // '': omitempty ile düşmemiş ama boş.
    expect(canRateVerdict('')).toBe(false);
    // Yalnız boşluk: sunucu tarafı de TrimSpace ediyor, iki uç aynı
    // kararı vermeli yoksa düğme çizilir ve POST 400 alır.
    expect(canRateVerdict('   ')).toBe(false);
  });
});
