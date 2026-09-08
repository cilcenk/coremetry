import { describe, expect, it } from 'vitest';
import { kafkaBlockItems, kafkaDegradeTR, kafkaLastRows, kafkaLastValue, kafkaPanelUnit, kafkaSeriesLabel, KAFKA_SERIES_CAP } from './kafkaClients';
import type { KafkaMetricBlock, MessagingClients } from '@/lib/types';

// v0.10.551 — çekmece "Kafka istemcileri" saf çekirdeği.
const blk = (groupBy: string[], series: Array<[string[], number[]]>): KafkaMetricBlock => ({
  metric: 'm', label: 'l', unit: '{record}/s', kind: 'gauge', agg: 'sum', groupBy,
  series: series.map(([gk, vals]) => ({ groupKey: gk, points: vals.map((v, i) => ({ time: i, value: v })) })),
});

describe('kafkaClients', () => {
  it('seri etiketi ve son değer', () => {
    expect(kafkaSeriesLabel(['a', 'c1'])).toBe('a · c1');
    expect(kafkaSeriesLabel([])).toBe('(tümü)');
    expect(kafkaLastValue({ groupKey: [], points: [{ time: 1, value: 2 }, { time: 2, value: NaN }] })).toBe(2);
    expect(kafkaLastValue({ groupKey: [], points: [] })).toBeNull();
  });
  it('panel öğeleri tavanlı, rol data', () => {
    const many = blk(['service.name'], Array.from({ length: 15 }, (_, i) => [[`s${i}`], [1]] as [string[], number[]]));
    const { items, truncated } = kafkaBlockItems(many);
    expect(items.length).toBe(KAFKA_SERIES_CAP);
    expect(truncated).toBe(15 - KAFKA_SERIES_CAP);
    expect(items[0]).toMatchObject({ name: 's0', role: 'data' });
    expect(kafkaBlockItems(undefined)).toEqual({ items: [], truncated: 0 });
  });
  it('son-değer satırları: istemci satırı servis-düzeyi değeri servis anahtarıyla alır', () => {
    const rows = kafkaLastRows({
      consumer_consumed_rate: blk(['service.name'], [[['loan'], [10, 20]]]),
      consumer_lag_max: blk(['service.name', 'client_id'], [[['loan', 'c2'], [300]], [['loan', 'c1'], [1240]], [['pay', 'p1'], [5]]]),
    }, ['consumer_consumed_rate', 'consumer_lag_max']);
    expect(rows.map(r => r.key)).toEqual(['loan', 'loan · c1', 'loan · c2', 'pay · p1']);
    expect(rows[1].values).toEqual({ consumer_consumed_rate: 20, consumer_lag_max: 1240 });
    expect(rows[3].values).toEqual({ consumer_consumed_rate: null, consumer_lag_max: 5 });
    expect(kafkaLastRows(undefined, ['x'])).toEqual([]);
  });
  it('birim yalnız zaman; düşüş metni', () => {
    expect(kafkaPanelUnit('ms')).toBe('ms');
    expect(kafkaPanelUnit('{record}/s')).toBeUndefined();
    expect(kafkaDegradeTR(null)).toMatch(/okunamadı/);
    expect(kafkaDegradeTR(undefined)).toBeNull();
    const base: MessagingClients = { system: 'kafka', cluster: '(default)', destination: 'o', source: 'vm', available: false, note: 'n', producers: [], consumers: [], blocks: {} };
    expect(kafkaDegradeTR(base)).toMatch(/OTel Java agent/);
    expect(kafkaDegradeTR({ ...base, available: true })).toBeNull();
  });
});
