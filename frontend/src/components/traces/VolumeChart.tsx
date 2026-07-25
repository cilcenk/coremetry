// VolumeChart — Span Volume, now a thin adapter over the shared <TimeChart>
// primitive (v0.8.91; was a hand-drawn DOM/SVG chart). Keeps its identity (card
// + legend above the bars + "spans / Nm bucket" note) but delegates the axes,
// gridlines, hover crosshair/tooltip, deploy-free brush and Canvas rendering to
// TimeChart. ok-span bars (accent) with the error share overlaid red at the
// bottom + a p50 latency line on the right axis. Drag to brush a time range.

import { useMemo } from 'react';
import type { SpanMetricSeries } from '@/lib/types';
import { TimeChart, type TimeChartSeries } from '@/components/charts/TimeChart';
import { statusColor } from '@/lib/statusColor';

// fmtDurRight — sağ eksen p50 süre formatı (v0.9.73): <1000ms ms,
// aksi s. "3100 ms" gibi okunmaz büyük değerleri "3.1s" yapar; küçük
// gecikmelerde tam sayı ms okunur.
function fmtDurRight(v: number): string {
  if (v >= 1000) return `${(v / 1000).toFixed(v >= 10000 ? 0 : 1)}s`;
  return `${v.toFixed(v < 10 ? 1 : 0)}ms`;
}

export function VolumeChart({
  count, errors, p50, height = 140, onBrush, xRange, header, headerRight,
}: {
  count: SpanMetricSeries[] | null;
  errors: SpanMetricSeries[] | null;
  p50: SpanMetricSeries[] | null;
  height?: number;
  onBrush?: (fromMs: number, toMs: number) => void;
  // v0.9.83 — sorgu penceresi (unix sec); histogram x-ekseni buna sabitlenir.
  xRange?: { from: number; to: number } | null;
  // v0.9.246 — kartın başlık şeridine gömülen içerik (Volume/Latency anahtarı
  // + pencere istatistikleri). Grafiği kontrol eden düğme grafiğin İÇİNDE
  // durur; daha önce kartın üstünde ayrı bir satırdı ve trace tablosunu
  // aşağı itiyordu.
  header?: React.ReactNode;
  // Aynı şeridin SAĞ ucu — pencere istatistikleri (TOTAL / ERROR SPANS /
  // ERR RATE / P50 MAX). Ayrı slot, çünkü aradaki bucket ipucu ikisinin
  // ortasına giriyor.
  headerRight?: React.ReactNode;
}) {
  const { times, series, bucketMin } = useMemo(() => {
    const cPts = count?.[0]?.points ?? [];
    if (!cPts.length) return { times: [] as number[], series: [] as TimeChartSeries[], bucketMin: 1 };
    const eMap = new Map((errors?.[0]?.points ?? []).map(p => [p.time, p.value]));
    const pMap = new Map((p50?.[0]?.points ?? []).map(p => [p.time, p.value]));
    const t = cPts.map(p => Math.round(p.time / 1e9)); // ns → unix sec
    const total = cPts.map(p => p.value);
    const err = cPts.map(p => Math.min(eMap.get(p.time) ?? 0, p.value));
    // v0.9.73 — p50 GAP'li: örnek olmayan (ya da 0 dönen) bucket'ta
    // null → çizgi tabana çakmaz, gerçek boşluk gösterir. Eski `?? 0`
    // her boş bucket'ı 0ms'e çekip sahte iniş-çıkış üretiyordu.
    const p50d: (number | null)[] = cPts.map(p => {
      const v = pMap.get(p.time);
      return v && v > 0 ? v : null;
    });
    const dt = t.length > 1 ? Math.round((t[1] - t[0]) / 60) : 1;
    // Draw order = overlay order: full bar (accent) first, then the error
    // share (red) on top so it reads at the bottom of each bar; p50 line last.
    const s: TimeChartSeries[] = [
      { key: 'total', label: 'ok spans', data: total, color: 'var(--accent)', type: 'bar', axis: 'left' },
      { key: 'error', label: 'errors', data: err, color: statusColor('error'), type: 'bar', axis: 'left' },
      // v0.9.73 — kalın çizgi + nokta: seyrek p50 örnekleri artık okunur.
      { key: 'p50', label: 'p50 latency', data: p50d, color: 'var(--orange)', type: 'line', axis: 'right', width: 2.2, pointsShow: true },
    ];
    return { times: t, series: s, bucketMin: Math.max(1, dt) };
  }, [count, errors, p50]);

  return (
    <div style={{ background: 'var(--bg2)', border: '1px solid var(--border)', borderRadius: 8, padding: 12, marginBottom: 10 }}>
      {/* v0.9.103 (Grafana-parity #1) — renk-anahtarı kaldırıldı; TimeChart
          artık altında StatsLegend (swatch+label+istatistik) gösteriyor.
          Yalnız bucket/sürükle ipucu üstte kalır (StatsLegend'de yok). */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap', marginBottom: 4, fontSize: 10.5, color: 'var(--text-faint)' }}>
        {header}
        <span style={{ fontFamily: 'var(--font-mono, ui-monospace)' }}>spans / {bucketMin}m bucket · sürükle = zaman seç</span>
        {headerRight && (
          <span style={{ marginLeft: 'auto', display: 'flex', alignItems: 'center', gap: 18, flexWrap: 'wrap' }}>
            {headerRight}
          </span>
        )}
      </div>

      {times.length === 0 ? (
        <div style={{ height, display: 'flex', alignItems: 'center', justifyContent: 'center', color: 'var(--text-faint)', fontSize: 12 }}>
          No traces in view to bucket.
        </div>
      ) : (
        <TimeChart
          times={times}
          series={series}
          height={height}
          leftUnit=""
          rightUnit=""
          onBrush={onBrush}
          fmtRight={fmtDurRight}
          xRange={xRange}
          // v0.9.245 — istatistik tablosu kapalı başlar. Bu sayfada asıl iş
          // ALTTAKİ trace listesi; üç serilik bir istatistik tablosunun
          // listeyi ekranın dışına itmesi yanlış öncelik (operatör raporu).
          // Toggle duruyor, isteyen tek tıkla açıyor.
          legendCollapsed
        />
      )}
    </div>
  );
}
