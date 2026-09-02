// columnModel.test.ts — v0.10.246 DataTable dilim 1 sözleşmesi (columnModel.ts başlığı).
import { describe, it, expect } from 'vitest';
import {
  defaultColumnModel, reconcileColumnModel, visibleColumnIds, toggleHidden, moveColumnTo,
  parseColsParam, modelFromVisible, resolveColumnModel, serializeColumnModel, parseColumnModel, orderColumnsByModel,
  type ColumnModel,
} from './columnModel';

const cols = [
  { id: 'time', hideable: false }, { id: 'operation', hideable: false }, { id: 'service' },
  { id: 'duration' }, { id: 'status' }, { id: 'channel_code' }, { id: 'function_code' },
];
const SIG = 'sig-a';

describe('reconcileColumnModel', () => {
  it('bilinmeyen id düşer, yeni id tanımdaki komşusunun arkasına girer', () => {
    const saved: ColumnModel = { v: 1, order: ['time', 'ghost', 'duration', 'operation'], hidden: ['ghost'], sig: SIG };
    const m = reconcileColumnModel(saved, cols, SIG);
    expect(m.order).not.toContain('ghost');
    expect(m.hidden).not.toContain('ghost');
    // service tanımda operation'dan sonra → sırada operation'ın arkasına
    expect(m.order.indexOf('service')).toBe(m.order.indexOf('operation') + 1);
    // status tanımda duration'dan sonra → duration'ın arkasına
    expect(m.order.indexOf('status')).toBe(m.order.indexOf('duration') + 1);
    expect(new Set(m.order).size).toBe(cols.length);
  });
  it('hideable:false gizlenemez', () => {
    const m = reconcileColumnModel({ v: 1, order: cols.map(c => c.id), hidden: ['time', 'service'], sig: SIG }, cols, SIG);
    expect(m.hidden).toEqual(['service']);
    expect(toggleHidden(m, 'operation', cols)).toBe(m);
    expect(toggleHidden(m, 'service', cols).hidden).toEqual([]);
  });
  it('sig uyuşmazsa genişlik düşer, order/hidden kalır', () => {
    const saved: ColumnModel = { v: 1, order: ['duration', 'time', 'operation', 'service', 'status', 'channel_code', 'function_code'], hidden: ['status'], sig: 'old', widths: { duration: 120 } };
    const m = reconcileColumnModel(saved, cols, SIG);
    expect(m.widths).toBeUndefined();
    expect(m.order[0]).toBe('duration');
    expect(m.hidden).toEqual(['status']);
    expect(m.sig).toBe(SIG);
    const same = reconcileColumnModel({ ...saved, sig: SIG }, cols, SIG);
    expect(same.widths).toEqual({ duration: 120 });
  });
  it('null/eski sürüm → varsayılan', () => {
    expect(reconcileColumnModel(null, cols, SIG)).toEqual(defaultColumnModel(cols, SIG));
    expect(reconcileColumnModel({ v: 0 as unknown as 1, order: [], hidden: [], sig: '' }, cols, SIG)).toEqual(defaultColumnModel(cols, SIG));
  });
});

describe('resolveColumnModel — kaynak önceliği', () => {
  const server: ColumnModel = { v: 1, order: ['time', 'operation', 'channel_code', 'service', 'duration', 'status', 'function_code'], hidden: ['function_code'], sig: SIG };
  const local: ColumnModel = { v: 1, order: cols.map(c => c.id), hidden: ['channel_code'], sig: SIG };
  it('URL cols= kazanır; boş dize "yok" sayılır', () => {
    const r = resolveColumnModel({ urlCols: 'time,operation,duration', server, local, columns: cols, sig: SIG });
    expect(r.source).toBe('url');
    expect(visibleColumnIds(r.model)).toEqual(['time', 'operation', 'duration']);
    expect(resolveColumnModel({ urlCols: '', server, local, columns: cols, sig: SIG }).source).toBe('server');
    expect(resolveColumnModel({ urlCols: ' , ', server, local, columns: cols, sig: SIG }).source).toBe('server');
    expect(parseColsParam(null)).toBeNull();
  });
  it('sunucu > yerel > varsayılan', () => {
    expect(resolveColumnModel({ server, local, columns: cols, sig: SIG }).source).toBe('server');
    expect(resolveColumnModel({ local, columns: cols, sig: SIG }).source).toBe('local');
    const d = resolveColumnModel({ columns: cols, sig: SIG });
    expect(d.source).toBe('default');
    expect(visibleColumnIds(d.model)).toEqual(cols.map(c => c.id));
  });
  it('URL görünür listesi kimlik kolonlarını gizleyemez', () => {
    const m = modelFromVisible(['duration'], cols, SIG);
    expect(visibleColumnIds(m)).toEqual(['duration', 'time', 'operation']);
  });
});

describe('moveColumnTo / serialize / parse', () => {
  it('taşıma sınırları kelepçelenir', () => {
    const m = defaultColumnModel(cols, SIG);
    expect(moveColumnTo(m, 'status', 0).order[0]).toBe('status');
    expect(moveColumnTo(m, 'time', 99).order.at(-1)).toBe('time');
    expect(moveColumnTo(m, 'nope', 1)).toBe(m);
  });
  it('serialize genişlikleri taşımaz; parse bozuk girdiyi null yapar', () => {
    const m: ColumnModel = { ...defaultColumnModel(cols, SIG), widths: { time: 100 } };
    const s = serializeColumnModel(m);
    expect(s).not.toContain('widths');
    expect(parseColumnModel(s)).toEqual({ v: 1, order: m.order, hidden: [], sig: SIG });
    expect(parseColumnModel('{not json')).toBeNull();
    expect(parseColumnModel({ v: 2, order: [], hidden: [] })).toBeNull();
    expect(parseColumnModel({ v: 1, order: ['a', 5, ''], hidden: [], widths: { a: 10, b: -1 } })).toEqual({ v: 1, order: ['a'], hidden: [], sig: '', widths: { a: 10 } });
  });
});

describe('orderColumnsByModel', () => {
  it('modele göre sıralar, gizlileri düşürür, modelde olmayanı sona ekler', () => {
    const declared = [{ id: 'a' }, { id: 'b' }, { id: 'c' }, { id: 'd' }];
    const model: ColumnModel = { v: 1, order: ['c', 'zzz', 'a', 'b'], hidden: ['b'], sig: 's' };
    expect(orderColumnsByModel(declared, model).map(c => c.id)).toEqual(['c', 'a', 'd']);
    expect(orderColumnsByModel(declared, null)).toBe(declared);
  });
});
