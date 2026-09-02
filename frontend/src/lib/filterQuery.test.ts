// filterQuery.test.ts — v0.10.264 tek satır sorgu kutusu çekirdeği sözleşmesi:
// op kısaltmaları (≠ ~ ∃), satır içi ayrıştırma (değer içindeki = korunur,
// IN listesi, EXISTS değersiz, boşluklu anahtar reddi), çip etiketi,
// upsert (aynı k+op günceller), son kullanılanlar (başa, tekrar düşer, ≤5,
// bozuk JSON → []), anahtar sıralaması (tam > önek > içerik > gözlem sayısı).
import { describe, it, expect } from 'vitest';
import {
  opFromShorthand, parseInlineFilter, splitListValues, chipValueLabel, upsertFilter,
  pushRecent, parseRecent, rankKeys, OP_SHORT, FILTER_OPS,
} from './filterQuery';

describe('opFromShorthand', () => {
  it('kısaltmalar ve kelimeler', () => {
    expect(opFromShorthand('≠')).toBe('!=');
    expect(opFromShorthand('!=')).toBe('!=');
    expect(opFromShorthand('~')).toBe('LIKE');
    expect(opFromShorthand('!~')).toBe('NOT LIKE');
    expect(opFromShorthand('NOT   IN')).toBe('NOT IN');
    expect(opFromShorthand('exists')).toBe('EXISTS');
    expect(opFromShorthand('∄')).toBe('NOT EXISTS');
    expect(opFromShorthand('>=')).toBe('>=');
    expect(opFromShorthand('??')).toBeNull();
    for (const op of FILTER_OPS) expect(OP_SHORT[op]).toBeTruthy();
  });
});

describe('parseInlineFilter', () => {
  it('temel şekiller', () => {
    expect(parseInlineFilter('http.route=/api/v1/accounts/{id}/balance')).toEqual({ k: 'http.route', op: '=', v: ['/api/v1/accounts/{id}/balance'] });
    expect(parseInlineFilter('channel_code in MOBILE, WEB,MOBILE')).toEqual({ k: 'channel_code', op: 'IN', v: ['MOBILE', 'WEB'] });
    expect(parseInlineFilter('status_code != ok')).toEqual({ k: 'status_code', op: '!=', v: ['ok'] });
    expect(parseInlineFilter('duration >= 100')).toEqual({ k: 'duration', op: '>=', v: ['100'] });
    expect(parseInlineFilter('error.type exists')).toEqual({ k: 'error.type', op: 'EXISTS', v: [] });
    expect(parseInlineFilter('function_code ~ TRN')).toEqual({ k: 'function_code', op: 'LIKE', v: ['TRN'] });
  });
  it('değer içindeki = korunur; boşluklu anahtar / eksik değer / boş → null', () => {
    expect(parseInlineFilter('http.route=/x?a=b')).toEqual({ k: 'http.route', op: '=', v: ['/x?a=b'] });
    expect(parseInlineFilter('http route=/x')).toBeNull();
    expect(parseInlineFilter('http.route=')).toBeNull();
    expect(parseInlineFilter('TRN')).toBeNull();
    expect(parseInlineFilter('  ')).toBeNull();
  });
});

describe('chip / upsert / recent / rank', () => {
  it('chipValueLabel + splitListValues', () => {
    expect(chipValueLabel({ k: 'a', op: 'IN', v: ['x', 'y'] })).toBe('x, y');
    expect(chipValueLabel({ k: 'a', op: 'EXISTS', v: [] })).toBe('');
    expect(splitListValues(' a, ,b ,a')).toEqual(['a', 'b']);
  });
  it('upsertFilter aynı k+op günceller', () => {
    const out = upsertFilter([{ k: 'a', op: '=', v: ['1'] }], { k: 'a', op: '=', v: ['2'] });
    expect(out).toEqual([{ k: 'a', op: '=', v: ['2'] }]);
    expect(upsertFilter(out, { k: 'a', op: '!=', v: ['3'] }).length).toBe(2);
  });
  it('pushRecent başa alır, tekrar düşer, ≤5; parseRecent toleranslı', () => {
    let r = pushRecent([], { k: 'a', op: '=', v: ['1'] });
    r = pushRecent(r, { k: 'b', op: '=', v: ['2'] });
    r = pushRecent(r, { k: 'a', op: '=', v: ['1'] });
    expect(r.map(f => f.k)).toEqual(['a', 'b']);
    for (let i = 0; i < 10; i++) r = pushRecent(r, { k: `k${i}`, op: '=', v: ['v'] });
    expect(r.length).toBe(5);
    expect(parseRecent(JSON.stringify(r))).toEqual(r);
    expect(parseRecent('{bad')).toEqual([]);
    expect(parseRecent(JSON.stringify([{ k: 'a', op: 'BOGUS', v: [] }, { k: '', op: '=', v: [] }, { k: 'ok', op: '=', v: ['1', 2] }]))).toEqual([{ k: 'ok', op: '=', v: ['1'] }]);
  });
  it('rankKeys sırası', () => {
    const keys = ['http.route', 'http.method', 'channel_code', 'function_code', 'db.system'];
    expect(rankKeys(keys, 'http')).toEqual(['http.method', 'http.route']);
    expect(rankKeys(keys, 'http', { 'http.route': 100, 'http.method': 5 })).toEqual(['http.route', 'http.method']);
    expect(rankKeys(keys, 'code')).toEqual(['channel_code', 'function_code']);
    expect(rankKeys(keys, 'db.system')[0]).toBe('db.system');
    expect(rankKeys(keys, '', { 'db.system': 9 })[0]).toBe('db.system');
    expect(rankKeys(keys, 'zzz')).toEqual([]);
  });
});
