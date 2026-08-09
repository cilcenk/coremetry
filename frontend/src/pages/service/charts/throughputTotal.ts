// throughputTotal.ts — Overview Throughput kartının "Toplam" çizgisini üreten
// saf çekirdek.
//
// v0.9.845 — `sumNullableSeries` SİLİNDİ. İNDEKS hizalı (dizi + dizi) toplamdı
// ve tek tüketicisi eski motorun ChartCard "Toplam" çizgisiydi; o dal
// v0.9.844'te söküldü. Geriye-uyum şimi bırakmıyoruz (CLAUDE.md). Kalan tek
// toplayıcı aşağıdaki sumSeries: ZAMAN anahtarlı, yani indeks kaymasına
// yapısal olarak bağışık — v2 panelleri zaten böyle istiyor.
//
// Boşluk (null) semantiği bilinçli: null = veri yok, grafikte GAP çizilir —
// uydurma 0 değil. NaN/Infinity boş sayılır (seriesStats ile aynı kural).

function num(v: number | null | undefined): number | null {
  return v == null || !isFinite(v) ? null : v;
}

// ── sumSeries (v0.9.798) ────────────────────────────────────────────────
//
// Throughput panelinin "Toplam" çizgisi: route serilerinin İSTEMCİ
// tarafında toplamı. EK SORGU YOK ve olmamalı — rate TOPLANABİLİR bir
// büyüklük (istek/sn), yani route'ların toplamı servis geneline eşittir.
//
// Response time'ın "Toplam"ı bunun TERSİ ve o yüzden ayrı bir sorgu
// atıyor: ortalama toplanamaz, route ortalamalarının ortalaması
// gözlem sayısını yok sayar (v0.9.776'nın düzelttiği hatanın istemci
// tarafındaki ikizi olurdu).
//
// BOŞLUK SEMANTİĞİ, sumNullableSeries ile aynı doktrin:
//   • Hiçbir seride o bucket YOKSA → nokta ÜRETİLMEZ (gap, uydurma 0
//     değil). Sıfır basmak, veri gelmeyen bir dakikayı "trafik durdu"
//     diye çizmek olurdu.
//   • Bazı serilerde varsa → VAR OLANLARIN toplamı. Eksik seriyi 0
//     saymak da 0 basmaktır; atlanır.
//   • NaN / Infinity boş sayılır.
//
// Zaman ızgarası serilerin BİRLEŞİMİ ve artan sıralı: paneller
// (framesToAligned) sıralı x bekler ve seriler farklı bucket'larda
// başlayıp bitebilir.

import type { SpanMetricSeries } from '@/lib/types';

export function sumSeries(series: readonly SpanMetricSeries[] | null | undefined): SpanMetricSeries {
  const byTime = new Map<number, number>();
  for (const s of series ?? []) {
    for (const p of s.points ?? []) {
      const v = num(p?.value);
      if (v == null) continue;
      byTime.set(p.time, (byTime.get(p.time) ?? 0) + v);
    }
  }
  const times = [...byTime.keys()].sort((a, b) => a - b);
  return { groupKey: [], points: times.map(t => ({ time: t, value: byTime.get(t) as number })) };
}
