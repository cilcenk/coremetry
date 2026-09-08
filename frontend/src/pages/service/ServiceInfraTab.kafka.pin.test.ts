// ServiceInfraTab.kafka.pin.test.ts — v0.10.552: Kafka client paneli Infra
// sekmesinde, Thanos gövdesinden BAĞIMSIZ (erken Empty'ler paneli yutmaz);
// Overview'a girmez (operatör: Infra).
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const infra = readFileSync(resolve(__dirname, 'ServiceInfraTab.tsx'), 'utf8');
const overview = readFileSync(resolve(__dirname, 'Overview.tsx'), 'utf8');

describe('ServiceKafkaClientsPanel yerleşimi', () => {
  it('Infra sekmesinde, thanosBody sonrası', () => {
    const body = infra.indexOf('const thanosBody = (() => {');
    const panel = infra.indexOf('<ServiceKafkaClientsPanel');
    expect(body).toBeGreaterThan(0);
    expect(panel).toBeGreaterThan(body);
    // erken dönüşler kapanışın İÇİNDE: paneli yutamaz
    expect(infra.indexOf('No Thanos clusters configured')).toBeGreaterThan(body);
    expect(infra.indexOf('No Thanos clusters configured')).toBeLessThan(panel);
  });
  it('Overview dokunulmadı', () => {
    expect(overview).not.toContain('ServiceKafkaClientsPanel');
  });
});
