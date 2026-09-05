// v0.9.594 — kalite panelinin dürüstlük kuralları.
//
// Hepsi tek bir soruyu koruyor: operatör bu panelden YANLIŞ bir sonuç
// çıkarabilir mi? "Hiç veri yok" ile "%0" aynı görünürse, motor daha
// hiç çalışmamışken "hiç kök neden bulamıyor" okunur.
import { describe, it, expect } from 'vitest';
import { rcaPct, rcaPctText, rcaEngineTone, rcaSatisfactionText, rcaBucketLabel, rcaBucketSatisfaction, rcaCalibrationNote } from './rcaQualityView';
import type { RCAConfidenceBucket } from '@/lib/types';
import type { RCAVerdictQuality } from '@/lib/types';

const base: RCAVerdictQuality = {
  total: 0, rootCauseIdentified: 0, probableCause: 0, insufficientEvidence: 0,
  unparsed: 0, repaired: 0, shielded: 0, avgConfidence: 0, thumbsUp: 0, thumbsDown: 0,
  calibration: [],
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

// v0.10.410 — güven kalibrasyonu kovaları (CoSRE denetimi E4).
describe('calibration', () => {
  const bk = (bucket: RCAConfidenceBucket['bucket'], lo: number, hi: number, up = 0, down = 0): RCAConfidenceBucket =>
    ({ bucket, lo, hi, total: up + down, thumbsUp: up, thumbsDown: down });

  it('etiket sınırları sunucudan, yazım kuralı burada', () => {
    expect(rcaBucketLabel(bk('low', 0, 0.4))).toBe('düşük (<0.40)');
    expect(rcaBucketLabel(bk('mid', 0.4, 0.6))).toBe('orta (0.40–0.60)');
    expect(rcaBucketLabel(bk('high', 0.6, 1))).toBe('yüksek (>0.60)');
  });

  it('oy yoksa "oy yok", varsa oran + sayı', () => {
    expect(rcaBucketSatisfaction(bk('mid', 0.4, 0.6))).toBe('oy yok');
    expect(rcaBucketSatisfaction(bk('mid', 0.4, 0.6, 3, 1))).toBe('%75 (4 oy)');
  });

  it('not yalnız yeterli oyla (≥5) ve çoğunluk 👎 iken', () => {
    expect(rcaCalibrationNote([])).toBeNull();
    expect(rcaCalibrationNote([bk('high', 0.6, 1, 1, 3)])).toBeNull(); // 4 oy — hüküm yok
    expect(rcaCalibrationNote([bk('high', 0.6, 1, 4, 1)])).toBeNull(); // kalibre
    expect(rcaCalibrationNote([bk('high', 0.6, 1, 2, 3)])).toMatch(/kalibre değil/);
  });
});
