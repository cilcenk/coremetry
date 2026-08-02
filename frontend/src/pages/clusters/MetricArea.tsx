import { Card } from '@/components/ui';
import { MultiLineChart } from '@/components/MultiLineChart';
import { namedSeriesToSeries } from '@/pages/clusters/trendSeries';
import type { ClusterNamedSeries } from '@/lib/types';

// MetricArea — başlık + Total/By-X segmented toggle + area chart kartı
// (v0.9.49, design handoff §3+§8 ortak bileşeni). Clusters Overview'un
// CPU/Mem kartlarından çıkarıldı (v0.9.35 ResToggleHeader'ın genel
// hali): Servis → Infrastructure sekmesi aynı kartı "By pod"
// etiketiyle kullanır. Seri yoksa null döner — görünmez-düşer.
export function MetricArea({ title, subtitle, byLabel, totalLabel = 'Total', by, onToggle, series, seriesName, unit, height = 180, maxSeries, totalSeries, onZoom, onZoomReset, syncKey }: {
  title: string;
  // v0.9.383 (redesign D7) — insanileştirilmiş başlığın altında ham
  // metrik adı (monospace, soluk) — bilgi kaybı yasak.
  subtitle?: string;
  // v0.9.534 — byLabel/onToggle opsiyonel: HAProxy route panelleri hep
  // route-bazlı, toggle anlamsız; onToggle verilmezse toggle hiç çizilmez.
  byLabel?: string; // "By node" | "By pod" — toggle'ın sağ şıkkı
  // v0.9.146 — sol şık etiketi (varsayılan "Total"); jboss datasource
  // panelleri "By datasource" (off=data_source, on=pod+data_source) yapar.
  totalLabel?: string;
  by?: boolean;
  onToggle?: (v: boolean) => void;
  series: ClusterNamedSeries[] | null | undefined;
  seriesName: string; // Total modunda tek serinin legend adı
  unit?: string;
  height?: number;
  // v0.9.148 — MultiLineChart fold eşiği; jboss datasource panelleri
  // tüm datasource'ları göstersin diye yüksek verilir (others kesme yok).
  maxSeries?: number;
  // v0.9.370 — sunucu kesmesi ifşası: kesme öncesi toplam seri sayısı.
  // series.length'ten büyükse başlıkta "N / M pod" rozeti çizilir; sekme
  // "hangi pod'un heap'i dolu" sorusuna bakarken 8-pod top-cut sessizdi.
  totalSeries?: number;
  // v0.9.58 — drag-seçim global time picker'a yazılsın (operatör
  // isteği): MultiLineChart'ın onZoom'u aynen iletilir; çağıran
  // setRange({preset:'custom', fromMs, toMs}) yapar (Service.tsx emsali).
  onZoom?: (fromUnixSec: number, toUnixSec: number) => void;
  // Grafana-parite M1 — çift-tık: sayfa zoom geri-yığınını pop eder
  // (MultiLineChart'a aynen iletilir; verilmezse mevcut davranış).
  onZoomReset?: () => void;
  // Madde 4 sweep — kardeş kartlarla imleç senkronu (uPlot.sync);
  // MultiLineChart'a aynen iletilir (Clusters CPU/Mem = 'clusters',
  // servis JMX panelleri = RuntimeCharts'ın `runtime:${service}` grubu).
  syncKey?: string;
}) {
  if (!series || series.length === 0) return null;
  return (
    <Card header={
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 8 }}>
        <span>
          {title}
          {subtitle && (
            <span className="mono" style={{ display: 'block', fontSize: 9.5, fontWeight: 400, color: 'var(--text3)' }}>{subtitle}</span>
          )}
          {totalSeries != null && series.length < totalSeries && (
            <span className="badge b-warn" style={{ marginLeft: 8 }}
              title={`Sunucu ortalaması en yüksek ${series.length} pod'u döndürdü (${totalSeries} pod'dan). Pencere sonunda sıçrayan ama ortalaması düşük bir pod bu kesimin dışında kalabilir — tek pod'a bakmak için satırdan pod seçin.`}>
              {series.length} / {totalSeries} pod
            </span>
          )}
        </span>
        {onToggle && byLabel && (
          <span style={{ display: 'inline-flex', border: '1px solid var(--border)', borderRadius: 4, overflow: 'hidden' }}>
            {([[totalLabel, false], [byLabel, true]] as const).map(([label, v]) => (
              <button key={label} type="button"
                onClick={e => { e.stopPropagation(); onToggle(v); }}
                style={{
                  all: 'unset', cursor: 'pointer', padding: '2px 8px', fontSize: 11,
                  background: by === v ? 'var(--accent-soft)' : 'transparent',
                  color: by === v ? 'var(--accent2)' : 'var(--text3)',
                }}>{label}</button>
            ))}
          </span>
        )}
      </div>
    }>
      <MultiLineChart series={namedSeriesToSeries(series, seriesName)} height={height} unit={unit} maxSeries={maxSeries} onZoom={onZoom} onZoomReset={onZoomReset} syncKey={syncKey} />
    </Card>
  );
}
