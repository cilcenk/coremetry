import { describe, expect, it } from 'vitest';
import { defaultKafkaRuleName } from './KafkaAlertModal';
import { KAFKA_METRICS, targetMetrics } from './constants';

// v0.10.554 — Kafka hedefli kural: ad şablonu + metrik ailesi seçimi.
describe('KafkaAlertModal', () => {
  it('varsayılan ad kapsamı taşır', () => {
    expect(defaultKafkaRuleName({ service: 'loan', topic: 'orders' }, 'kafka_lag_max')).toBe('Kafka lag: loan · topic orders');
    expect(defaultKafkaRuleName({ service: 'pay', clientId: 'p1' }, 'kafka_producer_error_rate')).toBe('Kafka gönderim hatası: pay · istemci p1');
    expect(defaultKafkaRuleName({}, 'kafka_lag_max')).toBe('Kafka lag: servis');
  });
  it('metrik ailesi hedef türüne göre', () => {
    expect(targetMetrics('kafka_client')).toBe(KAFKA_METRICS);
    expect(targetMetrics('db_statement').every(m => m.v.startsWith('db_stmt_'))).toBe(true);
    expect(targetMetrics(undefined).some(m => m.v === 'error_rate')).toBe(true);
  });
});
