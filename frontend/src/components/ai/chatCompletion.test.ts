import { describe, it, expect } from 'vitest';
import { completionQuery, applyCompletion, moveHighlight } from './chatCompletion';

// v0.10.687 — CoSRE girişte servis adı tamamlama (D4, cosre-chat-parity planı;
// ai-ui-patterns #7). SÖZLEŞME (saf):
//   1. Tetik: imleçteki token '@' ile başlıyorsa 1+ karakterde AÇIK tetik;
//      öneksiz token ≥3 karakter ve '-' içeriyorsa (servis adı biçimi)
//      OTOMATİK tetik; aksi null (sıradan sözcükler sunucuya gitmez).
//   2. applyCompletion token'ı kanonik adla değiştirir + boşluk; imleç adın
//      sonrasına.
//   3. moveHighlight sarar (son → ilk, ilk → son).
describe('completionQuery', () => {
  it("'@' öneki açık tetik", () => {
    expect(completionQuery('hata var @sh', 12)).toEqual({ query: 'sh', start: 9, end: 12, explicit: true });
    expect(completionQuery('@', 1)).toBeNull(); // yalnız '@' — sorgu yok
  });
  it('öneksiz: ≥3 karakter ve tire → otomatik; sıradan sözcük null', () => {
    expect(completionQuery('shop-pay servisinde', 8)).toEqual({ query: 'shop-pay', start: 0, end: 8, explicit: false });
    expect(completionQuery('neden yavaş', 11)).toBeNull();
    expect(completionQuery('ab-', 3)).toEqual({ query: 'ab-', start: 0, end: 3, explicit: false });
    expect(completionQuery('a-', 2)).toBeNull();
  });
  it('imleç token ortasındaysa yalnız imlece kadar olan kısım', () => {
    expect(completionQuery('@shop-payment x', 5)).toEqual({ query: 'shop', start: 0, end: 5, explicit: true });
  });
});

describe('applyCompletion', () => {
  it('token kanonik adla değişir, boşluk eklenir, imleç sonda', () => {
    const q = completionQuery('hata var @sh', 12)!;
    expect(applyCompletion('hata var @sh', q, 'shop-payment')).toEqual({ text: 'hata var shop-payment ', caret: 22 });
  });
  it('metnin ortasında değişim sonrasını korur', () => {
    const q = completionQuery('@shop-payment x', 5)!;
    expect(applyCompletion('@shop-payment x', q, 'shop-gateway')).toEqual({ text: 'shop-gateway -payment x', caret: 13 });
  });
});

describe('moveHighlight', () => {
  it('sarar', () => {
    expect(moveHighlight(0, -1, 3)).toBe(2);
    expect(moveHighlight(2, 1, 3)).toBe(0);
    expect(moveHighlight(1, 1, 3)).toBe(2);
    expect(moveHighlight(0, 1, 0)).toBe(0);
  });
});
