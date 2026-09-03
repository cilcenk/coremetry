// thresholdLines.ts — v0.10.314 (chart-layer audit Dilim 2.2): yatay eşik
// çizgilerinin TEK kapısı. İki kaynak, tek çıktı (ChartThreshold):
//   • severity eşikleri (SLO hedefi, alarm eşiği, pod limit/request —
//     TrendPanel `limitThresholds`, failureSlo) → warn/err token'ı;
//   • dashboard bant eşikleri (PanelEditor ThresholdEditor'ün green/amber/red
//     basamakları; stat/gauge zaten boyuyor) → amber = warn, red = err çizgi;
//     green TABAN banttır, çizgi basılmaz (0 değerinde anlamsız çizgi olurdu).
// Renkler token (var(--warn)/var(--err)) — tema ile birlikte çözülür; hex yok.
// Saf; React yok.
import type { ChartThreshold } from '@/lib/chart/overlays';
import type { PanelThresholdBand } from '@/lib/types';

// Threshold — horizontal line at a y-value, optionally coloured by
// severity. v0.9.x'ten beri MultiLineChart'ta yaşıyordu; oradan tip olarak
// yeniden dışa aktarılır (failureSlo / trendSeries importları kırılmaz).
export interface Threshold {
  value: number;
  label?: string;            // e.g. "SLO 500ms"
  severity?: 'warn' | 'err'; // default 'warn'
}

export function severityThresholdLines(ts?: Threshold[]): ChartThreshold[] {
  return (ts ?? []).map(t => ({
    value: t.value, label: t.label,
    color: t.severity === 'err' ? 'var(--err)' : 'var(--warn)',
  }));
}

export function bandThresholdLines(bands?: PanelThresholdBand[]): ChartThreshold[] {
  return (bands ?? [])
    .filter(b => b.color !== 'green' && Number.isFinite(b.value))
    .sort((a, b) => a.value - b.value)
    .map(b => ({
      value: b.value,
      label: b.color === 'red' ? 'red ≥' : 'amber ≥',
      color: b.color === 'red' ? 'var(--err)' : 'var(--warn)',
    }));
}
