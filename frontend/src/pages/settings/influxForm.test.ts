import { describe, expect, it } from 'vitest';
import {
  parseAttrMap, attrMapToText, parseList, listToText, numFromForm, numToForm,
  thresholdsToForm, thresholdsToWire, REDACTEDTEMPLATE, GG_TOTAL_TEMPLATE,
} from './influxForm';

// v0.10.222 — InfluxTab metin kutuları ↔ tel. Sessiz sınıf: boş eşik
// kutusunun tele 0 yerine HİÇ gitmemesi (vmForm dersi) ve attrMap
// satırlarının yorum/boşluk toleransı.

describe('attrMap', () => {
  it('satır başına TAG=attr, `:` de kabul, yorum ve boş satır atlanır', () => {
    expect(parseAttrMap(`REDACTED=operation
# yorum
REDACTED : error.code

REDACTED=k8s.pod.name`)).toEqual({ REDACTED: 'operation', REDACTED: 'error.code', REDACTED: 'k8s.pod.name' });
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
    expect(parseList('REDACTED, REDACTED\n REDACTED ,,')).toEqual(['REDACTED', 'REDACTED', 'REDACTED']);
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

describe('REDACTED şablonu (v0.10.526 — GoldenGate ekibinin sorgusu, Coremetry uyarlaması)', () => {
  it('gruplama kanal + operasyon; attrMap altı tag', () => {
    expect(REDACTEDTEMPLATE.groupBy).toEqual(['REDACTED', 'REDACTED']);
    expect(Object.keys(REDACTEDTEMPLATE.attrMap ?? {}).sort()).toEqual(
      ['REDACTED', 'REDACTED', 'REDACTED', 'REDACTED', 'REDACTED', 'TRACEID']);
    expect(REDACTEDTEMPLATE.attrMap?.TRACEID).toBe('trace_id');
    expect(REDACTEDTEMPLATE.attrMap?.REDACTED).toBe('k8s.pod.name');
  });
  it('SORGU 1: ekibin süzgeçleri (REDACTED/REDACTED, REDACTED =~ /^01/) + Coremetry uyarlamaları; Grafana değişkeni YOK', () => {
    REDACTED")');
    expect(REDACTEDTEMPLATE.flux).toContain('r._measurement == "REDACTED" and r._field == "REDACTED"');
    expect(REDACTEDTEMPLATE.flux).toContain('r.REDACTED =~ /^01/');
    expect(REDACTEDTEMPLATE.flux).toContain('range(start: -2m)');
    expect(REDACTEDTEMPLATE.flux).toContain('group(columns: ["REDACTED", "REDACTED"])');
    expect(REDACTEDTEMPLATE.flux).toContain('aggregateWindow(every: 1m, fn: sum, createEmpty: false)');
    for (const bad of ['v.timeRangeStart', 'v.windowPeriod', 'createEmpty: true', '_value > 4']) {
      expect(REDACTEDTEMPLATE.flux).not.toContain(bad);
    }
  });
  it('SORGU 2 (kanıt) yer tutucuları groupBy tag adlarıyla + from/to; SORGU 1 hiçbirini taşımaz', () => {
    for (const ph of ['{{from}}', '{{to}}', '{{REDACTED}}', '{{REDACTED}}']) {
      expect(REDACTEDTEMPLATE.enrichFlux).toContain(ph);
      expect(REDACTEDTEMPLATE.flux).not.toContain(ph);
    }
    expect(REDACTEDTEMPLATE.enrichFlux).toContain('keep(columns: ["_time", "TRACEID", "REDACTED", "REDACTED", "REDACTED"])');
    expect(REDACTEDTEMPLATE.enrichFlux).toContain('limit(n: 50)');
    // enrich.go her groupBy tag'ını adıyla doldurur; başka yer tutucu HATA olurdu.
    const phs = [...(REDACTEDTEMPLATE.enrichFlux ?? '').matchAll(/\{\{\s*([A-Za-z_]+)\s*\}\}/g)].map(m => m[1]);
    for (const p of phs) expect(['from', 'to', ...(REDACTEDTEMPLATE.groupBy ?? [])]).toContain(p);
  });
  it('GoldenGate toplam şablonu: ekibin ikinci sorgusu, aynı süzgeç + gruplama, kanıt sorgusu yok', () => {
    expect(GG_TOTAL_TEMPLATE.name).toBe('gg_01_adet_total');
    REDACTED")');
    expect(GG_TOTAL_TEMPLATE.flux).toContain('r._field == "REDACTED"');
    expect(GG_TOTAL_TEMPLATE.flux).not.toContain('_measurement ==');
    expect(GG_TOTAL_TEMPLATE.flux).toContain('r.REDACTED =~ /^01/');
    expect(GG_TOTAL_TEMPLATE.flux).toContain('aggregateWindow(every: 1m, fn: sum, createEmpty: false)');
    expect(GG_TOTAL_TEMPLATE.groupBy).toEqual(['REDACTED', 'REDACTED']);
    expect(GG_TOTAL_TEMPLATE.enrichFlux).toBeUndefined();
    expect(GG_TOTAL_TEMPLATE.name).not.toBe(REDACTEDTEMPLATE.name);
  });
});
