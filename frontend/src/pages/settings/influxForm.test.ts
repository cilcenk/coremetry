import { describe, expect, it } from 'vitest';
import {
  parseAttrMap, attrMapToText, parseList, listToText, numFromForm, numToForm,
  thresholdsToForm, thresholdsToWire, REDACTEDTEMPLATE,
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

describe('REDACTED şablonu (spec)', () => {
  it('gruplama v1: yalnız REDACTED + REDACTED; attrMap altı tag', () => {
    expect(REDACTEDTEMPLATE.groupBy).toEqual(['REDACTED', 'REDACTED']);
    expect(Object.keys(REDACTEDTEMPLATE.attrMap ?? {}).sort()).toEqual(
      ['REDACTED', 'REDACTED', 'REDACTED', 'REDACTED', 'REDACTED', 'TRACEID']);
    expect(REDACTEDTEMPLATE.attrMap?.TRACEID).toBe('trace_id');
    expect(REDACTEDTEMPLATE.attrMap?.REDACTED).toBe('k8s.pod.name');
  });
  it('SORGU 2 dört yer tutucuyu taşır, SORGU 1 hiçbirini taşımaz', () => {
    for (const ph of ['{{from}}', '{{to}}', '{{op}}', '{{err}}']) {
      expect(REDACTEDTEMPLATE.enrichFlux).toContain(ph);
    }
    expect(REDACTEDTEMPLATE.flux).not.toContain('{{');
    expect(REDACTEDTEMPLATE.enrichFlux).toContain('limit(n: 50)');
  });
});
