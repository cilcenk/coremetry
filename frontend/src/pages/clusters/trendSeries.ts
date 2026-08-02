import type { SpanMetricSeries, ClusterPodTrendPoint, ClusterPodSeriesTrend, ClusterNetTrendPoint, ClusterNamedSeries } from '@/lib/types';
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

// netTrendToSeries — cluster throughput trendi → in/out iki seri
// (v0.9.10, Overview grafiği). Aynı saniye→ns sözleşmesi.
export function netTrendToSeries(trend: ClusterNetTrendPoint[]): SpanMetricSeries[] {
  if (!trend.length) return [];
  return [
    { groupKey: ['Net in (B/s)'], points: trend.map(t => ({ time: t.bucket * 1e9, value: t.inBps })) },
    { groupKey: ['Net out (B/s)'], points: trend.map(t => ({ time: t.bucket * 1e9, value: t.outBps })) },
  ];
}

// namedSeriesToSeries — ResourceTrend NamedSeries[] → MultiLineChart
// (v0.9.35). Boş ad → verilen fallback etiketi (total modda "CPU"
// gibi). Aynı saniye→ns sözleşmesi.
export function namedSeriesToSeries(
  series: ClusterNamedSeries[],
  fallbackLabel: string,
  // v0.9.539 — lejant kısaltma öneki (operatör: "lejantta isim
  // gösterimleri Grafana gibi olabilir mi?"). Grafana'da seri adları
  // kısa ("lckhsdbp04"); bizde tam pod adı geliyordu
  // ("mobile-crm-dashboard-bff-57bdc7975b-59c8q", 41 karakter) ve
  // lejant üç sütuna taşıp grafiği aşağı itiyordu. Ortak önek
  // (deployment adı) HER seride aynı olduğu için ayırt edici DEĞİL —
  // atılınca "57bdc7975b-59c8q" kalır, okunur ve Grafana yoğunluğuna
  // yaklaşır. Verilmezse davranış bayt-bayt eskisi.
  trimPrefix?: string,
): SpanMetricSeries[] {
  return series
    .filter(s => s.points.length > 0)
    .map(s => ({
      groupKey: [shortSeriesLabel(s.name, fallbackLabel, trimPrefix)],
      points: s.points.map(p => ({ time: p.bucket * 1e9, value: p.value })),
    }));
}

// shortSeriesLabel — saf, tablo-testli. Önek YALNIZ tam segment
// sınırında atılır ("<önek>-" ile başlıyorsa); kısmi ad çakışması
// ("mobile-crm" öneki "mobile-crmx-..." pod'unu kırpmaz) yanlış etiket
// üretirdi. Kırpma sonrası boş kalırsa ham ad korunur — etiketsiz seri
// lejantta ayırt edilemez.
export function shortSeriesLabel(name: string, fallback: string, trimPrefix?: string): string {
  const n = (name || '').trim();
  if (!n) return fallback;
  if (trimPrefix) {
    const p = trimPrefix + '-';
    if (n.startsWith(p) && n.length > p.length) return n.slice(p.length);
  }
  return n;
}
