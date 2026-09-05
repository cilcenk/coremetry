// v0.9.794 (operatör-raporlu regresyon) — Overview'ün metrik panellerinden
// Metrics sayfasına giden tıkların KIRILIMI taşıması.
//
// OLAY: v0.9.774'te Response time paneli "avg BY http.route" çizmeye geçti
// (başlığı bile öyle diyor) ama onExpandClick hâlâ `metricsHref()` — yani
// `by`sız — çağırıyordu. Tık, route kırılımı olmayan tek toplam çizgiye
// açılıyordu; operatör "üstüne basınca by http.route grafiğini göstermiyor,
// eskiden gösteriyordu" diye bildirdi. Throughput paneli aynı dosyada DOĞRU
// deseni taşıyordu, yani kusur bir unutmaydı — ve 774'ün self-review'unda
// işaretlenmişti.
//
// KAPI KAYNAK TARAMASIDIR: Overview.tsx bir sayfa bileşeni (fetch + router +
// uPlot); saf bir fonksiyona çıkarılacak bir "href kararı" yok — metricsHref
// zaten tek satırlık ve doğru, hata ÇAĞRI YERİNDEYDİ. Bu yüzden korunan şey
// çağrı yeri: v2 metrik panellerinin HER onExpandClick'i bir `by` taşımalı.
// Regresyon buradan geri sızarsa test kızarır.

import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const src = readFileSync(resolve(__dirname, './Overview.tsx'), 'utf8')
  .replace(/\/\/.*$/gm, '');

describe('Overview → Metrics tık hedefi (v0.9.794 + v0.10.370)', () => {
  it('🔴 hiçbir onExpandClick çıplak metricsHref() çağırmaz', () => {
    const bare = src.match(/navigate\(metricsHref\(\)\)/g) ?? [];
    expect(bare, 'by taşımayan metricsHref() çağrısı').toEqual([]);
  });

  it('Response time paneli route kırılımlı, rtMetric + avg ile açılır', () => {
    const i = src.indexOf('ov-response-time-metric-v2');
    expect(i).toBeGreaterThan(0);
    const near = src.slice(i, i + 400);
    expect(near).toMatch(/onExpandClick=\{\(\) => navigate\(rtExploreHref\(\)\)\}/);
    expect(src).toMatch(/const rtExploreHref = \(\) => metricsHref\(\{ by: 'http\.route', metric: metricName, agg: 'avg' \}\);/);
  });

  // v0.10.370 — operator-reported: "Overview'daki metrik grafiği artan trend
  // gösterirken üstüne basıp açılan Explore grafiği dümdüz". Kapı `_count`
  // sayacını `avg` ile açıyordu (kümülatif sayacın ortalaması, eksen
  // "4.63 days"). Throughput = _count + RATE; ad soruya göre çözülür
  // (v0.9.1274 dersinin kapı hâli).
  it('🔴 Throughput paneli _count adını RATE ile açar, avg ile değil', () => {
    expect(src).toMatch(/const tputExploreHref = \(\) => metricsHref\(\{ by: 'http\.route', metric: metricTputQ\.data\?\.metric \?\? '', agg: 'rate' \}\);/);
    expect(src).toMatch(/onExpandClick=\{\(\) => navigate\(tputExploreHref\(\)\)\}/);
  });

  it('iki panel AYNI kırılımı taşır', () => {
    const calls = src.match(/metricsHref\(\{ by: '([^']+)'/g) ?? [];
    expect(calls.length).toBe(2);
    expect(new Set(calls).size, 'iki panel aynı kırılımı kullanmalı').toBe(1);
  });

  it('metricsHref `by` ve `agg` parametrelerini gerçekten URL\'e yazar', () => {
    expect(src).toMatch(/opts\.by \? `&by=\$\{encodeURIComponent\(opts\.by\)\}` : ''/);
    expect(src).toMatch(/`&agg=\$\{opts\.agg\}`/);
    // agg'siz bir kapı = Metrics sayfasının varsayılanı (avg) = sayaç için yanlış.
    expect(src).not.toMatch(/metricsHref\(\{ by: 'http\.route' \}\)/);
  });
});
