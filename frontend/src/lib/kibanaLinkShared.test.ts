import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { buildKQLFromFilter, buildKibanaURL } from './kibanaLink';
import {
  LEVEL_FIELDS, SERVICE_FIELDS, SPAN_FIELDS, TRACE_FIELDS, kqlEscape,
} from './logFieldAliases';

// v0.9.658 — operatör: "CoSRE o servisin kibana linkini de verebilir mi".
//
// buildKibanaURL ve KQL kurucusu ZATEN vardı — /logs kullanıyordu.
// Kurucu Logs.tsx içinde SAYFA-YEREL bir fonksiyondu; analiz paneli için
// kopyalamak, alan adlarını (service.name/log.level/trace.id) ikinci bir
// yere yazmak demekti. Paylaşılan modüle taşındı.

describe('buildKQLFromFilter', () => {
  const base = { service: '', search: '', severity: 0, traceId: '', spanId: '' };

  // v0.9.661 (operatör-bildirimi, prod): tek alan adı yerine ADAY GRUBU.
  // O indekste `service.name` alanı HİÇ YOK — kimlik
  // kubernetes.container_name'de. Eski tek-alanlı sorgu "No results"
  // döndürüyordu, Coremetry'nin Logs sayfası ise aynı logları
  // listeliyordu.
  it('servisi TÜM aday alanlarda arıyor', () => {
    const kql = buildKQLFromFilter({ ...base, service: 'checkout' });
    for (const f of SERVICE_FIELDS) {
      expect(kql).toContain(`${f}:"checkout"`);
    }
    expect(kql).toContain(' or ');
  });

  // Düz `service_name` LİSTEDE OLMAMALI: v0.9.480'de ölçüldü, o alan
  // cluster-logging kayıtlarında çoğu zaman servis değil OPERASYON adı.
  // Eklemek yanlış kayıtları eşleştirirdi — "yardımcı olan" üst küme
  // burada bir hata olurdu.
  it('düz service_name aday DEĞİL', () => {
    expect(SERVICE_FIELDS).not.toContain('service_name');
    expect(buildKQLFromFilter({ ...base, service: 'checkout' }))
      .not.toContain('service_name:');
  });

  // Ortam ekli servis adları: bazı pipeline'lar konteyneri eksiz
  // adlandırıyor, backend svcValues'da ikisini de deniyor.
  it('ortam ekli adın eksiz hâlini de arıyor', () => {
    const kql = buildKQLFromFilter({ ...base, service: 'bsa-loan-prod' });
    expect(kql).toContain('"bsa-loan-prod"');
    expect(kql).toContain('"bsa-loan"');
  });

  // Ek YOKSA aynı değeri iki kez sormamalı.
  it('eksiz adda değer tekrarlanmıyor', () => {
    const kql = buildKQLFromFilter({ ...base, service: 'checkout' });
    expect(kql.match(/service\.name:"checkout"/g)).toHaveLength(1);
  });

  // Tırnak KAÇIRILMALI: servis adında " geçerse KQL bozulur ve sorgu
  // sessizce başka bir şey döndürür.
  it('servis adındaki tırnağı kaçırır', () => {
    expect(buildKQLFromFilter({ ...base, service: 'a"b' })).toContain('a\\"b');
  });

  // Ters bölü ÖNCE kaçırılmalı, yoksa kendi eklediğimiz kaçışı bir daha
  // kaçırırız ve sorgu bozulur.
  it('ters bölüyü de kaçırır', () => {
    expect(kqlEscape('a\\b')).toBe('a\\\\b');
    expect(kqlEscape('a\\"b')).toBe('a\\\\\\"b');
  });

  // trace_id de aynı sınıf: o indekste alan DÜZ `trace_id`, link
  // `trace.id:` soruyordu.
  it('trace id\'yi tüm aday alanlarda arıyor', () => {
    const kql = buildKQLFromFilter({ ...base, traceId: 'abc123' });
    for (const f of TRACE_FIELDS) expect(kql).toContain(`${f}:"abc123"`);
  });

  it('span id\'yi tüm aday alanlarda arıyor', () => {
    const kql = buildKQLFromFilter({ ...base, spanId: 'def456' });
    for (const f of SPAN_FIELDS) expect(kql).toContain(`${f}:"def456"`);
  });

  // v0.8.406 trace-only: DEĞER değil VARLIK sorgusu — her aday alan için.
  it('trace-only filtresi varlık sorgusu', () => {
    const kql = buildKQLFromFilter({ ...base, hasTrace: true });
    for (const f of TRACE_FIELDS) expect(kql).toContain(`${f}:*`);
  });

  it('traceId varken trace-only eklenmiyor', () => {
    expect(buildKQLFromFilter({ ...base, traceId: 'abc', hasTrace: true }))
      .not.toContain(':*');
  });

  // Analiz paneli severity 17 gönderiyor — hata odaklı.
  // HER seviye eşiği ayrı dal; birim-şablon dersi: tek dalı test etmek
  // yetmiyor.
  it('severity eşikleri: 13 WARN+, 17 ERROR+, 21 FATAL', () => {
    const k13 = buildKQLFromFilter({ ...base, severity: 13 });
    expect(k13).toContain('"WARN"');
    expect(k13).toContain('"ERROR"');
    const k17 = buildKQLFromFilter({ ...base, severity: 17 });
    expect(k17).toContain('"ERROR"');
    expect(k17).not.toContain('"WARN"');
    const k21 = buildKQLFromFilter({ ...base, severity: 21 });
    expect(k21).toContain('"FATAL"');
    expect(k21).not.toContain('"ERROR"');
  });

  it('seviyeyi tüm aday alanlarda arıyor', () => {
    const kql = buildKQLFromFilter({ ...base, severity: 17 });
    for (const f of LEVEL_FIELDS) expect(kql).toContain(`${f}:"ERROR"`);
  });

  // severity 0-12 arası eşik ALTINDA: seviye yan tümcesi hiç olmamalı.
  it('eşik altındaki severity yan tümce üretmiyor', () => {
    expect(buildKQLFromFilter({ ...base, severity: 9 })).toBe('');
  });

  it('servis + severity AND ile birleşiyor', () => {
    const kql = buildKQLFromFilter({ ...base, service: 'checkout', severity: 17 });
    expect(kql).toContain(' AND ');
    expect(kql.startsWith('(service.name:"checkout"')).toBe(true);
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
