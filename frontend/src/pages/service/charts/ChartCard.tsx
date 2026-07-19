import { useMemo } from 'react';
import { Spinner } from '@/components/Spinner';
import type { SpanMetricSeries } from '@/lib/types';
import { OverviewChart, type OvChartSeries } from './OverviewChart';

// ChartCard (v0.9.87'de Overview.tsx'ten çıkarıldı — Runtime paneli de
// kullanır). Accepts N lines over the same x axis (Response time draws
// p50/p95/p99 as three lines; throughput/failure draw one). The time axis
// comes from the first non-empty line — CALLER, tüm çizgilerin AYNI bucket
// kümesine hizalı olmasını garanti eder (RED: tek batch; Runtime paneli:
// alignToUnion). Hizasız seriler index-kaymasıyla yanlış zamana çizilir.
export interface ChartLine { series: SpanMetricSeries[]; color: string; label: string }

export function ChartCard({ title, lines, unit, mode = 'line', deploy, status = 'ready', onZoom, xRange }: {
  title: string; lines: ChartLine[]; unit: string;
  mode?: 'line' | 'area' | 'stacked'; deploy?: { sec: number; label: string } | null;
  // RED series fetch state — distinguishes loading/error from a genuinely
  // empty window so a failed metric fetch doesn't masquerade as "No data".
  status?: 'loading' | 'error' | 'ready';
  // v0.8.534 — threaded to OverviewChart for drag-zoom → global range.
  onZoom?: (fromSec: number, toSec: number) => void;
  // v0.9.83 — sorgu penceresi (unix sec): x-ekseni pencereye sabitlenir.
  xRange?: { from: number; to: number } | null;
}) {
  const times = useMemo(() => {
    const base = lines.find(l => (l.series[0]?.points ?? []).length)?.series[0]?.points ?? [];
    return base.map(p => p.time / 1e9);
  }, [lines]);
  const ovSeries = useMemo<OvChartSeries[]>(() =>
    lines
      .map(l => ({ label: l.label, color: l.color, data: (l.series[0]?.points ?? []).map(p => p.value) }))
      .filter(s => s.data.length),
  [lines]);
  return (
    <div className="card">
      <div className="ov-card-h">
        <h3>{title}</h3>
        <div className="ov-right">
          <span className="ov-legend">
            {lines.map(l => <span key={l.label}><i className="ov-sw" style={{ background: l.color }} />{l.label}</span>)}
          </span>
        </div>
      </div>
      <div className="ov-card-b" style={{ paddingTop: 10, paddingBottom: 10 }}>
        {times.length < 2 ? (
          <div style={{ height: 150, display: 'grid', placeItems: 'center',
            color: status === 'error' ? 'var(--err)' : 'var(--text3)', fontSize: 12 }}>
            {status === 'loading' ? <Spinner />
              : status === 'error' ? 'Failed to load metrics'
              : 'No data in this window'}
          </div>
        ) : (
          <OverviewChart times={times} series={ovSeries} unit={unit} mode={mode}
            deployAtSec={deploy?.sec ?? null} deployLabel={deploy?.label} onZoom={onZoom} xRange={xRange} />
        )}
      </div>
    </div>
  );
}
