import { describe, expect, it } from 'vitest';
import {
  parseAttrMap, attrMapToText, parseList, listToText, numFromForm, numToForm,
  thresholdsToForm, thresholdsToWire, TFAIL_TEMPLATE, GG_TOTAL_TEMPLATE,
  TFAIL_RATIO_TEMPLATE, ratioToForm, ratioToWire, EMPTY_RATIO,
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
  it('gruplama kanal + fonksiyon + operasyon (v0.10.548, ekibin paneli); attrMap altı tag', () => {
    expect(TFAIL_TEMPLATE.groupBy).toEqual(['KANALKOD', 'FUNCTIONCODE', 'OPERATIONCODE']);
    expect(Object.keys(TFAIL_TEMPLATE.attrMap ?? {}).sort()).toEqual(
      ['ERRORCODE', 'FUNCTIONCODE', 'INSTANCEID', 'KANALKOD', 'OPERATIONCODE', 'TRACEID']);
    expect(TFAIL_TEMPLATE.attrMap?.TRACEID).toBe('trace_id');
    expect(TFAIL_TEMPLATE.attrMap?.INSTANCEID).toBe('k8s.pod.name');
  });
  it('SORGU 1: TFAIL/ADET, kanal süzgeci YOK (v0.10.528) + Coremetry uyarlamaları; Grafana değişkeni YOK', () => {
    expect(TFAIL_TEMPLATE.flux).toContain('from(bucket: "GGFailTraceBckt")');
    expect(TFAIL_TEMPLATE.flux).toContain('r._measurement == "TFAIL" and r._field == "ADET"');
    expect(TFAIL_TEMPLATE.flux).not.toContain('KANALKOD =~'); // v0.10.528 kanal süzgeci yok
    expect(TFAIL_TEMPLATE.name).toBe('tfail_adet');
    expect(TFAIL_TEMPLATE.flux).toContain('range(start: -2h)'); // v0.10.527 gecikmeli kaynak
    expect(TFAIL_TEMPLATE.flux).toContain('group(columns: ["KANALKOD", "FUNCTIONCODE", "OPERATIONCODE"])');
    expect(TFAIL_TEMPLATE.flux).toContain('aggregateWindow(every: 1m, fn: sum, createEmpty: false)');
    for (const bad of ['v.timeRangeStart', 'v.windowPeriod', 'createEmpty: true', '_value > 4']) {
      expect(TFAIL_TEMPLATE.flux).not.toContain(bad);
    }
  });
  it('SORGU 2 (kanıt) yer tutucuları groupBy tag adlarıyla + from/to; SORGU 1 hiçbirini taşımaz', () => {
    for (const ph of ['{{from}}', '{{to}}', '{{KANALKOD}}', '{{FUNCTIONCODE}}', '{{OPERATIONCODE}}']) {
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
    expect(GG_TOTAL_TEMPLATE.name).toBe('gg_adet_total');
    expect(GG_TOTAL_TEMPLATE.flux).toContain('from(bucket: "GoldenGateBucket")');
    expect(GG_TOTAL_TEMPLATE.flux).toContain('r._field == "ADET"');
    expect(GG_TOTAL_TEMPLATE.flux).toContain('range(start: -2h)');
    expect(GG_TOTAL_TEMPLATE.flux).not.toContain('_measurement ==');
    expect(GG_TOTAL_TEMPLATE.flux).not.toContain('KANALKOD =~');
    expect(GG_TOTAL_TEMPLATE.flux).toContain('aggregateWindow(every: 1m, fn: sum, createEmpty: false)');
    expect(GG_TOTAL_TEMPLATE.groupBy).toEqual(['KANALKOD', 'OPERATIONCODE']);
    expect(GG_TOTAL_TEMPLATE.enrichFlux).toBeUndefined();
    expect(GG_TOTAL_TEMPLATE.name).not.toBe(TFAIL_TEMPLATE.name);
  });
});

// v0.10.532 — türetilmiş oran: şablon iki girdiyi ADIYLA bağlar, aynı
// gruplamayı taşır (sunucu groupBy eşitliğini reddeder), Flux boş; kutu
// boş = sunucu varsayılanı (tel'e yazılmaz).
describe('ratio', () => {
  it('şablon: tfail_adet ÷ gg_adet_total, aynı groupBy, flux boş, kanıt payın SORGU 2\'si', () => {
    expect(TFAIL_RATIO_TEMPLATE.name).toBe('tfail_oran');
    expect(TFAIL_RATIO_TEMPLATE.flux).toBe('');
    expect(TFAIL_RATIO_TEMPLATE.ratio).toEqual({ numerator: TFAIL_TEMPLATE.name, denominator: GG_TOTAL_TEMPLATE.name });
    // v0.10.548 — oran PAYDANIN taneciğinde; pay üst küme (sunucu fazlayı toplar).
    expect(TFAIL_RATIO_TEMPLATE.groupBy).toEqual(GG_TOTAL_TEMPLATE.groupBy);
    for (const g of TFAIL_RATIO_TEMPLATE.groupBy ?? []) expect(TFAIL_TEMPLATE.groupBy).toContain(g);
    expect(TFAIL_TEMPLATE.groupBy?.length).toBeGreaterThan(TFAIL_RATIO_TEMPLATE.groupBy?.length ?? 0);
    // Kanıt sorgusu yer tutucuları yalnız ORANIN tag'ları + from/to (enrich.go
    // oranın groupBy'ını doldurur; {{FUNCTIONCODE}} kalsaydı boş süzgeç = 0 satır).
    const phs = [...(TFAIL_RATIO_TEMPLATE.enrichFlux ?? '').matchAll(/\{\{\s*([A-Za-z_]+)\s*\}\}/g)].map(m => m[1]);
    expect(phs.sort()).toEqual(['KANALKOD', 'OPERATIONCODE', 'from', 'to'].sort());
    expect(TFAIL_RATIO_TEMPLATE.enrichFlux).not.toBe(TFAIL_TEMPLATE.enrichFlux);
    // Varsayılanlar şablona BASILMAZ (min payda / bekletme sunucuda).
    expect(TFAIL_RATIO_TEMPLATE.ratio?.minDenominator).toBeUndefined();
    expect(TFAIL_RATIO_TEMPLATE.ratio?.settleBuckets).toBeUndefined();
  });
  it('form ↔ tel: boş kutu tel\'e gitmez, dolu kutu sayı olur', () => {
    expect(ratioToForm(undefined)).toEqual(EMPTY_RATIO);
    const f = ratioToForm({ numerator: 'a', denominator: 'b', minDenominator: 50, settleBuckets: 3 });
    expect(f).toEqual({ numerator: 'a', denominator: 'b', minDenominator: '50', settleBuckets: '3' });
    expect(ratioToWire(f)).toEqual({ numerator: 'a', denominator: 'b', minDenominator: 50, settleBuckets: 3 });
    expect(ratioToWire({ numerator: ' a ', denominator: 'b', minDenominator: '', settleBuckets: '' }))
      .toEqual({ numerator: 'a', denominator: 'b', minDenominator: undefined, settleBuckets: undefined });
  });
});
