// partitionLag.ts — v0.10.589. SAF: set=partitions bloklarından "en kötü N
// partition" satırları. Lag azalan sıralı; eşitlikte partition artan
// (deterministik). Tavanlı liste DÜRÜST: toplam ayrıca döner, tablo
// "N partition daha" yazar (feedback-capped-list).
import type { KafkaMetricBlock } from '@/lib/types';
import { kafkaLastValue } from './kafkaClients';

export interface PartitionLagRow {
  partition: string;
  service: string;
  client: string;
  lag: number | null;
  lead: number | null;
}

export const PARTITION_LAG_ROWS = 20;

// groupKey sırası KafkaPartitionQuestions ile sözleşme: service.name · client_id · partition.
function keyOf(gk: string[] | null | undefined): { service: string; client: string; partition: string } {
  const g = gk ?? [];
  return { service: g[0] ?? '', client: g[1] ?? '', partition: g[2] ?? '' };
}

export function worstPartitions(
  blocks: Record<string, KafkaMetricBlock> | null | undefined,
  n = PARTITION_LAG_ROWS,
): { rows: PartitionLagRow[]; total: number } {
  const lag = blocks?.consumer_partition_lag?.series ?? [];
  const lead = blocks?.consumer_partition_lead?.series ?? [];
  const leadBy = new Map<string, number | null>();
  for (const s of lead) {
    const k = keyOf(s.groupKey);
    leadBy.set(`${k.service}|${k.client}|${k.partition}`, kafkaLastValue(s));
  }
  const rows: PartitionLagRow[] = lag.map(s => {
    const k = keyOf(s.groupKey);
    return { ...k, lag: kafkaLastValue(s), lead: leadBy.get(`${k.service}|${k.client}|${k.partition}`) ?? null };
  });
  rows.sort((a, b) => {
    const la = a.lag ?? -1, lb = b.lag ?? -1;
    if (la !== lb) return lb - la;
    return Number(a.partition) - Number(b.partition) || a.partition.localeCompare(b.partition);
  });
  return { rows: rows.slice(0, n), total: rows.length };
}
