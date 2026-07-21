import { useMemo } from 'react';
import { RuntimeCharts } from './RuntimeCharts';
import { timeRangeToNs } from '@/lib/utils';
import type { TimeRange } from '@/lib/types';

// ServiceMetricsTab (v0.9.139) — servis detayının "Metrics" sekmesi:
// Overview'dan taşınan OTel dil-runtime çizelgeleri (JVM heap/GC/threads ·
// .NET · Go — RuntimeCharts, kaynağı CH metric_points / /api/metrics/query).
//
// Not: Thanos'taki jvm_/jboss_ JMX metrikleri (auto-discovery, v0.9.144)
// operatör kararıyla Infrastructure sekmesinde yaşar (ServiceInfraTab) —
// pod/cluster keşfini orada zaten yapıyoruz.
export function ServiceMetricsTab({ service, range, onZoom }: {
  service: string;
  range: TimeRange;
  onZoom?: (fromUnixSec: number, toUnixSec: number) => void;
}) {
  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);
  return (
    <div>
      <RuntimeCharts service={service} from={from} to={to} onZoom={onZoom} />
    </div>
  );
}
