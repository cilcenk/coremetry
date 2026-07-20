import { useMemo } from 'react';
import { RuntimeCharts } from './RuntimeCharts';
import { timeRangeToNs } from '@/lib/utils';
import type { TimeRange } from '@/lib/types';

// ServiceMetricsTab (v0.9.139) — servis detayının "Metrics" sekmesi.
//
// Faz 1 (bu sürüm): Overview'dan taşınan OTel dil-runtime çizelgeleri
// (JVM heap/GC/threads · .NET · Go — RuntimeCharts, kaynağı CH
// metric_points / /api/metrics/query). Bu grafikler artık Overview'da
// DEĞİL, burada yaşar (operatör talebi 2026-07-20: "Metrics diye yeni
// bir tab olsun ve Overview'deki gc/jvm-heap/threadleri onun altına al").
//
// Faz 2 (planlı, spec onaylı): Thanos'tan JBoss 8.x JMX panelleri
// (XA-datasource pool'ları, thread pool'lar) — Infra sekmesiyle AYNI
// cluster/pod keşif mantığı (podMatchesService/clustersWithPods), JMX
// serileri `instance=` label'lı; yeni /api/clusters/jmx-trend ailesi.
// Buraya, RuntimeCharts'ın altına bir "JBoss (JMX)" bölümü eklenecek.
export function ServiceMetricsTab({ service, range, onZoom }: {
  service: string;
  range: TimeRange;
  // Grafik drag-seçimi global time picker'a yazar (Overview/Infra ile aynı).
  onZoom?: (fromUnixSec: number, toUnixSec: number) => void;
}) {
  // from/to unix NS — RuntimeCharts parent ile aynı RQ anahtarını paylaşır.
  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);
  return (
    <div>
      <RuntimeCharts service={service} from={from} to={to} onZoom={onZoom} />
    </div>
  );
}
