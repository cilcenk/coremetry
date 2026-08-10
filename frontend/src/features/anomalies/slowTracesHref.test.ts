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

describe('slowTracesHref', () => {
  it('servis + minMs + problem penceresi; rootOnly KAPALI', () => {
    const href = slowTracesHref('api-gateway', 3000, { fromNs: 111, toNs: 222 });
    expect(href).toContain('service=api-gateway');
    expect(href).toContain('minMs=3000');
    // Yavaş span kök olmak zorunda değil (DB çağrısı / downstream RPC);
    // /traces varsayılanı kök-only ve v0.8.585'te hata pivotu tam bu
    // yüzden operatör raporuyla düzeltilmişti.
    expect(href).toContain('rootOnly=false');
    expect(href).toContain('range=custom%3A111-222');
  });

  it('kesirli eşik yuvarlanır — URL gürültüsü taşımaz', () => {
    expect(slowTracesHref('svc', 3573.4759511999923, { fromNs: 1, toNs: 2 }))
      .toContain('minMs=3573');
  });
});
