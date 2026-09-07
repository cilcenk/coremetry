import { describe, expect, it } from 'vitest';
import {
  parseAttrMap, attrMapToText, parseList, listToText, numFromForm, numToForm,
  thresholdsToForm, thresholdsToWire, TFAIL_TEMPLATE, GG_TOTAL_TEMPLATE,
} from './influxForm';

// v0.10.222 — InfluxTab metin kutuları ↔ tel. Sessiz sınıf: boş eşik
// kutusunun tele 0 yerine HİÇ gitmemesi (vmForm dersi) ve attrMap
// satırlarının yorum/boşluk toleransı.

describe('attrMap', () => {
  it('satır başına TAG=attr, `:` de kabul, yorum ve boş satır atlanır', () => {
    expect(parseAttrMap(`OPERATIONCODE=operation
# yorum
ERRORCODE : error.code

INSTANCEID=k8s.pod.name`)).toEqual({ OPERATIONCODE: 'operation', ERRORCODE: 'error.code', INSTANCEID: 'k8s.pod.name' });
  });
  it('boş metin → undefined (omitempty)', () => {
    expect(parseAttrMap('')).toBeUndefined();
    expect(parseAttrMap('  \n# sadece yorum')).toBeUndefined();
  });
  it('gidiş-dönüş', () => {
    const m = { A: 'x', B: 'y.z' };
    expect(parseAttrMap(attrMapToText(m))).toEqual(m);
    expect(attrMapToText(undefined)).toBe('');
  });
});

describe('list', () => {
  it('virgül ve yeni satır ayırır, kırpar, boşları düşürür', () => {
    expect(parseList('OPERATIONCODE, ERRORCODE\n KANALKOD ,,')).toEqual(['OPERATIONCODE', 'ERRORCODE', 'KANALKOD']);
    expect(parseList('')).toEqual([]);
    expect(listToText(['A', 'B'])).toBe('A, B');
    expect(listToText(undefined)).toBe('');
  });
});

describe('eşikler', () => {
  it.each([
    ['', undefined], ['  ', undefined], ['abc', undefined], ['0', 0], ['2.5', 2.5], ['-1', -1],
  ])('numFromForm(%j) → %j', (t, want) => {
    expect(numFromForm(t)).toBe(want);
  });
  it('numToForm: 0/undefined/null → boş kutu (varsayılanı takip)', () => {
    expect(numToForm(0)).toBe('');
    expect(numToForm(undefined)).toBe('');
    expect(numToForm(null)).toBe('');
    expect(numToForm(6)).toBe('6');
  });
  it('hiç kutu dolu değilse tel\'e nesne bile gitmez', () => {
    expect(thresholdsToWire({ criticalZ: '', dwell: '', minAbsDelta: '', minMAD: '' })).toBeUndefined();
    expect(thresholdsToWire({ criticalZ: '6', dwell: '', minAbsDelta: '5', minMAD: '' })).toEqual({ criticalZ: 6, minAbsDelta: 5 });
  });
  it('sunucudan gelen 0/eksik alanlar boş kutu', () => {
    expect(thresholdsToForm(undefined)).toEqual({ criticalZ: '', dwell: '', minAbsDelta: '', minMAD: '' });
    expect(thresholdsToForm({ dwell: 3 })).toEqual({ criticalZ: '', dwell: '3', minAbsDelta: '', minMAD: '' });
  });
});

describe('TFAIL şablonu (v0.10.526 — GoldenGate ekibinin sorgusu, Coremetry uyarlaması)', () => {
  it('gruplama kanal + operasyon; attrMap altı tag', () => {
    expect(TFAIL_TEMPLATE.groupBy).toEqual(['KANALKOD', 'OPERATIONCODE']);
    expect(Object.keys(TFAIL_TEMPLATE.attrMap ?? {}).sort()).toEqual(
      ['ERRORCODE', 'FUNCTIONCODE', 'INSTANCEID', 'KANALKOD', 'OPERATIONCODE', 'TRACEID']);
    expect(TFAIL_TEMPLATE.attrMap?.TRACEID).toBe('trace_id');
    expect(TFAIL_TEMPLATE.attrMap?.INSTANCEID).toBe('k8s.pod.name');
  });
  it('SORGU 1: ekibin süzgeçleri (TFAIL/ADET, KANALKOD =~ /^01/) + Coremetry uyarlamaları; Grafana değişkeni YOK', () => {
    expect(TFAIL_TEMPLATE.flux).toContain('from(bucket: "GGFailTraceBckt")');
    expect(TFAIL_TEMPLATE.flux).toContain('r._measurement == "TFAIL" and r._field == "ADET"');
    expect(TFAIL_TEMPLATE.flux).toContain('r.KANALKOD =~ /^01/');
    expect(TFAIL_TEMPLATE.flux).toContain('range(start: -2h)'); // v0.10.527 gecikmeli kaynak
    expect(TFAIL_TEMPLATE.flux).toContain('group(columns: ["KANALKOD", "OPERATIONCODE"])');
    expect(TFAIL_TEMPLATE.flux).toContain('aggregateWindow(every: 1m, fn: sum, createEmpty: false)');
    for (const bad of ['v.timeRangeStart', 'v.windowPeriod', 'createEmpty: true', '_value > 4']) {
      expect(TFAIL_TEMPLATE.flux).not.toContain(bad);
    }
  });
  it('SORGU 2 (kanıt) yer tutucuları groupBy tag adlarıyla + from/to; SORGU 1 hiçbirini taşımaz', () => {
    for (const ph of ['{{from}}', '{{to}}', '{{KANALKOD}}', '{{OPERATIONCODE}}']) {
      expect(TFAIL_TEMPLATE.enrichFlux).toContain(ph);
      expect(TFAIL_TEMPLATE.flux).not.toContain(ph);
    }
    expect(TFAIL_TEMPLATE.enrichFlux).toContain('keep(columns: ["_time", "TRACEID", "INSTANCEID", "FUNCTIONCODE", "KANALKOD"])');
    expect(TFAIL_TEMPLATE.enrichFlux).toContain('limit(n: 50)');
    // enrich.go her groupBy tag'ını adıyla doldurur; başka yer tutucu HATA olurdu.
    const phs = [...(TFAIL_TEMPLATE.enrichFlux ?? '').matchAll(/\{\{\s*([A-Za-z_]+)\s*\}\}/g)].map(m => m[1]);
    for (const p of phs) expect(['from', 'to', ...(TFAIL_TEMPLATE.groupBy ?? [])]).toContain(p);
  });
  it('GoldenGate toplam şablonu: ekibin ikinci sorgusu, aynı süzgeç + gruplama, kanıt sorgusu yok', () => {
    expect(GG_TOTAL_TEMPLATE.name).toBe('gg_01_adet_total');
    expect(GG_TOTAL_TEMPLATE.flux).toContain('from(bucket: "GoldenGateBucket")');
    expect(GG_TOTAL_TEMPLATE.flux).toContain('r._field == "ADET"');
    expect(GG_TOTAL_TEMPLATE.flux).toContain('range(start: -2h)');
    expect(GG_TOTAL_TEMPLATE.flux).not.toContain('_measurement ==');
    expect(GG_TOTAL_TEMPLATE.flux).toContain('r.KANALKOD =~ /^01/');
    expect(GG_TOTAL_TEMPLATE.flux).toContain('aggregateWindow(every: 1m, fn: sum, createEmpty: false)');
    expect(GG_TOTAL_TEMPLATE.groupBy).toEqual(['KANALKOD', 'OPERATIONCODE']);
    expect(GG_TOTAL_TEMPLATE.enrichFlux).toBeUndefined();
    expect(GG_TOTAL_TEMPLATE.name).not.toBe(TFAIL_TEMPLATE.name);
  });
});
