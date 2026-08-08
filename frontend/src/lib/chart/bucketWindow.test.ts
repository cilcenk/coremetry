// bucketWindow — v0.9.789. Bucket-tık penceresinin saf çekirdeği.
//
// Neden test: hesap v0.7.22'den beri MultiLineChart'ın uPlot hook'unun
// içinde yaşıyordu ve oradan test EDİLEMİYORDU (canvas + jsdom). v2
// motoruna (CorePanel) taşınırken ikinci bir kopya çıkarmak yerine saf
// modüle alındı; bu tablo, İKİ motorun aynı tıkta aynı pencereyi
// açtığının kanıtı — sn ekseni (v1) ve ms ekseni (v2) aynı fonksiyonu
// aynı sonuçla çağırıyor.

import { describe, it, expect } from 'vitest';
import { bucketStepSec, bucketWindowNs, DEFAULT_BUCKET_STEP_SEC } from './bucketWindow';

// 60 sn'lik düzenli eksen (RED panellerinin tipik step'i), unix saniye.
const XS_SEC = [1_700_000_000, 1_700_000_060, 1_700_000_120, 1_700_000_180];
// Aynı eksen milisaniye (v2 motoru / @grafana köprüsü birimi).
const XS_MS = XS_SEC.map(s => s * 1000);

describe('bucketStepSec — yerel boşluk kuralı', () => {
  const cases: [string, ArrayLike<number>, number, number][] = [
    ['düzenli eksen, ortadaki nokta', XS_SEC, 1_700_000_060, 60],
    ['düzenli eksen, imleç iki nokta arasında', XS_SEC, 1_700_000_085, 60],
    ['ilk nokta (sol komşu yok → sağdaki boşluk)', XS_SEC, 1_700_000_000, 60],
    ['son nokta (sağ komşu yok → soldaki boşluk)', XS_SEC, 1_700_000_180, 60],
    ['eksen dışında sol', XS_SEC, 1_699_999_000, 60],
    ['eksen dışında sağ', XS_SEC, 1_700_009_999, 60],
    // Düzensiz aralık: en yakın noktanın YEREL boşluğu kazanır, ilk
    // boşluk değil. 10'un komşuları 0 (10 fark) ve 310 (300 fark) →
    // min = 10. 310'un komşuları 10 ve 320 → min = 10.
    ['düzensiz eksen, dar taraf', [0, 10, 310, 320], 10, 10],
    ['düzensiz eksen, geniş bölgenin sağ ucu', [0, 10, 310, 320], 310, 10],
    // Adım TIKLANAN yerin değil, EN YAKIN NOKTANIN boşluğudur: 160'a en
    // yakın nokta 300 (140 uzak; 10 ise 150), onun dar komşuluğu 290.
    ['düzensiz eksen, geniş boşlukta en yakına snap', [0, 10, 300, 900], 160, 290],
    ['tek nokta → 60 sn varsayılan', [1_700_000_000], 1_700_000_000, DEFAULT_BUCKET_STEP_SEC],
    ['boş eksen → 60 sn varsayılan', [], 0, DEFAULT_BUCKET_STEP_SEC],
    // Bozuk eksen (aynı x iki kez): yerel boşluk 0 → ilk boşluğa düşer.
    ['tekrarlı nokta → ilk boşluğa düşer', [0, 30, 30, 60], 30, 30],
    // Tüm noktalar aynı: ne yerel boşluk ne ilk boşluk kullanılabilir.
    ['tamamen dejenere eksen → 60 sn', [5, 5, 5], 5, DEFAULT_BUCKET_STEP_SEC],
  ];
  it.each(cases)('%s → %d sn', (_n, xs, x, want) => {
    expect(bucketStepSec(xs, x)).toBe(want);
  });

  it('ms ekseni sn ekseniyle AYNI adımı verir (unitsPerSec)', () => {
    expect(bucketStepSec(XS_MS, 1_700_000_060_000, 1000))
      .toBe(bucketStepSec(XS_SEC, 1_700_000_060));
  });
});

describe('bucketWindowNs — [merkez ± adım/2], nanosaniye', () => {
  it('pencere tıklanan anı ORTALAR (kenarlamaz)', () => {
    const w = bucketWindowNs(XS_SEC, 1_700_000_060);
    expect(w.fromNs).toBe(1_700_000_030 * 1e9);
    expect(w.toNs).toBe(1_700_000_090 * 1e9);
    expect(w.toNs - w.fromNs).toBe(60 * 1e9);
  });

  it('iki motor AYNI pencereyi açar — sn (v1) ↔ ms (v2)', () => {
    expect(bucketWindowNs(XS_MS, 1_700_000_085_000, 1000))
      .toEqual(bucketWindowNs(XS_SEC, 1_700_000_085));
  });

  it('tek noktalı seri sıfır genişlikte pencere ÜRETMEZ (60 sn)', () => {
    const w = bucketWindowNs([1_700_000_000], 1_700_000_000);
    expect(w.toNs - w.fromNs).toBe(DEFAULT_BUCKET_STEP_SEC * 1e9);
  });

  it('düzensiz eksende dar bucket dar pencere verir', () => {
    const w = bucketWindowNs([0, 10, 310, 320], 10);
    expect(w.fromNs).toBe(5 * 1e9);
    expect(w.toNs).toBe(15 * 1e9);
  });

  it('kesirli saniye tam sayı ns\'e yuvarlanır — float kayması yok', () => {
    // 0.1 sn adım: 0.05 sn yarım pencere → 50_000_000 ns TAM.
    const w = bucketWindowNs([1000.0, 1000.1, 1000.2], 1000.1);
    expect(w.fromNs).toBe(1_000_050_000_000);
    expect(w.toNs).toBe(1_000_150_000_000);
    expect(Number.isInteger(w.fromNs)).toBe(true);
    expect(Number.isInteger(w.toNs)).toBe(true);
  });
});
