import { describe, it, expect } from 'vitest';
import { worstPartitions } from './partitionLag';
import type { KafkaMetricBlock } from '@/lib/types';

// v0.10.589 — en kötü N partition: lag azalan, eşitlikte partition artan,
// lead aynı anahtardan eşleşir, toplam dürüst.
const blk = (rows: Array<[string[], number[]]>): KafkaMetricBlock => ({
  metric: 'm', label: 'l', unit: '{record}', kind: 'gauge', agg: 'max', groupBy: ['service.name', 'client_id', 'partition'],
  series: rows.map(([gk, vals]) => ({ groupKey: gk, points: vals.map((v, i) => ({ time: (1_700_000_000 + i * 60) * 1e9, value: v })) })),
});

describe('worstPartitions', () => {
  it('lag azalan, eşitlikte partition artan; lead eşleşir; toplam dürüst', () => {
    const blocks = {
      consumer_partition_lag: blk([[['s', 'c', '3'], [1, 500]], [['s', 'c', '1'], [1, 500]], [['s', 'c', '7'], [1, 9000]]]),
      consumer_partition_lead: blk([[['s', 'c', '7'], [1, 12]], [['s', 'c', '1'], [1, 4000]]]),
    };
    const { rows, total } = worstPartitions(blocks, 2);
    expect(total).toBe(3);
    expect(rows.map(r => r.partition)).toEqual(['7', '1']);
    expect(rows[0].lag).toBe(9000);
    expect(rows[0].lead).toBe(12);
    expect(rows[1].lead).toBe(4000);
  });
  it('lead bloğu yoksa lead null, satırlar yine gelir', () => {
    const { rows } = worstPartitions({ consumer_partition_lag: blk([[['s', 'c', '0'], [1, 2]]]) }, 5);
    expect(rows).toHaveLength(1);
    expect(rows[0].lead).toBeNull();
  });
  it('boş → boş', () => {
    expect(worstPartitions(undefined)).toEqual({ rows: [], total: 0 });
  });
});
