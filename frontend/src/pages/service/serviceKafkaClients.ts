// serviceKafkaClients.ts — v0.10.552 (Messaging Kafka Faz 3). SAF: servis
// paneli için /kafka-clients zarfı → StatTile şeridi değerleri + MetricArea'nın
// ClusterNamedSeries şekli. Şerit "son değer"lerden: bağlantı toplam (sum),
// gecikme ort. (istemciler arası ortalama) / maks., rebalance toplam, son
// poll maks. Blok boş → null (karo "—", sessiz 0 yok).
import type { ClusterNamedSeries, KafkaMetricBlock, ServiceKafkaClients } from '@/lib/types';
import { kafkaLastValue, kafkaSeriesLabel } from '@/features/dependencies/kafkaClients';

export const SERVICE_KAFKA_SERIES_CAP = 12;

function lastValues(block: KafkaMetricBlock | undefined): number[] {
  return (block?.series ?? []).map(kafkaLastValue).filter((v): v is number => v !== null);
}
const sum = (xs: number[]) => (xs.length ? xs.reduce((a, b) => a + b, 0) : null);
const avg = (xs: number[]) => (xs.length ? xs.reduce((a, b) => a + b, 0) / xs.length : null);
const max = (xs: number[]) => (xs.length ? Math.max(...xs) : null);

export interface KafkaStrip {
  producerConnections: number | null;
  consumerConnections: number | null;
  latencyAvgMs: number | null;
  latencyMaxMs: number | null;
  rebalancePerHour: number | null;
  lastPollSecMax: number | null;
}

export function kafkaStrip(blocks: Record<string, KafkaMetricBlock> | null | undefined): KafkaStrip {
  const b = blocks ?? {};
  return {
    producerConnections: sum(lastValues(b.producer_connection_count)),
    consumerConnections: sum(lastValues(b.consumer_connection_count)),
    latencyAvgMs: avg(lastValues(b.producer_request_latency_avg)),
    latencyMaxMs: max(lastValues(b.producer_request_latency_max)),
    rebalancePerHour: sum(lastValues(b.consumer_rebalance_rate)),
    lastPollSecMax: max(lastValues(b.consumer_last_poll)),
  };
}

/** SpanMetricSeries (ns) → MetricArea'nın ClusterNamedSeries'i (unix s). */
export function kafkaToNamedSeries(block: KafkaMetricBlock | null | undefined, cap = SERVICE_KAFKA_SERIES_CAP): { series: ClusterNamedSeries[]; total: number } {
  const all = block?.series ?? [];
  return {
    series: all.slice(0, cap).map(s => ({
      name: kafkaSeriesLabel(s.groupKey),
      points: (s.points ?? []).map(p => ({ bucket: Math.round(p.time / 1e9), value: p.value })),
    })),
    total: all.length,
  };
}

/** Şerit tamamen boşsa panel yalnız grafiklerle yetinir. */
export function kafkaStripEmpty(s: KafkaStrip): boolean {
  return Object.values(s).every(v => v === null);
}

export type { ServiceKafkaClients };
