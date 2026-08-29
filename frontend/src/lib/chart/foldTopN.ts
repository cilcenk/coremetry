// foldTopN — "others" katlamasının SAF çekirdeği (v0.9.807).
//
// Gövde MultiLineChart.tsx'in eski uPlot gövdesinden ALINDI, yeniden
// yazılmadı. Sebep bir davranış farkıydı: eski gövde >N seriyi katlıyor,
// v0.9.789'da açılan CorePanel yolu TÜM serileri çiziyordu. Aynı panelin
// iki motoru farklı sayıda seri gösteriyordu — ServiceCharts'ın by-op
// error-rate / latency / RPS üçlüsünde gözle görülür. Eski motor
// v0.9.844'te söküldü; katlama TEK kaynakta kaldı ve MLC sarmalayıcısı
// hâlâ buradan geçiyor.
//
// SÖZLEŞME (eski gövdeden birebir devralındı — İCAT YOK):
//
//   • Sıralama ALAN bazlı: Σ|değer| büyükten küçüğe. null nokta 0 sayılır
//     (Math.abs(p.value ?? 0)), yani delikli bir seri sırf delikleri
//     yüzünden öne geçmez.
//   • Üst N tutulur, kalan tek bir "others" serisine toplanır.
//   • Kuyruk NOKTA BAZINDA birleşir: aynı zaman damgasındaki değerler
//     toplanır. TOPLANABİLİR birimde (rps / count / bytes / birimsiz)
//     toplam doğrudur; ORAN birimlerinde (%, ms, s) toplam anlamsızdır —
//     orada ORTALAMA alınır (o bucket'ta değeri OLAN kuyruk serilerinin
//     sayısına bölünür). Yüzdeleri toplamak yanlış, p95'leri toplamak
//     anlamsız; v1 bu ayrımı zaten yapıyordu ve aynen korunuyor.
//   • null nokta toplama KATILMAZ (0 sayılmaz) — hem toplayanı hem
//     sayacı atlar, yani deliği olan bir kuyruk serisi ortalamayı aşağı
//     çekmez.
//   • Üretilen seri tail[0]'ın alanlarını devralır (yayılım) ve yalnız
//     groupKey + points'i değiştirir: SpanMetricSeries bir gün yeni bir
//     alan kazanırsa "others" onu sessizce kaybetmez.
//   • series.length <= n ise dizi AYNEN (aynı referans) döner — katlama
//     olmayan yolda tek bir nesne bile ayrılmaz.

import type { SpanMetricSeries, TailPoint } from '@/lib/types';

// Katlanmış serinin groupKey'i. Renk kanalı bunu tanır: v1 gövdesi
// mutedGray'e, v2 yolu 'muted' seri ROLÜNE eşler (seriesRoleColor →
// var(--text3)). İki motorda da uzun kuyruk sessiz gri kalır.
export const OTHERS_KEY = 'others';

// Varsayılan tavan. Çağıran yükseltebilir (JBoss datasource panelleri her
// datasource'u ister — v0.9.148 operatör-bildirimi).
export const DEFAULT_MAX_SERIES = 8;

// Oran birimleri: toplanamaz, ortalanır. Liste v1'den birebir.
function isMeanUnit(unit: string | undefined): boolean {
  const u = (unit || '').trim().toLowerCase();
  return u === '%' || u === 'ms' || u === 's';
}

// v0.10.147 — `serverTail`: sunucunun top-N DIŞINDA bıraktığı serilerin
// zaman-başı ham toplam + sayısı (SpanMetricResult.tail). Kuyruk
// birleştirmesine aynen katılır: toplanabilir birimde Σ'ya, oran biriminde
// hem Σ'ya hem sayaca — yani "others" çizgisi ve notu TAM seriyle
// katlamaya eşit (sunucu kırpmasından bağımsız). Sunucu birim BİLMEZ,
// birim kuralı yalnız burada (v0.9.946 tek-çekirdek ilkesi korunur).
// Tail yok/boşsa davranış bayt-bayt eski.
export function foldTopN(
  series: SpanMetricSeries[],
  unit: string | undefined,
  n = DEFAULT_MAX_SERIES,
  serverTail?: readonly TailPoint[],
): SpanMetricSeries[] {
  const hasServerTail = !!serverTail && serverTail.length > 0;
  if (series.length <= n && !hasServerTail) return series;
  const mean = isMeanUnit(unit);
  const ranked = series
    .map(s => ({ s, area: s.points.reduce((a, p) => a + Math.abs(p.value ?? 0), 0) }))
    .sort((a, b) => b.area - a.area);
  const keep = ranked.slice(0, n).map(r => r.s);
  const tail = ranked.slice(n).map(r => r.s);
  const sumByTime = new Map<number, number>();
  const cntByTime = new Map<number, number>();
  for (const s of tail) {
    for (const p of s.points) {
      if (p.value == null) continue;
      sumByTime.set(p.time, (sumByTime.get(p.time) ?? 0) + p.value);
      cntByTime.set(p.time, (cntByTime.get(p.time) ?? 0) + 1);
    }
  }
  if (hasServerTail) {
    for (const t of serverTail!) {
      sumByTime.set(t.time, (sumByTime.get(t.time) ?? 0) + t.sum);
      cntByTime.set(t.time, (cntByTime.get(t.time) ?? 0) + t.count);
    }
  }
  const others: SpanMetricSeries = {
    ...(tail[0] ?? {}),
    groupKey: [OTHERS_KEY],
    // v0.9.1369 — tail[0]'ın fullKey'i MİRAS ALINMAZ. Yayılma operatörü
    // "+N others" çizgisine rastgele bir pod adını tooltip'te yazdırırdı;
    // katlanmış seri tek bir pod DEĞİL.
    fullKey: undefined,
    points: [...sumByTime.entries()]
      .sort((a, b) => a[0] - b[0])
      .map(([time, sum]) => ({ time, value: mean ? sum / (cntByTime.get(time) || 1) : sum })),
  };
  return [...keep, others];
}

// isOthersSeries — bu seri katlamanın ürünü mü? groupKey TAM olarak
// [OTHERS_KEY] olmalı: yalnız ilk anahtara bakmak, ['others', 'checkout']
// gibi gerçek bir grup tuple'ını sessizce gri boyardı.
export function isOthersSeries(s: SpanMetricSeries): boolean {
  return s.groupKey.length === 1 && s.groupKey[0] === OTHERS_KEY;
}

// foldedCount — kaç seri "others"a girdi (0 = katlama yok). Kırpma notunun
// sayısı buradan; grafiğin altındaki tek satır bir TAHMİN değil, katlamanın
// kendi aritmetiğidir.
export function foldedCount(total: number, n = DEFAULT_MAX_SERIES): number {
  return total > n ? total - n : 0;
}

// foldNote — dürüstlük satırı (CorePanel.note). Katlama olmadıysa null:
// boş bir not satırı "bir şey gizlendi" izlenimi verirdi.
export function foldNote(total: number, n = DEFAULT_MAX_SERIES): string | null {
  const f = foldedCount(total, n);
  return f > 0 ? `+${f} seri katlandı (alan bazlı)` : null;
}
