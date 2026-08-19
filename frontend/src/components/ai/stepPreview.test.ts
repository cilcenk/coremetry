import { describe, expect, it } from 'vitest';
import { parseStepPreview, fmtPreviewBytes } from './stepPreview';

// v0.9.1181 (AI Faz 4.3) — tool çıktısının çizilebilir hâli.
//
// Bu modülün tamamı kenar durum: bir tool ne döndürürse döndürsün blok
// KIRILMADAN ve UYDURMADAN bir şey göstermek zorunda. "Uydurmamak" burada
// somut bir davranış: ayrıştırılamayan gövde HAM gösterilir, tahmini bir
// şekle zorlanmaz — kanıtı görünür kılmak için yazılmış bir dilimin
// kanıtı düzeltmesi kendi amacını yer.

describe('parseStepPreview — tablo dalı', () => {
  it('düz nesne dizisi tabloya döner', () => {
    const v = parseStepPreview('[{"service":"api","calls":12},{"service":"db","calls":3}]');
    expect(v.kind).toBe('table');
    if (v.kind !== 'table') return;
    expect(v.cols).toEqual(['service', 'calls']);
    expect(v.rows).toEqual([['api', '12'], ['db', '3']]);
  });

  it('sarmalanmış tek dizi alanı bulunur', () => {
    const v = parseStepPreview('{"ok":true,"rows":[{"a":1}]}');
    expect(v.kind).toBe('table');
    if (v.kind !== 'table') return;
    expect(v.cols).toEqual(['a']);
  });

  it('sütunlar BÜTÜN satırların birleşimi — sonradan beliren alan yutulmaz', () => {
    // İlk satırı örnek almak `err`i sessizce düşürürdü.
    const v = parseStepPreview('[{"a":1},{"a":2,"err":"boom"}]');
    expect(v.kind).toBe('table');
    if (v.kind !== 'table') return;
    expect(v.cols).toEqual(['a', 'err']);
    expect(v.rows[0]).toEqual(['1', '']);
  });

  it('iki dizi alanı varsa TAHMİN ETMEZ, ham gösterir', () => {
    expect(parseStepPreview('{"rows":[{"a":1}],"other":[{"b":2}]}').kind).toBe('text');
  });

  it('iç içe değer taşıyan satırlar tabloya zorlanmaz', () => {
    expect(parseStepPreview('[{"a":{"deep":1}}]').kind).toBe('text');
  });

  it('50 satırdan fazlası ham JSON', () => {
    const many = JSON.stringify(Array.from({ length: 51 }, (_, i) => ({ i })));
    expect(parseStepPreview(many).kind).toBe('text');
  });
});

describe('parseStepPreview — metin dalı (uydurma yok)', () => {
  it('kırpılmış JSON onarılmaz, ham gider', () => {
    // Gerçek kırpma böyle görünür: yapının ortasından kesilmiş.
    const cut = '[{"service":"api","calls":12},{"service":"db","cal';
    const v = parseStepPreview(cut, true);
    expect(v.kind).toBe('text');
    if (v.kind !== 'text') return;
    expect(v.text).toBe(cut);
  });

  it('JSON olmayan çıktı ham gider', () => {
    expect(parseStepPreview('error: upstream 502')).toEqual({ kind: 'text', text: 'error: upstream 502' });
  });

  it('boş dizi tabloya değil ham JSON’a düşer', () => {
    expect(parseStepPreview('[]').kind).toBe('text');
  });

  it('boş girdi boş metin', () => {
    expect(parseStepPreview('')).toEqual({ kind: 'text', text: '' });
    expect(parseStepPreview('   ')).toEqual({ kind: 'text', text: '' });
  });

  it('skaler JSON ham gider', () => {
    expect(parseStepPreview('42').kind).toBe('text');
    expect(parseStepPreview('"merhaba"').kind).toBe('text');
  });
});

describe('parseStepPreview — hücre biçimleme', () => {
  it('null ve boolean okunur biçimde', () => {
    const v = parseStepPreview('[{"a":null,"b":true}]');
    if (v.kind !== 'table') throw new Error('tablo bekleniyordu');
    expect(v.rows[0]).toEqual(['', 'true']);
  });

  it('kırpılmış gövdeden gelen tablo bunu NOT olarak taşır', () => {
    const v = parseStepPreview('[{"a":1}]', true);
    if (v.kind !== 'table') throw new Error('tablo bekleniyordu');
    expect(v.note).toBe('kırpılmış gövdeden');
  });
});

describe('fmtPreviewBytes', () => {
  it.each([
    [0, '0 B'],
    [-5, '0 B'],
    [512, '512 B'],
    [1024, '1.0 KB'],
    [4096, '4.0 KB'],
    [1024 * 1024, '1.0 MB'],
  ])('%d → %s', (n, want) => {
    expect(fmtPreviewBytes(n)).toBe(want);
  });
});
