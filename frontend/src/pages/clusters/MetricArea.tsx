import { Card } from '@/components/ui';
import { MultiLineChart } from '@/components/MultiLineChart';
import { namedSeriesToSeries } from '@/pages/clusters/trendSeries';
import type { ClusterNamedSeries } from '@/lib/types';

// MetricArea — başlık + Total/By-X segmented toggle + area chart kartı
// (v0.9.49, design handoff §3+§8 ortak bileşeni). Clusters Overview'un
// CPU/Mem kartlarından çıkarıldı (v0.9.35 ResToggleHeader'ın genel
// hali): Servis → Infrastructure sekmesi aynı kartı "By pod"
// etiketiyle kullanır. Seri yoksa null döner — görünmez-düşer.
export function MetricArea({ title, byLabel, by, onToggle, series, seriesName, unit, height = 180, onZoom }: {
  title: string;
  byLabel: string; // "By node" | "By pod" — toggle'ın sağ şıkkı
  by: boolean;
  onToggle: (v: boolean) => void;
  series: ClusterNamedSeries[] | null | undefined;
  seriesName: string; // Total modunda tek serinin legend adı
  unit?: string;
  height?: number;
  // v0.9.58 — drag-seçim global time picker'a yazılsın (operatör
  // isteği): MultiLineChart'ın onZoom'u aynen iletilir; çağıran
  // setRange({preset:'custom', fromMs, toMs}) yapar (Service.tsx emsali).
  onZoom?: (fromUnixSec: number, toUnixSec: number) => void;
}) {
  if (!series || series.length === 0) return null;
  return (
    <Card header={
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 8 }}>
        <span>{title}</span>
        <span style={{ display: 'inline-flex', border: '1px solid var(--border)', borderRadius: 4, overflow: 'hidden' }}>
          {([['Total', false], [byLabel, true]] as const).map(([label, v]) => (
            <button key={label} type="button"
              onClick={e => { e.stopPropagation(); onToggle(v); }}
              style={{
                all: 'unset', cursor: 'pointer', padding: '2px 8px', fontSize: 11,
                background: by === v ? 'var(--accent-soft)' : 'transparent',
                color: by === v ? 'var(--accent2)' : 'var(--text3)',
              }}>{label}</button>
          ))}
        </span>
      </div>
    }>
      <MultiLineChart series={namedSeriesToSeries(series, seriesName)} height={height} unit={unit} onZoom={onZoom} />
    </Card>
  );
}
