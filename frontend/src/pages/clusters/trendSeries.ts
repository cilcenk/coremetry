import type { SpanMetricSeries, ClusterPodTrendPoint, ClusterPodSeriesTrend } from '@/lib/types';
import type { Threshold } from '@/components/MultiLineChart';

// trendSeries — Thanos trend verisini MultiLineChart'ın beklediği
// şekillere çeviren PAYLAŞILAN saf yardımcılar (v0.9.4, trend-upgrade
// audit §2.1). Tekil pod ve multi-pod aynı dönüştürücüden geçer —
// iki tutarsız implementasyon oluşamaz. bucket unix SANİYE →
// time unix NANOSANİYE (SpanMetricSeries sözleşmesi).

// thanosTrendToSeries — tekil trend → tek seri.
export function thanosTrendToSeries(
  trend: ClusterPodTrendPoint[],
  label: string,
  pick: (t: ClusterPodTrendPoint) => number,
): SpanMetricSeries[] {
  if (!trend.length) return [];
  return [{
    groupKey: [label],
    points: trend.map(t => ({ time: t.bucket * 1e9, value: pick(t) })),
  }];
}

// thanosPodSeriesToSeries — pod başına seri (multi-pod görünüm).
// Sunucu top-10'a kesmiş halde yollar; sıra korunur (ortalama CPU
// desc — legend en yoğun pod'la başlar).
export function thanosPodSeriesToSeries(
  pods: ClusterPodSeriesTrend[],
  pick: (t: ClusterPodTrendPoint) => number,
): SpanMetricSeries[] {
  return pods
    .filter(p => p.trend.length > 0)
    .map(p => ({
      groupKey: [p.pod],
      points: p.trend.map(t => ({ time: t.bucket * 1e9, value: pick(t) })),
    }));
}

// limitThresholds — limit (err) + request (warn) yatay referans
// çizgileri. 0/undefined = bilinmiyor → çizgi yok (kurulu "0 =
// bilinmiyor" sözleşmesi; boş grafik yanlış sıfır çizgisi basmaz).
export function limitThresholds(limit?: number, request?: number, unit = ''): Threshold[] {
  const out: Threshold[] = [];
  if (limit && limit > 0) {
    out.push({ value: limit, label: `limit${unit ? ` (${unit})` : ''}`, severity: 'err' });
  }
  if (request && request > 0) {
    out.push({ value: request, label: `request${unit ? ` (${unit})` : ''}`, severity: 'warn' });
  }
  return out;
}
