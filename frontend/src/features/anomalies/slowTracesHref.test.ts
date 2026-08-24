// slowTracesHref — v0.9.961 (UX denetimi G4 / Ö7).
//
// Korunan karar: BİRİM ADDAN kanıtlanmıyorsa link ÇİZİLMEZ.
// problem.threshold metriğin kendi biriminde, /traces `minMs`i
// milisaniye okur. İkisini kanıtsız bağlamak bu deponun en pahalı hata
// sınıfı (v0.6.36 değer+birim şablonları). Saniye cinsinden bir eşiği
// milisaniye sanmak listeyi sessizce 1000× yanlış kıstırır ve sonuç
// "yavaş trace yok" diye okunur — hiç link olmamasından beter.
//
// CANLI VOKABÜLER (2026-08-11, lokal): evaluator'ın gecikme metrikleri
// http_p99_ms / db_p99_ms / mq_consume_p99_ms; problem gövdesi eşiği
// "threshold > 3000.00ms" diye yazıyor.

import { describe, it, expect } from 'vitest';
import { latencyThresholdMs, slowTracesHref } from './slowTracesHref';

describe('latencyThresholdMs', () => {
  it('_ms ile biten metrikler gecikme sayılır', () => {
    expect(latencyThresholdMs({ metric: 'http_p99_ms', threshold: 3000 })).toBe(3000);
    expect(latencyThresholdMs({ metric: 'db_p99_ms', threshold: 2500 })).toBe(2500);
    expect(latencyThresholdMs({ metric: 'mq_consume_p99_ms', threshold: 120000 })).toBe(120000);
  });

  it('gecikme OLMAYAN metrikte null', () => {
    expect(latencyThresholdMs({ metric: 'error_rate', threshold: 5 })).toBeNull();
    expect(latencyThresholdMs({ metric: 'request_rate', threshold: 100 })).toBeNull();
    expect(latencyThresholdMs({ metric: 'db.capacity', threshold: 90 })).toBeNull();
  });

  it('birimi SÖYLEMEYEN adlar dışarıda — saniye/ms karışması yasak', () => {
    // Bunlar gecikme OLABİLİR ama birimi ad kanıtlamıyor; tahmin etmek
    // 1000× yanlış bir minMs demek.
    expect(latencyThresholdMs({ metric: 'latency', threshold: 3 })).toBeNull();
    expect(latencyThresholdMs({ metric: 'duration', threshold: 3 })).toBeNull();
    expect(latencyThresholdMs({ metric: 'p99_seconds', threshold: 3 })).toBeNull();
    expect(latencyThresholdMs({ metric: 'p99_msec', threshold: 3 })).toBeNull();
  });

  it('eşik yoksa/anlamsızsa null — minMs=0 filtresiz liste demek olurdu', () => {
    expect(latencyThresholdMs({ metric: 'http_p99_ms', threshold: 0 })).toBeNull();
    expect(latencyThresholdMs({ metric: 'http_p99_ms', threshold: -1 })).toBeNull();
    expect(latencyThresholdMs({ metric: 'http_p99_ms', threshold: NaN })).toBeNull();
    expect(latencyThresholdMs({ metric: '', threshold: 3000 })).toBeNull();
    // Tip `metric`i zorunlu diyor ama ESKİ bir problem satırı alanı hiç
    // taşımayabilir; helper'ın ?? '' savunması o çalışma-zamanı hâli için.
    expect(latencyThresholdMs({ metric: undefined as unknown as string, threshold: 3000 })).toBeNull();
  });
});

// v0.9.1331 — bu blok ÖNCE YALANI ÇİVİLİYORDU: `{fromNs: 111, toNs: 222}`
// veriyor ve `range=custom:111-222` bekliyordu, yani imzanın nanosaniye
// dediği değerin milisaniye olarak basıldığını doğru davranış sayıyordu.
// Doğru bir yeniden yazım o testi kırar ve REGRESYON gibi görünürdü.
// Artık gerçek nanosaniye giriyor ve dönüşümü `windowRangeParam` yapıyor.
const FROM_NS = 1_700_000_000_000_000_000; // 2023-11-14T22:13:20Z
const TO_NS   = 1_700_000_060_000_000_000; // +60 sn

describe('slowTracesHref', () => {
  it('servis + minMs + problem penceresi; rootOnly KAPALI', () => {
    const href = slowTracesHref('api-gateway', 3000, { fromNs: FROM_NS, toNs: TO_NS });
    expect(href).toContain('service=api-gateway');
    expect(href).toContain('minMs=3000');
    // Yavaş span kök olmak zorunda değil (DB çağrısı / downstream RPC);
    // /traces varsayılanı kök-only ve v0.8.585'te hata pivotu tam bu
    // yüzden operatör raporuyla düzeltilmişti.
    expect(href).toContain('rootOnly=false');
    expect(href).toContain('range=custom%3A1700000000000-1700000060000');
  });

  it('kesirli eşik yuvarlanır — URL gürültüsü taşımaz', () => {
    expect(slowTracesHref('svc', 3573.4759511999923, { fromNs: FROM_NS, toNs: TO_NS }))
      .toContain('minMs=3573');
  });

  // windowRangeParam'ın iki kuralı bu çağrı üzerinden de çivileniyor —
  // elle string kurarken ikisi de kayıptı.
  it('yuvarlama pencereyi DARALTMAZ: from floor, to CEIL', () => {
    // to ucunda yarım ms fazlalık: from aynı ms'te kalır (floor), to bir
    // SONRAKİ ms'e taşar (ceil). Kesilen bir `to` en yeni kovayı
    // düşürürdü, yani operatörün görmeye geldiği yarıyı.
    //
    // ⚠ Neden yarım ms, neden +1 ns DEĞİL: 1,7e18 nanosaniye
    // Number.MAX_SAFE_INTEGER'ı (9,007e15) ~190× aşıyor ve o büyüklükte
    // float64'ün adım aralığı 256 ns. `TO_NS + 1` ile yazdığım ilk hâli
    // BU YÜZDEN geçmedi: +1 sessizce kayboldu ve ceil hiç ısırmadı.
    // Gerçekçi ns damgalarında sub-µs delta ile test yazılamaz — bu,
    // testin değil JavaScript sayı tipinin sınırı.
    const href = slowTracesHref('svc', 1, { fromNs: FROM_NS, toNs: TO_NS + 500_000 });
    expect(href).toContain('range=custom%3A1700000000000-1700000060001');
  });

  it('reddedilen pencerede range parametresi HİÇ yazılmaz', () => {
    // Epoch altı / ters pencere: decodeRange bunu reddeder. Boş yazmak
    // adres çubuğunda kendinden emin ama yanlış bir pencere gösterirdi;
    // atlamak sayfayı sticky pencereye düşürür (serviceHref.ts:67 kuralı).
    const href = slowTracesHref('svc', 1, { fromNs: 0, toNs: 0 });
    expect(href).not.toContain('range=');
    expect(href).toContain('service=svc');
  });

  // ⚠ KALAN RİSK, açıkça çivilenmiş. Ad artık dürüst ve tip her link
  // üreticisiyle aynı (`TimeRange | {fromNs,toNs}`), ama biri hâlâ
  // MİLİSANİYE geçerse muhafaza onu yakalamaz: değerler pozitif ve artan
  // olduğu için `windowRangeParam` reddetmez. Yani düzeltme "adı doğru
  // yaptı", "yanlış birimi imkânsız kılmadı".
  //
  // Ve sonuç tahmin ettiğimden KÖTÜ: 60 saniyelik bir pencere 1970'e
  // gitmekle kalmıyor, 1 MİLİSANİYEYE çöküyor (1700000 → 1700001).
  // Yani /traces kesin boş döner ve bu "yavaş trace yok" diye okunur —
  // bu dosyanın §BİRİM şerhinin yazılma nedeni olan arıza şeklinin ta
  // kendisi. Gizlemek yerine çiviliyoruz: bu test bir gün kırılırsa,
  // koruma GERÇEKTEN eklenmiş demektir.
  it('ms geçilirse pencere 1 ms.ye ÇÖKER (korunmayan artık risk)', () => {
    const href = slowTracesHref('svc', 1, { fromNs: 1_700_000_000_000, toNs: 1_700_000_060_000 });
    expect(href).toContain('range=custom%3A1700000-1700001');
  });
});
