import { describe, it, expect } from 'vitest';
import { upsertAttrFilter } from './aggDrill';
import type { FilterExpr } from './types';

// v0.9.856 — UX denetimi K11: /traces aggregate görünümünde "group by
// attribute" ile CHANNEL_CODE'a göre gruplayıp "X100" satırına tıklayan
// kullanıcı liste görünümünde YALNIZ servis filtresini buluyordu.
// `a.groupKey` (tıklanan attribute DEĞERİ) hiçbir filtreye yazılmıyordu:
// liste o servisin TÜM trace'leriydi, satır sayısı grup sayısıyla tutmuyor,
// kullanıcı "X100'ün trace'leri bunlar" sanıyordu.
//
// Bu, boş listeden daha pahalı bir sınıf — "sessizce genişleyen soru":
// ekranda filtrenin eksik olduğunu söyleyen hiçbir şey yok, kullanıcı
// YANLIŞ veriye bakıp emin oluyor.

const F = (k: string, v: string, op = '='): FilterExpr => ({ k, op: op as FilterExpr['op'], v: [v] });

describe('upsertAttrFilter — K11 düşen attribute değeri', () => {
  it('tıklanan değeri filtre olarak yazar', () => {
    expect(upsertAttrFilter([], 'CHANNEL_CODE', 'X100'))
      .toEqual([{ k: 'CHANNEL_CODE', op: '=', v: ['X100'] }]);
  });

  it('mevcut filtreleri korur', () => {
    const prev = [F('http.method', 'POST')];
    expect(upsertAttrFilter(prev, 'CHANNEL_CODE', 'X100')).toEqual([
      { k: 'http.method', op: '=', v: ['POST'] },
      { k: 'CHANNEL_CODE', op: '=', v: ['X100'] },
    ]);
  });

  it('AYNI anahtarı ekleMEZ, DEĞİŞTİRİR — çelişkili AND boş liste demek', () => {
    // X100'e drill edip geri dönüp X200'e drill etmek, append olsaydı
    // `CHANNEL_CODE = X100 AND CHANNEL_CODE = X200` üretirdi: hiçbir satırın
    // eşleşemeyeceği bir sorgu, ekranda yine "trace yok" olarak okunur.
    const after = upsertAttrFilter([F('CHANNEL_CODE', 'X100')], 'CHANNEL_CODE', 'X200');
    expect(after).toEqual([{ k: 'CHANNEL_CODE', op: '=', v: ['X200'] }]);
    expect(after.filter(f => f.k === 'CHANNEL_CODE')).toHaveLength(1);
  });

  it('anahtar eşleşmesi op\'tan bağımsız — eski != koşulu da temizlenir', () => {
    const after = upsertAttrFilter([F('CHANNEL_CODE', 'X100', '!=')], 'CHANNEL_CODE', 'X100');
    expect(after).toEqual([{ k: 'CHANNEL_CODE', op: '=', v: ['X100'] }]);
  });

  it('yalnız aynı anahtarı değiştirir, başkasına dokunmaz', () => {
    const prev = [F('CHANNEL_CODE', 'X100'), F('function_code', 'F9')];
    expect(upsertAttrFilter(prev, 'function_code', 'F7')).toEqual([
      { k: 'CHANNEL_CODE', op: '=', v: ['X100'] },
      { k: 'function_code', op: '=', v: ['F7'] },
    ]);
  });

  it('boş anahtar/değerde filtreleri OLDUĞU GİBİ bırakır', () => {
    // groupAttr kutusu boşken drill `"" = ""` yazmamalı — URL'e ve sorguya
    // anlamsız bir koşul sızardı.
    const prev = [F('http.method', 'POST')];
    for (const [k, v] of [['', 'X100'], ['   ', 'X100'], ['CHANNEL_CODE', '']] as Array<[string, string]>) {
      expect(upsertAttrFilter(prev, k, v)).toBe(prev);
    }
  });

  it('anahtarın baş/son boşluğunu kırpar (kutu serbest metin)', () => {
    expect(upsertAttrFilter([], '  CHANNEL_CODE  ', 'X100'))
      .toEqual([{ k: 'CHANNEL_CODE', op: '=', v: ['X100'] }]);
  });

  it('girdi dizisini MUTATE etmez (React state güvenliği)', () => {
    const prev = [F('CHANNEL_CODE', 'X100')];
    const snapshot = JSON.stringify(prev);
    upsertAttrFilter(prev, 'CHANNEL_CODE', 'X200');
    expect(JSON.stringify(prev)).toBe(snapshot);
  });
});
