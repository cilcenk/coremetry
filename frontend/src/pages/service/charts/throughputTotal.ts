// throughputTotal.ts (v0.9.483, operatör: "throughput yığılmış alan yerine
// çizgi olsun") — Overview Throughput kartının "Toplam" çizgisini üreten saf
// çekirdek.
//
// Yığılmış alan kalktığı için toplam artık GÖRSEL olarak (bantların üst
// kenarı) değil, VERİ olarak çizilmeli: Toplam = OK + Errors, eleman-eleman.
//
// Boşluk (null) semantiği bilinçli ve OVC sözleşmesiyle aynı (OvChartSeries:
// null = veri yok, grafikte GAP çizilir — uydurma 0 değil):
//   null + null = null   → iki seride de veri yoksa toplam da yok
//   null + x    = x      → bir seri boşsa diğerinin değeri toplamdır
//   x    + y    = x + y
// Dizi boyları farklıysa uzun olana göre hizalanır, eksik taraf null sayılır
// (kısa dizi sessizce toplamı KIRPMAZ — kırpma x eksenini kaydırırdı).
// NaN/Infinity boş sayılır (seriesStats ile aynı kural).

export function sumNullableSeries(
  a: ReadonlyArray<number | null | undefined>,
  b: ReadonlyArray<number | null | undefined>,
): (number | null)[] {
  const n = Math.max(a.length, b.length);
  const out: (number | null)[] = new Array(n);
  for (let i = 0; i < n; i++) {
    const x = num(a[i]);
    const y = num(b[i]);
    out[i] = x == null ? y : y == null ? x : x + y;
  }
  return out;
}

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
