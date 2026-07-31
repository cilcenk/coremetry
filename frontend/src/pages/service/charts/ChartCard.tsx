import { useMemo } from 'react';
import { Spinner } from '@/components/Spinner';
import type { SpanMetricSeries } from '@/lib/types';
import type { ChartThreshold, ChartTimeRegion } from '@/lib/chart/overlays';
import { OverviewChart, type OvChartSeries } from './OverviewChart';

// ChartCard (v0.9.87'de Overview.tsx'ten çıkarıldı — Runtime paneli de
// kullanır). Accepts N lines over the same x axis (Response time draws
// p50/p95/p99 as three lines; throughput/failure draw one). The time axis
// comes from the first non-empty line — CALLER, tüm çizgilerin AYNI bucket
// kümesine hizalı olmasını garanti eder (RED: tek batch; Runtime paneli:
// alignToUnion). Hizasız seriler index-kaymasıyla yanlış zamana çizilir.
// values (v0.9.483): x eksenine ZATEN hizalı, önceden hesaplanmış değerler.
// SpanMetricSeries.points.value tipi `number` (API sözleşmesi) olduğundan
// türetilmiş bir çizgi boşluk (null) taşıyamıyordu; Throughput'un "Toplam"
// çizgisi OK+Errors'ın eleman-eleman toplamı ve boşlukları KORUMALI. values
// verildiğinde series yalnız zaman eksenine katkı için okunur (boş da
// olabilir) — hizalama garantisi, sınıfın geri kalanında olduğu gibi,
// ÇAĞIRANDA.
export interface ChartLine { series: SpanMetricSeries[]; color: string; label: string; values?: (number | null)[] }

// toOvSeries — ChartLine[] → OverviewChart serileri. Modül kapsamında (saf):
// hem çizilen çizgiler hem de v0.9.483'te gelen ayrı istatistik kümesi aynı
// dönüşümden geçer, useMemo bağımlılığı yalnız girdiye kalır.
function toOvSeries(ls: ChartLine[]): OvChartSeries[] {
  return ls
    .map(l => ({
      label: l.label, color: l.color,
      // v0.9.483 — önceden hesaplanmış değerler varsa onlar (null boşluklu
      // türetilmiş çizgiler); yoksa eski yol (ilk serinin noktaları).
      data: l.values ?? (l.series[0]?.points ?? []).map(p => p.value),
    }))
    .filter(s => s.data.length);
}

export function ChartCard({ title, titleTip, lines, statsLines, unit, mode = 'line', deploy, status = 'ready', onZoom, onZoomReset, syncKey, xRange, thresholds, regions, legendStorageKey, defaultHidden, statsDefaultCollapsed }: {
  title: string; lines: ChartLine[]; unit: string;
  // v0.9.483 — başlığın hover açıklaması (kapsam tanımı). Overview RED
  // kartlarında "· giriş" eki başlıktan kalktı, tanım buraya taşındı.
  titleTip?: string;
  // v0.9.483 — alttaki istatistik tablosu için AYRI seri kümesi (verilmezse
  // çizilen çizgiler). Throughput: çizgiler Toplam+Errors, tablo OK+Errors
  // (+ tfoot "Toplam") — bkz. OverviewChart.statsSeries.
  statsLines?: ChartLine[];
  // v0.9.483 — istatistik tablosu kapalı başlasın mı (kalıcı seçim yoksa).
  statsDefaultCollapsed?: boolean;
  mode?: 'line' | 'area' | 'stacked'; deploy?: { sec: number; label: string } | null;
  // RED series fetch state — distinguishes loading/error from a genuinely
  // empty window so a failed metric fetch doesn't masquerade as "No data".
  status?: 'loading' | 'error' | 'ready';
  // v0.8.534 — threaded to OverviewChart for drag-zoom → global range.
  onZoom?: (fromSec: number, toSec: number) => void;
  // Grafana-parite M1 — çift-tık geri (sayfa yığını) + kardeş grafiklerle
  // imleç senkron key'i; ikisi de OverviewChart'a aynen iletilir.
  onZoomReset?: () => void;
  syncKey?: string;
  // v0.9.83 — sorgu penceresi (unix sec): x-ekseni pencereye sabitlenir.
  xRange?: { from: number; to: number } | null;
  // Grafana-parite M3 — eşik çizgileri + problem/anomali x-bölgeleri;
  // OverviewChart'a aynen iletilir (Overview failure-rate SLO eşiği).
  thresholds?: ChartThreshold[];
  regions?: ChartTimeRegion[];
  // Grafana-parite madde 4 (legend persist + latency default'u) —
  // OverviewChart'a aynen iletilir.
  legendStorageKey?: string;
  defaultHidden?: readonly string[];
}) {
  const times = useMemo(() => {
    const base = lines.find(l => (l.series[0]?.points ?? []).length)?.series[0]?.points ?? [];
    return base.map(p => p.time / 1e9);
  }, [lines]);
  const ovSeries = useMemo<OvChartSeries[]>(() => toOvSeries(lines), [lines]);
  const ovStats = useMemo<OvChartSeries[] | undefined>(
    () => (statsLines ? toOvSeries(statsLines) : undefined), [statsLines]);
  return (
    <div className="card">
      <div className="ov-card-h">
        <h3 title={titleTip}>{title}</h3>
        {/* v0.9.103 (Grafana-parity #1) — header swatch lejantı kaldırıldı;
            OverviewChart artık altında StatsLegend (swatch+label+istatistik)
            gösteriyor, çift lejant olmasın. */}
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
            deployAtSec={deploy?.sec ?? null} deployLabel={deploy?.label}
            thresholds={thresholds} regions={regions}
            onZoom={onZoom} onZoomReset={onZoomReset} syncKey={syncKey} xRange={xRange}
            legendStorageKey={legendStorageKey} defaultHidden={defaultHidden}
            statsSeries={ovStats} statsDefaultCollapsed={statsDefaultCollapsed} />
        )}
      </div>
    </div>
  );
}
