// VolumeChart — Span Volume, now a thin adapter over the shared <TimeChart>
// primitive (v0.8.91; was a hand-drawn DOM/SVG chart). Keeps its identity (card
// + legend above the bars + "spans / Nm bucket" note) but delegates the axes,
// gridlines, hover crosshair/tooltip, deploy-free brush and Canvas rendering to
// TimeChart. ok-span bars (accent) with the error share overlaid red at the
// bottom + a p50 latency line. Drag to brush a time range.
//
// v0.9.843 (operatör isteği) — EKSEN TAKASI, Grafana düzeni: SÜRE (p50
// çizgisi) SOL eksende, SPAN SAYISI (bar'lar) SAĞ eksende. Eşleme +
// biçimlendirici sözleşmesi volumeSeries.ts'te, tablo-testli.

import { useMemo } from 'react';
import type { SpanMetricSeries } from '@/lib/types';
import { TimeChart } from '@/components/charts/TimeChart';
import { buildVolumeSeries, fmtVolumeDuration } from './volumeSeries';

export function VolumeChart({
  count, errors, p50, height = 140, onBrush, onZoomReset, xRange, header, headerRight, unit = 'traces',
}: {
  count: SpanMetricSeries[] | null;
  errors: SpanMetricSeries[] | null;
  p50: SpanMetricSeries[] | null;
  height?: number;
  onBrush?: (fromMs: number, toMs: number) => void;
  // v0.9.390 (Faz A-3) — çift-tık = brush'ı geri al; TimeChart'ın mevcut
  // dblclick altyapısına aynen iletilir. Verilmezse eski davranış.
  onZoomReset?: () => void;
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
  // v0.10.268 — çubuk birimi ("traces" | "requests"), volumeUnitLabel.
  unit?: string;
}) {
  const { times, series, bucketMin } = useMemo(
    () => buildVolumeSeries(count, errors, p50, unit),
    [count, errors, p50, unit],
  );

  return (
    // v0.9.301 — tighter card. The chart is the brush/overview TOOL for
    // the table below it (this file has said so since v0.9.246); at
    // 12px padding + 10px margin around a 140px plot it read as the
    // headline instead, and the trace rows — the point of the page —
    // started below the fold. Operator-reported.
    <div style={{ background: 'var(--bg2)', border: '1px solid var(--border)', borderRadius: 8, padding: '8px 10px', marginBottom: 8 }}>
      {/* v0.9.103 (Grafana-parity #1) — renk-anahtarı kaldırıldı; TimeChart
          artık altında StatsLegend (swatch+label+istatistik) gösteriyor.
          Yalnız bucket/sürükle ipucu üstte kalır (StatsLegend'de yok). */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap', marginBottom: 4, fontSize: 10.5, color: 'var(--text-faint)' }}>
        {header}
        <span style={{ fontFamily: 'var(--font-mono, ui-monospace)' }}
          title="Giriş span'leri (server/consumer) sayılır: servis seçiliyken istek = trace; servissiz pencerede her hop bir kez sayılır.">
          {unit} / {bucketMin}m bucket · sürükle = zaman seç</span>
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
          onZoomReset={onZoomReset}
          // v0.10.268 — Dynatrace düzeni: SAYIM sol eksende (fmtLeft
          // verilmez → TimeChart'ın kısaltması "30.9k"), SÜRE sağ eksende
          // (fmtRight = ms/s biçimlendirici). v0.9.843 takası geri alındı;
          // biçimlendirici eksenle birlikte taşındı (sayıya "ms" yazma tuzağı).
          fmtRight={fmtVolumeDuration}
          xRange={xRange}
          // v0.10.321 (operatör, prod ekran görüntüsü: "Series paneli kapalı
          // olsun. Shrink mode gelsin.") — lejant VARSAYILAN KAPALI: şerit
          // sayfanın aracı, tablo sayfanın kendisi; açık lejant ~150 px
          // yiyip tabloyu katlanın altına itiyordu. v0.10.268'in "açık"
          // kararı geri alındı; ▶ Series (3) tıkla açılır (oturumluk).
          // İnce çubuk + boşluk (0.62).
          legendCollapsed={true}
          barSize={0.62}
        />
      )}
    </div>
  );
}
