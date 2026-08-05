import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import {
  LEVEL_FIELDS, LOG_ENV_SUFFIXES, SERVICE_FIELDS, SPAN_FIELDS, TRACE_FIELDS,
  kqlAnyField, serviceValues, stripLogEnvSuffix,
} from './logFieldAliases';

// v0.9.661 — DİLLER ARASI KAPI.
//
// Bu listeler Go'daki kardeşlerinin AYNASI. Ayrıştıkları an operatör
// şunu görür: Coremetry logları listeler, "Discover in Kibana" ise
// "No results" der — sessiz, teşhisi zor ve tam olarak bugün bildirilen
// hata. Aynanın kendisi kapısız bırakılamaz.
//
// Emsal: internal/logstore/env_suffix.go zaten "podWorkload.ts'teki
// ENV_SUFFIXES ile AYNI küme" diye uyarıyordu — ama uyarı bir yorumdu,
// kapı değildi.

const GO = (p: string) =>
  readFileSync(resolve(__dirname, '../../../internal/logstore', p), 'utf8');

// stripGoComments — Go satır yorumlarını atar.
//
// ŞART: bu dosyanın aradığı alan adları Go tarafında YORUMLARDA da
// geçiyor (v0.9.480 açıklaması düz service_name'den söz ediyor).
// Yorumlu tarama kendi açıklamasını "kod" sanar — bu kod tabanında
// dört kez ısırdı.
function stripGoComments(src: string): string {
  return src.split('\n').map(l => {
    const i = l.indexOf('//');
    return i < 0 ? l : l.slice(0, i);
  }).join('\n');
}

function quoted(block: string): string[] {
  return [...block.matchAll(/"([^"]+)"/g)].map(m => m[1]);
}

// block — `<anchor>` sonrasındaki ilk `{ … }` gövdesi.
function block(src: string, anchor: string): string {
  const i = src.indexOf(anchor);
  if (i < 0) throw new Error(`Go kaynağında bulunamadı: ${anchor}`);
  const open = src.indexOf('{', i);
  const close = src.indexOf('}', open);
  if (open < 0 || close < 0) throw new Error(`gövde ayrıştırılamadı: ${anchor}`);
  return src.slice(open + 1, close);
}

describe('Go ↔ TS alan adı aynası', () => {
  const es = stripGoComments(GO('elasticsearch.go'));

  // Kibana linki, Coremetry'nin KENDİ ES filtresinin eşlediği her alanı
  // sormalı. Aksi halde Coremetry'nin bulduğu bir logu link bulamaz.
  it('SERVICE_FIELDS backend svcFields\'ı kapsıyor', () => {
    const goFields = quoted(block(es, 'svcFields := []string{'));
    expect(goFields.length).toBeGreaterThan(0);
    for (const f of goFields) {
      expect(SERVICE_FIELDS as readonly string[]).toContain(f);
    }
  });

  // s.fields.Service (operatörün yapılandırabildiği alan) varsayılanı
  // "service.name"; TS tarafı yalnız varsayılanı biliyor, o yüzden
  // listede olmak zorunda.
  it('yapılandırılabilir alanın VARSAYILANI listede', () => {
    expect(es).toContain('c.Fields.Service = "service.name"');
    expect(SERVICE_FIELDS as readonly string[]).toContain('service.name');
  });

  // Bilinçli DIŞLAMA — iki tarafta da. v0.9.480: cluster-logging
  // kayıtlarında düz service_name çoğu zaman OPERASYON adı.
  it('düz service_name iki tarafta da aday DEĞİL', () => {
    expect(quoted(block(es, 'svcFields := []string{'))).not.toContain('service_name');
    expect(SERVICE_FIELDS as readonly string[]).not.toContain('service_name');
  });

  // expandShorthand alias tablosu — trace/span/level.
  const alias = (key: string) => {
    const m = new RegExp(`"${key}":\\s*\\{([^}]*)\\}`).exec(es);
    if (!m) throw new Error(`alias bulunamadı: ${key}`);
    return quoted(m[1]);
  };

  it('TRACE_FIELDS backend alias\'ını kapsıyor', () => {
    for (const f of alias('trace')) expect(TRACE_FIELDS as readonly string[]).toContain(f);
  });

  it('SPAN_FIELDS backend alias\'ını kapsıyor', () => {
    for (const f of alias('span')) expect(SPAN_FIELDS as readonly string[]).toContain(f);
  });

  it('LEVEL_FIELDS backend alias\'ını kapsıyor', () => {
    for (const f of alias('level')) expect(LEVEL_FIELDS as readonly string[]).toContain(f);
  });

  // env_suffix.go zaten "podWorkload.ts ile AYNI küme" diyor; üçüncü
  // kopya da aynı kümede kalmalı.
  it('LOG_ENV_SUFFIXES Go logEnvSuffixes ile birebir', () => {
    const go = quoted(block(stripGoComments(GO('env_suffix.go')), 'logEnvSuffixes = []string{'));
    expect([...LOG_ENV_SUFFIXES]).toEqual(go);
  });
});

describe('yardımcılar', () => {
  // HER ek ayrı dal — birim-şablon dersi.
  it('her ortam ekini soyuyor', () => {
    expect(stripLogEnvSuffix('svc-prod')).toBe('svc');
    expect(stripLogEnvSuffix('svc-int')).toBe('svc');
    expect(stripLogEnvSuffix('svc-uat')).toBe('svc');
    expect(stripLogEnvSuffix('svc-prep')).toBe('svc');
  });

  it('eki olmayanı bırakıyor', () => {
    expect(stripLogEnvSuffix('checkout')).toBe('checkout');
  });

  // SADECE ek — soyunca boş ad kalırdı; Go'daki `len(service) > len(suf)`
  // koşulunun aynısı.
  it('adın tamamı ekse soymuyor', () => {
    expect(stripLogEnvSuffix('-prod')).toBe('-prod');
  });

  // "integration-prod" içinde "-int" GEÇİYOR ama sonek değil.
  it('alt dize değil SONEK arıyor', () => {
    expect(stripLogEnvSuffix('printer')).toBe('printer');
  });

  it('serviceValues ek yoksa tek değer', () => {
    expect(serviceValues('checkout')).toEqual(['checkout']);
  });

  it('serviceValues ek varsa iki değer', () => {
    expect(serviceValues('svc-uat')).toEqual(['svc-uat', 'svc']);
  });

  // Tek alan + tek değer parantezsiz kalmalı: en sık durum okunabilir
  // olsun ve mevcut linkler gereksiz yere değişmesin.
  it('tek yan tümce parantezsiz', () => {
    expect(kqlAnyField(['trace.id'], ['abc'])).toBe('trace.id:"abc"');
  });

  it('çok yan tümce parantezli or grubu', () => {
    expect(kqlAnyField(['a', 'b'], ['x'])).toBe('(a:"x" or b:"x")');
  });

  it('alan × değer çapraz çarpımı', () => {
    expect(kqlAnyField(['a'], ['x', 'y'])).toBe('(a:"x" or a:"y")');
  });

  it('boş girdi boş dize', () => {
    expect(kqlAnyField([], ['x'])).toBe('');
    expect(kqlAnyField(['a'], [])).toBe('');
  });
});
