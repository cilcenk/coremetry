import { describe, expect, it } from 'vitest';
import { kafkaStrip, kafkaStripEmpty, kafkaToNamedSeries, SERVICE_KAFKA_SERIES_CAP } from './serviceKafkaClients';
import type { KafkaMetricBlock } from '@/lib/types';

// v0.10.552 — servis Kafka paneli saf çekirdeği: şerit son değerlerden, boş blok null.
const blk = (series: Array<[string[], number[]]>): KafkaMetricBlock => ({
  metric: 'm', label: 'l', unit: 'ms', kind: 'gauge', agg: 'avg', groupBy: ['client_id'],
  series: series.map(([gk, vals]) => ({ groupKey: gk, points: vals.map((v, i) => ({ time: (1_700_000_000 + i * 60) * 1e9, value: v })) })),
});

describe('serviceKafkaClients', () => {
  it('şerit: sum / avg / max son değerlerden; boş → null', () => {
    const s = kafkaStrip({
      producer_connection_count: blk([[['p1'], [3, 4]], [['p2'], [2]]]),
      consumer_connection_count: blk([]),
      producer_request_latency_avg: blk([[['p1'], [10]], [['p2'], [30]]]),
      producer_request_latency_max: blk([[['p1'], [50]], [['p2'], [90, 70]]]),
      consumer_rebalance_rate: blk([[['c1'], [0.5]], [['c2'], [1]]]),
      consumer_last_poll: blk([[['c1'], [2]], [['c2'], [40]]]),
    });
    expect(s).toEqual({ producerConnections: 6, consumerConnections: null, latencyAvgMs: 20, latencyMaxMs: 70, rebalancePerHour: 1.5, lastPollSecMax: 40 });
    expect(kafkaStripEmpty(s)).toBe(false);
    expect(kafkaStripEmpty(kafkaStrip(undefined))).toBe(true);
  });
  it('named series: ns → unix s, etiket groupKey, tavan', () => {
    const { series, total } = kafkaToNamedSeries(blk(Array.from({ length: 14 }, (_, i) => [[`c${i}`], [i]] as [string[], number[]])));
    expect(total).toBe(14);
    expect(series.length).toBe(SERVICE_KAFKA_SERIES_CAP);
    expect(series[0]).toEqual({ name: 'c0', points: [{ bucket: 1_700_000_000, value: 0 }] });
    expect(kafkaToNamedSeries(undefined)).toEqual({ series: [], total: 0 });
  });
});
