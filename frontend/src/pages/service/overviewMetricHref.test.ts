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

describe('Overview → Metrics tık hedefi (v0.9.794)', () => {
  it('🔴 hiçbir onExpandClick `by`siz metricsHref() çağırmaz', () => {
    // Çıplak metricsHref() — parametresiz çağrı — artık bir kusurdur:
    // panellerin ikisi de kırılımlı çiziyor.
    const bare = src.match(/navigate\(metricsHref\(\)\)/g) ?? [];
    expect(bare, 'by taşımayan metricsHref() çağrısı').toEqual([]);
  });

  it('Response time paneli route kırılımını taşır (operatör bulgusu)', () => {
    const i = src.indexOf('ov-response-time-metric-v2');
    expect(i).toBeGreaterThan(0);
    // storageKey ile aynı JSX satırında/komşuluğunda tık hedefi.
    const near = src.slice(i, i + 400);
    expect(near).toMatch(/onExpandClick=\{\(\) => navigate\(metricsHref\(\{ by: 'http\.route' \}\)\)\}/);
  });

  it('Throughput paneli emsali korunur — iki panel AYNI dilde', () => {
    const calls = src.match(/metricsHref\(\{ by: '([^']+)' \}\)/g) ?? [];
    expect(calls.length).toBe(2);
    expect(new Set(calls).size, 'iki panel aynı kırılımı kullanmalı').toBe(1);
  });

  it('metricsHref `by` parametresini gerçekten URL\'e yazar', () => {
    // Sözleşmenin öteki ucu: çağrı doğru ama fonksiyon yutuyorsa düzeltme
    // görünmez kalırdı.
    expect(src).toMatch(/opts\?\.by \? `&by=\$\{encodeURIComponent\(opts\.by\)\}` : ''/);
  });
});
