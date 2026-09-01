// v0.10.228 (Influx D3) — dış (Influx) özne: `ext:<kaynak>/<grup değerleri>`.
// Servis sayfası linki YOK (özne bir servis değil); eski sekmenin JSON'u
// kind alanı taşımasa bile önekten tanınır (db: emsali, v0.9.1342).
import { describe, it, expect } from 'vitest';
import { subjectKind, subjectIsLinkable, subjectLabel, subjectTitle } from './problemSubject';

describe('external özne', () => {
  it("kind='external' → external, link yok", () => {
    expect(subjectKind('ext:ggfail/OP1/E1', 'external')).toBe('external');
    expect(subjectIsLinkable('ext:ggfail/OP1/E1', 'external')).toBe(false);
  });
  it('kind yokken ext: öneki yeter', () => {
    expect(subjectKind('ext:ggfail/OP1/E1')).toBe('external');
    expect(subjectKind('ext:ggfail/OP1/E1', '')).toBe('external');
  });
  it('etiket: kaynak · grup değerleri', () => {
    expect(subjectLabel('ext:ggfail/OP1/E1')).toBe('ggfail · OP1 · E1');
    expect(subjectLabel('ext:ggfail')).toBe('ggfail');
  });
  it('başlık dış kaynağı açıklar', () => {
    expect(subjectTitle('ext:ggfail/OP1/E1')).toMatch(/dış|Influx/i);
  });
  it('servis-şekilli özneyi external YAPMAZ', () => {
    expect(subjectKind('checkout-api')).toBe('service');
    expect(subjectKind('checkout-api', 'external')).toBe('external');
  });
});
