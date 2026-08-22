// @vitest-environment jsdom
//
// MetricThroughputNote × kaynak rozeti — v0.9.1268 (operatör-raporlu,
// ekran görüntülü).
//
// SEMPTOM: servis Overview'unda "Throughput · metrik
// (http.server.request.duration)" paneli "bu servise eşleşen seri yok"
// diyordu. Metrik VictoriaMetrics'te VARDI ve AYNI metriği okuyan soldaki
// avg-by-route paneli çiziyordu.
//
// KÖK NEDEN backend'deydi (eşleyici s.metricSourceFor'u görmüyordu, ClickHouse'a
// çakılıydı) ve orada düzeltildi. BURADA ölçülen, hatanın neden bu kadar uzun
// süre GÖRÜNMEZ kaldığı: not nerede aradığını söylemiyordu. "Seri yok" ile
// "yanlış depoda arandı" ekranda birbirinin aynısıydı, ve ikisi bambaşka
// eylem istiyor.
//
// Bu yüzden rozet ÜÇ dalın da testi. Not zaten yalnız seri boşken çiziliyor,
// yani rozetin göründüğü her an tam olarak teşhisin gerektiği an; bir dalda
// düşerse tam o dalda aynı körlük geri gelir.

import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MetricThroughputNote } from './MetricThroughputNote';
import type { ServiceMetricThroughput } from '@/lib/types';

let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  host = document.createElement('div');
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

function render(d: ServiceMetricThroughput): string {
  act(() => root.render(<MetricThroughputNote d={d} />));
  return host.textContent ?? '';
}

/** base — the "metric exists, nothing matched" branch, the operator's case. */
function base(over: Partial<ServiceMetricThroughput> = {}): ServiceMetricThroughput {
  return {
    service: 'bsa-deposit-uat',
    metric: 'http.server.request.duration',
    jobLabel: 'job',
    pattern: '^(.*/)?(bsa-deposit-uat|bsa-deposit)$',
    metricExists: true,
    matched: 0,
    ...over,
  };
}

describe('MetricThroughputNote — kaynak rozeti', () => {
  it('üç dalın ÜÇÜNDE de hangi depoda arandığını yazar', () => {
    // 1) eşleşme yok (operatörün gördüğü dal)
    expect(render(base({ source: 'vm' }))).toContain('VictoriaMetrics');
    // 2) metrik hiç yok
    expect(render(base({ metricExists: false, source: 'vm' }))).toContain('VictoriaMetrics');
    // 3) instrument desteklenmiyor
    expect(render(base({ unsupportedInstrument: true, instrument: 'gauge', source: 'vm' })))
      .toContain('VictoriaMetrics');
  });

  it('ClickHouse cevabında ClickHouse yazar — rozet sabit metin DEĞİL', () => {
    const txt = render(base({ source: 'ch' }));
    expect(txt).toContain('ClickHouse');
    // Ayırt edici: rozet gerçekten `source`u okuyor mu. Sabitlenmiş bir
    // "VictoriaMetrics" dizesi yukarıdaki testi de geçerdi ve tam olarak
    // düzelttiğimiz yanlış-depo yalanını üretirdi.
    expect(txt).not.toContain('VictoriaMetrics');
  });

  it('source YOKKEN rozet çizilmez — varsayılan UYDURMAZ', () => {
    // Rolling deploy: alanı bilmeyen eski bir pod'a düşen istek. "ClickHouse'ta
    // arandı" varsaymak, düzeltmenin kendisinin yalan söyleyebildiği tek yol
    // olurdu — bilmemek, yanlış bilmekten iyidir.
    const txt = render(base({}));
    expect(txt).not.toContain('ClickHouse');
    expect(txt).not.toContain('VictoriaMetrics');
    // Ama notun kendisi yine çiziliyor: rozet bir EK, ön koşul değil.
    expect(txt).toContain('eşleşen seri yok');
  });

  it('tanılama gövdesi rozetle birlikte KORUNUYOR', () => {
    // Rozet eklerken üç dalın içeriğini bozmadığımızın kanıtı: denenen
    // etiketler, var olan anahtarlar ve örnek job değerleri hâlâ basılıyor.
    // Bunlar "collector'ı düzelt" ile "deseni düzelt" arasındaki farkı
    // gösteren asıl bilgi (v0.9.682).
    const txt = render(base({
      source: 'vm',
      triedLabels: ['k8s_deployment_name', 'service_name'],
      presentKeys: ['job'],
      sampleJobs: ['deposit/bsa-deposit-uat'],
      sampleServices: ['bsa-deposit-uat'],
    }));
    expect(txt).toContain('k8s_deployment_name');
    expect(txt).toContain('service_name');
    expect(txt).toContain('deposit/bsa-deposit-uat');
  });
});
