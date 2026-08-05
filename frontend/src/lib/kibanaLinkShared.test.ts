import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { buildKQLFromFilter, buildKibanaURL } from './kibanaLink';

// v0.9.658 — operatör: "CoSRE o servisin kibana linkini de verebilir mi".
//
// buildKibanaURL ve KQL kurucusu ZATEN vardı — /logs kullanıyordu.
// Kurucu Logs.tsx içinde SAYFA-YEREL bir fonksiyondu; analiz paneli için
// kopyalamak, alan adlarını (service.name/log.level/trace.id) ikinci bir
// yere yazmak demekti. Paylaşılan modüle taşındı.

describe('buildKQLFromFilter', () => {
  const base = { service: '', search: '', severity: 0, traceId: '', spanId: '' };

  it('servis alanını Kibana adıyla üretir', () => {
    expect(buildKQLFromFilter({ ...base, service: 'checkout' }))
      .toBe('service.name:"checkout"');
  });

  // Tırnak KAÇIRILMALI: servis adında " geçerse KQL bozulur ve sorgu
  // sessizce başka bir şey döndürür.
  it('servis adındaki tırnağı kaçırır', () => {
    expect(buildKQLFromFilter({ ...base, service: 'a"b' })).toContain('a\\"b');
  });

  // Analiz paneli severity 17 gönderiyor — hata odaklı.
  it('severity 17 FATAL|ERROR veriyor', () => {
    expect(buildKQLFromFilter({ ...base, severity: 17 }))
      .toContain('log.level:("FATAL" OR "ERROR")');
  });

  it('servis + severity AND ile birleşiyor', () => {
    const kql = buildKQLFromFilter({ ...base, service: 'checkout', severity: 17 });
    expect(kql).toContain(' AND ');
    expect(kql.startsWith('service.name:"checkout"')).toBe(true);
  });

  it('boş filtre boş KQL', () => {
    expect(buildKQLFromFilter(base)).toBe('');
  });
});

describe('buildKibanaURL kapıları', () => {
  const kql = 'service.name:"checkout"';
  it('yapılandırılmamışsa link YOK', () => {
    expect(buildKibanaURL(null, { kql })).toBeNull();
    expect(buildKibanaURL({ enabled: false, baseUrl: 'https://k' }, { kql })).toBeNull();
    expect(buildKibanaURL({ enabled: true, baseUrl: '' }, { kql })).toBeNull();
  });
  it('yapılandırılmışsa discover linki', () => {
    const href = buildKibanaURL({ enabled: true, baseUrl: 'https://k/' }, { kql });
    expect(href).toContain('/app/discover#/?');
    // Sondaki / tekrarlanmamalı.
    expect(href).not.toContain('k//app');
  });
});

// Kurucu TEK yerde kalmalı: Logs sayfası kendi kopyasını geri
// yazarsa alan adları zamanla ayrışır.
describe('tek kaynak', () => {
  const logs = readFileSync(resolve(__dirname, '../pages/Logs.tsx'), 'utf8');
  it('Logs.tsx kendi KQL kurucusunu TAŞIMIYOR', () => {
    expect(logs).not.toMatch(/^function buildKQLFromFilter/m);
  });
  it('Logs.tsx paylaşılan kurucuyu import ediyor', () => {
    expect(logs).toContain("buildKQLFromFilter } from '@/lib/kibanaLink'");
  });
});
