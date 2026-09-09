// DetailDrawer.kafka.pin.test.ts — v0.10.551: Kafka istemci bölümü çekmecede
// yalnız queue dalında, Consumers'tan SONRA, Top operations'tan ÖNCE; pod hücresi
// pivot linki; liste sayfasında (Messaging.tsx) grafik YOK (v0.9.834).
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const drawer = readFileSync(resolve(__dirname, 'DetailDrawer.tsx'), 'utf8');
const caller = readFileSync(resolve(__dirname, 'CallerSection.tsx'), 'utf8'); // v0.10.575
const list = readFileSync(resolve(__dirname, '../../pages/Messaging.tsx'), 'utf8');

describe('KafkaClientsSection yerleşimi', () => {
  it('queue dalında, Consumers sonrası, Top operations öncesi', () => {
    const sec = drawer.indexOf('<KafkaClientsSection');
    const consumers = drawer.indexOf('title={`Consumers ·');
    const topOps = drawer.indexOf('{/* Top operations');
    expect(sec).toBeGreaterThan(consumers);
    expect(sec).toBeLessThan(topOps);
    expect(drawer).toContain("import { KafkaClientsSection } from './KafkaClientsSection'");
  });
  it('pod hücresi podDetailPath ile linkli', () => {
    // v0.10.575 — CallerSection çekmeceden ÇIKARILDI (/messaging/topic sayfası
    // aynı tabloyu çiziyor). Pivot taşındı, KAYBOLMADI: iddia kodun yeni evini
    // okuyor + çekmecenin o evden import ettiğini ayrıca çiviliyor, yoksa
    // bileşen sessizce düşse de test yeşil kalırdı.
    expect(caller).toContain("podDetailPath({ pod: c.pod, service: c.service");
    expect(drawer).toContain("import { CallerSection } from './CallerSection';");
    expect(drawer).toContain('<CallerSection');
  });
  it('v0.10.553 — Top-ops operasyon türü kolonu yalnız queue kipinde', () => {
    expect(drawer).toContain("kind === 'queue'\n      ? [{ id: 'op', label: 'Type'");
    expect(drawer).toContain("{o.operation ?? '—'}");
  });
  it('liste sayfasına grafik/bölüm girmez', () => {
    expect(list).not.toContain('KafkaClientsSection');
    expect(list).not.toContain('CorePanelMulti');
  });
});
