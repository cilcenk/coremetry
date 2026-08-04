import { describe, expect, it } from 'vitest';
import { formatColsParam, parseColsParam } from './endpointCols';

// v0.8.574 — /endpoints kolon göster/gizle codec'i. The URL contract
// matches Logs `?cols=`: absent param = default (all visible), and the
// param round-trips through parse→format for every non-default subset.

const ALL = ['service', 'path', 'method', 'calls', 'traces'] as const;

describe('parseColsParam', () => {
  it('null and empty mean all columns visible', () => {
    expect(parseColsParam(null, ALL)).toEqual(new Set(ALL));
    expect(parseColsParam('', ALL)).toEqual(new Set(ALL));
  });

  it('parses a subset and drops unknown ids', () => {
    expect(parseColsParam('service,calls,nope', ALL)).toEqual(new Set(['service', 'calls']));
  });

  it('tolerates whitespace around ids', () => {
    expect(parseColsParam(' service , path ', ALL)).toEqual(new Set(['service', 'path']));
  });

  it('falls back to all when nothing valid survives — never a column-less table', () => {
    expect(parseColsParam('bogus,junk', ALL)).toEqual(new Set(ALL));
  });
});

describe('formatColsParam', () => {
  it('all visible → empty string (caller deletes the param)', () => {
    expect(formatColsParam(new Set(ALL), ALL)).toBe('');
  });

  it('subset emits canonical column order regardless of insertion order', () => {
    expect(formatColsParam(new Set(['calls', 'service']), ALL)).toBe('service,calls');
  });

  it('round-trips every non-default subset', () => {
    const subset = new Set(['path', 'traces']);
    expect(parseColsParam(formatColsParam(subset, ALL), ALL)).toEqual(subset);
  });

  it('ignores ids not in the schema', () => {
    expect(formatColsParam(new Set(['service', 'ghost']), ALL)).toBe('service');
  });
});

// v0.9.642 — operatör-bildirimli: "Endpoints tablosunda çok kolon olduğu
// için içerik sızıyor". Varsayılan kolon kümesi daraldı.
//
// Daraltma HİÇBİR ŞEY SİLMİYOR: gizlenenler ColumnManager'da bir tık
// ötede, ?cols= ile hâlâ adreslenebilir. Bu testler o sözleşmeyi
// çiviliyor.
describe('defaultIds — varsayılan alt küme olabilir', () => {
  const ALL2 = ['a', 'b', 'c', 'd'];
  const DEF = ['a', 'b'];

  it('param yoksa VARSAYILAN döner, hepsi değil', () => {
    expect(parseColsParam(null, ALL2, DEF)).toEqual(new Set(DEF));
    expect(parseColsParam('', ALL2, DEF)).toEqual(new Set(DEF));
  });

  it('varsayılan dışı kolon ?cols= ile GERİ GELEBİLİR', () => {
    expect(parseColsParam('a,c', ALL2, DEF)).toEqual(new Set(['a', 'c']));
    expect(parseColsParam('a,b,c,d', ALL2, DEF)).toEqual(new Set(ALL2));
  });

  it('varsayılan görünümde URL TEMİZ kalıyor', () => {
    expect(formatColsParam(new Set(DEF), ALL2, DEF)).toBe('');
  });

  it('hepsi görünürken artık param YAZILIYOR (varsayılan değil çünkü)', () => {
    expect(formatColsParam(new Set(ALL2), ALL2, DEF)).toBe('a,b,c,d');
  });

  it('defaultIds atlanırsa eski davranış birebir korunuyor', () => {
    expect(parseColsParam(null, ALL2)).toEqual(new Set(ALL2));
    expect(formatColsParam(new Set(ALL2), ALL2)).toBe('');
  });

  // Boş bir varsayılan kolonsuz tablo demek — paylaşılan link asla
  // kolonsuz bir tablo üretmemeli (kodeğin özgün sözleşmesi).
  it('geçersiz ?cols= varsayılana düşüyor', () => {
    expect(parseColsParam('zzz,yyy', ALL2, DEF)).toEqual(new Set(DEF));
  });
});
