// v0.9.594 — kalite panelinin dürüstlük kuralları.
//
// Hepsi tek bir soruyu koruyor: operatör bu panelden YANLIŞ bir sonuç
// çıkarabilir mi? "Hiç veri yok" ile "%0" aynı görünürse, motor daha
// hiç çalışmamışken "hiç kök neden bulamıyor" okunur.
import { describe, it, expect } from 'vitest';
import { rcaPct, rcaPctText, rcaEngineTone, rcaSatisfactionText } from './rcaQualityView';
import type { RCAVerdictQuality } from '@/lib/types';

const base: RCAVerdictQuality = {
  total: 0, rootCauseIdentified: 0, probableCause: 0, insufficientEvidence: 0,
  unparsed: 0, repaired: 0, shielded: 0, avgConfidence: 0, thumbsUp: 0, thumbsDown: 0,
};

describe('rcaPct', () => {
  it('normal oran', () => expect(rcaPct(3, 12)).toBe(25));

  it('payda sıfırsa null — 0 DEĞİL', () => {
    // 0 dönseydi "hiç veri yok" ekranda "%0" olarak çizilirdi ve
    // yokluk bir bulgu gibi görünürdü.
    expect(rcaPct(0, 0)).toBeNull();
    expect(rcaPct(5, 0)).toBeNull();
    expect(rcaPct(5, -1)).toBeNull();
  });

  it('NaN/Infinity null', () => {
    expect(rcaPct(NaN, 10)).toBeNull();
    expect(rcaPct(1, Infinity)).toBeNull();
  });
});

describe('rcaPctText', () => {
  it('ölçülemeyende em-dash', () => expect(rcaPctText(0, 0)).toBe('—'));
  it('ölçülende yüzde', () => expect(rcaPctText(1, 4)).toBe('%25'));
});

describe('rcaEngineTone', () => {
  it('veri yoksa renk de yok', () => {
    expect(rcaEngineTone(base)).toBeUndefined();
  });

  it('temiz motor yeşil', () => {
    expect(rcaEngineTone({ ...base, total: 100, unparsed: 2, shielded: 3 })).toBe('ok');
  });

  it('kalkanlar sık devrede → kırmızı', () => {
    expect(rcaEngineTone({ ...base, total: 100, shielded: 35 })).toBe('err');
  });

  it('insufficient_evidence tonu ETKİLEMEZ', () => {
    // Bu bir arıza değil, geçerli bir cevap. Prompt modele "bunu demek
    // ayıp değil" diyor; panelde kırmızı yapmak modeli tam da
    // kaçınmasını istemediğimiz yöne — kendinden emin ve yanlış
    // cevaba — iter.
    const allInsufficient = { ...base, total: 100, insufficientEvidence: 100 };
    expect(rcaEngineTone(allInsufficient)).toBe('ok');
  });
});

describe('rcaSatisfactionText', () => {
  it('oy yoksa "%0" DEĞİL "oy yok"', () => {
    // Oylama seyrek bir jest; sıfır oyu sıfır memnuniyet diye okumak
    // motoru haksız yere kötü gösterir.
    expect(rcaSatisfactionText({ ...base, total: 40 })).toBe('oy yok');
  });

  it('oy varsa oran + sayı', () => {
    expect(rcaSatisfactionText({ ...base, total: 40, thumbsUp: 3, thumbsDown: 1 })).toBe('%75 (4 oy)');
  });
});
