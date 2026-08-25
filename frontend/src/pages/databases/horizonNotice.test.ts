import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { receiverHorizonNotice, spanHorizonNotice, fmtHorizon } from './horizonNotice';

// v0.10.18 — F0.9a. /databases'in iki paneli farklı ufuktan okuyor ve
// boş receiver paneli operatöre YANLIŞ TEŞHİS veriyordu ("receiver kur"
// — oysa receiver çalışıyor, veri TTL'e düşmüş).

const D = 86400;

describe('receiverHorizonNotice', () => {
  it('OPERATÖRÜN YAŞADIĞI DURUM — 30g pencere, 7g saklama, boş panel', () => {
    const n = receiverHorizonNotice(30 * D, 7, 0);
    expect(n).not.toBeNull();
    expect(n!.kind).toBe('explains-empty');
    // Kritik: boş olmanın kurulum eksikliği OLMADIĞINI söylemeli.
    expect(n!.text).toContain('GELMEZ');
    expect(n!.text).toContain('7 gün');
  });

  it('pencere ufkun İÇİNDEyse hiçbir şey ilan edilmez', () => {
    // Asimetri yoksa beyan gürültüdür.
    expect(receiverHorizonNotice(6 * D, 7, 0)).toBeNull();
    expect(receiverHorizonNotice(7 * D, 7, 0)).toBeNull();
  });

  it('sınırın bir saniye üstü ilan edilir', () => {
    expect(receiverHorizonNotice(7 * D + 1, 7, 0)).not.toBeNull();
  });

  it('dolu panelde ton değişir — açıklama değil, sınır bildirimi', () => {
    const n = receiverHorizonNotice(30 * D, 7, 12);
    expect(n!.kind).toBe('declares-limit');
    // Dolu panelde "boş olması ... GELMEZ" cümlesi anlamsız olurdu.
    expect(n!.text).not.toContain('GELMEZ');
  });

  // ⚠ EN ÖNEMLİ DAL. Ufuk bilinmiyorsa SUSMAK zorunda.
  //
  // Buradaki tehlike, birinin 0'ı "makul" bir varsayılanla (7)
  // doldurması: o an arayüz ÖLÇÜLMEMİŞ bir sayıyı ölçülmüş gibi ilan
  // eder ve düzeltme kendi kusuruna dönüşür.
  describe('ufuk bilinmiyorsa SUSAR', () => {
    for (const [name, h] of [
      ['undefined', undefined],
      ['sıfır', 0],
      ['negatif', -1],
    ] as const) {
      it(name, () => expect(receiverHorizonNotice(30 * D, h, 0)).toBeNull());
    }
  });

  it('bozuk pencere değerlerinde SUSAR', () => {
    expect(receiverHorizonNotice(0, 7, 0)).toBeNull();
    expect(receiverHorizonNotice(-5, 7, 0)).toBeNull();
    expect(receiverHorizonNotice(NaN, 7, 0)).toBeNull();
    expect(receiverHorizonNotice(Infinity, 7, 0)).toBeNull();
  });

  it('operatör saklamayı UZATTIYSA beyan kendiliğinden susar', () => {
    // Sunucu etkin değeri gönderdiği için, retention.metrics 90d'ye
    // çekildiğinde 30g pencerede asimetri kalmaz. Sabit "7 gün" yazan
    // bir sürüm burada YALAN söylemeye devam ederdi — bu testin sebebi o.
    expect(receiverHorizonNotice(30 * D, 90, 0)).toBeNull();
  });
});

describe('spanHorizonNotice', () => {
  it('env/raw modunda kısalan ufku ilan eder', () => {
    // env süzgeci açıkken okuma MV'yi bırakıp ham spans'e düşüyor:
    // 90 → 30. Sayfa kaynağın değiştiğini söylüyor, ufkun değil.
    const n = spanHorizonNotice(60 * D, 30);
    expect(n).not.toBeNull();
    expect(n!.text).toContain('30 gün');
  });

  it('MV modunda 90 günün altındaki pencerelerde susar', () => {
    expect(spanHorizonNotice(30 * D, 90)).toBeNull();
  });

  it('ufuk bilinmiyorsa susar', () => {
    expect(spanHorizonNotice(60 * D, undefined)).toBeNull();
  });
});

describe('fmtHorizon', () => {
  it.each([
    [7, '7 gün'],
    [30, '30 gün'],
    [90, '90 gün'],
    [1, '1 gün'],
  ])('%i → %s', (d, want) => expect(fmtHorizon(d)).toBe(want));
});

// ⚠ KABLOLAMA PİNİ. Yukarıdaki 16 test saf çekirdeği koruyor ama
// çekirdek ÇAĞRILMIYORSA kusur yerinde kalır — bu depoda tekrar eden
// bir sınıf (v0.9.1334, v0.10.11). Kaynak taraması, çünkü korunması
// gereken davranış değil BAĞLANTI.
describe('Databases.tsx kablolaması', () => {
  const src = readFileSync(
    new URL('../Databases.tsx', import.meta.url), 'utf8',
  );

  it('yardımcıları GERÇEKTEN çağırıyor', () => {
    expect(src).toContain('receiverHorizonNotice(windowSec');
    expect(src).toContain('spanHorizonNotice(windowSec');
  });

  it('YANLIŞ TEŞHİS metni ufuk dalına kapılanmış', () => {
    // Asıl kusur buydu: pencere saklamayı aştığında boş panel
    // "receiver kur" diyordu. Metin hâlâ var (ufuk içindeyken doğru)
    // ama artık ÖNÜNDE explains-empty dalı olmak zorunda.
    const misdiagnosis = src.indexOf('Point an OpenTelemetry database receiver');
    const guard = src.indexOf("receiverNotice?.kind === 'explains-empty'");
    expect(guard).toBeGreaterThan(-1);
    expect(guard).toBeLessThan(misdiagnosis);
  });

  it('windowSec memoize — bare hesap her render yeni değer üretir', () => {
    // CLAUDE.md sert kısıtı: timeRangeToNs türevleri useMemo dışında
    // hesaplanamaz (v0.5.184 sonsuz refetch sınıfı).
    expect(src).toContain('const windowSec = useMemo(');
  });
});
