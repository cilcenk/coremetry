import { describe, expect, it } from 'vitest';
import { kafkaBlockItems, kafkaChartItems, kafkaDegradeTR, kafkaLastRows, kafkaLastValue, kafkaPanelUnit, kafkaScopeNoteTR, kafkaSeriesLabel, KAFKA_SERIES_CAP, kafkaShortLabels } from './kafkaClients';
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
  // v0.10.575 — /messaging/topic üst grafiği + kapsam beyanı.
  it('grafik item\'ları iki bloktan gelir, adları ÖNEKLİ', () => {
    const { items, truncated } = kafkaChartItems({
      producer_send_rate: blk(['service.name'], [[['loan'], [1, 2]]]),
      consumer_consumed_rate: blk(['service.name'], [[['loan'], [3, 4]]]),
    });
    expect(items.map(i => i.name)).toEqual(['gönderilen · loan', 'tüketilen · loan']);
    expect(truncated).toBe(0);
    // Blok yoksa item de yok — panel boş-durumunu kendi çizer.
    expect(kafkaChartItems(undefined)).toEqual({ items: [], truncated: 0 });
    // Tavan blok BAŞINA: bir taraf kalabalıksa diğerini yutmaz.
    const many = blk(['service.name'], Array.from({ length: 15 }, (_, i) => [[`s${i}`], [1]] as [string[], number[]]));
    const both = kafkaChartItems({ producer_send_rate: many, consumer_consumed_rate: many });
    expect(both.items.length).toBe(KAFKA_SERIES_CAP * 2);
    expect(both.truncated).toBe((15 - KAFKA_SERIES_CAP) * 2);
  });
  it('kapsam beyanı yalnız servis kapsamında yazılır', () => {
    expect(kafkaScopeNoteTR('services')).toMatch(/topic'e göre süzülemez/);
    expect(kafkaScopeNoteTR('topic')).toBeNull();
    expect(kafkaScopeNoteTR(undefined)).toBeNull();
  });
});

// v0.10.595 — grafik/tooltip etiketleri okunabilir. Operatör ekranı: altı
// tooltip'in her satırı "servis · consumer-…-<uuid>-7" idi. Kural:
//   1. blokta TÜM serilerde aynı olan sütun düşer (kapsam zaten başlıkta),
//      ama en az bir sütun kalır
//   2. UUID ilk 8 karaktere iner, sondaki -N örnek indeksi KALIR
//   3. kısaltma iki seriyi aynı yapıyorsa o seriler TAM etikete düşer —
//      okunabilirlik tekilliği yemez
// Son-değer tablosu TAM etiketi korur (sütunu var); grafikler kısa.
describe('kafkaShortLabels', () => {
  const sr = (...gk: string[]) => ({ groupKey: gk, points: [] });
  it('ortak servis sütunu düşer, UUID kısalır, indeks kalır', () => {
    const got = kafkaShortLabels([
      sr('shop-consumer', 'consumer-shop-orders-21c6f42c-aff6-45f7-9ee6-ac66938990cf-7'),
      sr('shop-consumer', 'consumer-shop-orders-3b3399b9-c636-466d-b9fc-e08d3d15773b-7'),
    ]);
    expect(got).toEqual(['consumer-shop-orders-21c6f42c…-7', 'consumer-shop-orders-3b3399b9…-7']);
  });
  it('tek sütun ASLA düşmez', () => {
    expect(kafkaShortLabels([sr('shop-consumer'), sr('shop-consumer')])).toEqual(['shop-consumer', 'shop-consumer']);
  });
  it('kısaltma çakıştırırsa TAM etikete düşer', () => {
    const got = kafkaShortLabels([
      sr('s', 'c-21c6f42c-aff6-45f7-9ee6-ac66938990cf-1'),
      sr('s', 'c-21c6f42c-0000-0000-0000-000000000000-1'), // ilk 8 aynı → çakışma
      sr('s', 'c-deadbeef-aff6-45f7-9ee6-ac66938990cf-1'),
    ]);
    // Tam etiket ORTAK sütunu da geri getirir — ayırt edicilik okunabilirliği yener.
    expect(got[0]).toBe('s · c-21c6f42c-aff6-45f7-9ee6-ac66938990cf-1');
    expect(got[1]).toBe('s · c-21c6f42c-0000-0000-0000-000000000000-1');
    expect(got[2]).toBe('c-deadbeef…-1');
  });
  it('boş anahtar (tümü), tek seri kısa kalır', () => {
    expect(kafkaShortLabels([sr()])).toEqual(['(tümü)']);
    // Tek seride servis sütunu 'ortak' sayılır ve düşer: kapsam başlıkta, kural tutarlı.
    expect(kafkaShortLabels([sr('svc', 'client-1')])).toEqual(['client-1']);
  });
  it('kafkaBlockItems grafik adları KISA, tablo tam', () => {
    const block = {
      metric: 'm', label: 'l', unit: '1', kind: 'gauge', agg: 'sum', groupBy: ['service.name', 'client_id'],
      series: [sr('shop-consumer', 'c-21c6f42c-aff6-45f7-9ee6-ac66938990cf-7'), sr('shop-consumer', 'c-3b3399b9-c636-466d-b9fc-e08d3d15773b-7')],
    } as KafkaMetricBlock;
    expect(kafkaBlockItems(block).items.map(i => i.name)).toEqual(['c-21c6f42c…-7', 'c-3b3399b9…-7']);
    expect(kafkaLastRows({ x: block }, ['x'])[0].key).toContain('shop-consumer · c-');
  });
});
