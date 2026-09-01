import { describe, expect, it } from 'vitest';
import {
  parseAttrMap, attrMapToText, parseList, listToText, numFromForm, numToForm,
  thresholdsToForm, thresholdsToWire, TFAIL_TEMPLATE,
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

describe('TFAIL şablonu (spec)', () => {
  it('gruplama v1: yalnız OPERATIONCODE + ERRORCODE; attrMap altı tag', () => {
    expect(TFAIL_TEMPLATE.groupBy).toEqual(['OPERATIONCODE', 'ERRORCODE']);
    expect(Object.keys(TFAIL_TEMPLATE.attrMap ?? {}).sort()).toEqual(
      ['ERRORCODE', 'FUNCTIONCODE', 'INSTANCEID', 'KANALKOD', 'OPERATIONCODE', 'TRACEID']);
    expect(TFAIL_TEMPLATE.attrMap?.TRACEID).toBe('trace_id');
    expect(TFAIL_TEMPLATE.attrMap?.INSTANCEID).toBe('k8s.pod.name');
  });
  it('SORGU 1 Grafana ile hizalı: GoldenGateBucket + 1 dk aggregateWindow + gürültü filtreleri, _value tabanı YOK', () => {
    expect(TFAIL_TEMPLATE.flux).toContain('from(bucket: "GGFailTraceBckt")');
    expect(TFAIL_TEMPLATE.flux).toContain('aggregateWindow(every: 1m, fn: sum, createEmpty: false)');
    expect(TFAIL_TEMPLATE.flux).toContain('r.OPERATIONCODE != "0"');
    expect(TFAIL_TEMPLATE.flux).toContain('r.FUNCTIONCODE != "N/A"');
    expect(TFAIL_TEMPLATE.flux).not.toContain('_value > 4');
    expect(TFAIL_TEMPLATE.enrichFlux).toContain('from(bucket: "GGFailTraceBckt")');
  });

  it('SORGU 2 dört yer tutucuyu taşır, SORGU 1 hiçbirini taşımaz', () => {
    for (const ph of ['{{from}}', '{{to}}', '{{op}}', '{{err}}']) {
      expect(TFAIL_TEMPLATE.enrichFlux).toContain(ph);
    }
    expect(TFAIL_TEMPLATE.flux).not.toContain('{{');
    expect(TFAIL_TEMPLATE.enrichFlux).toContain('limit(n: 50)');
  });
});
