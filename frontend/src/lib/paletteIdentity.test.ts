import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { paletteIdentityQuery, identityTracesHref } from './paletteIdentity';

// v0.10.674 — operatör (prod): "aynı function_id global search'ten
// bulunmuyor ama Traces sayfasında girince buluyor". Kök neden: komut
// paleti eşleştirme için sorguyu küçük harfe çeviriyor (q = query.trim()
// .toLowerCase()) ve "Kimlikle trace ara" önerisi de o küçük harfli q'yu
// /traces?traceId= ile gönderiyordu; sunucunun kimlik-önce yolu
// attr_function_id = ? ile harf-DUYARLI eşitlik yapar → "…vzXA…" değeri
// "…vzxa…" olarak 0 satır. Traces kutusuna aynen yazınca bulunuyordu.
//
// SÖZLEŞME: kimlik değeri OLDUĞU GİBİ gider (trim; büyük/küçük harf
// korunur). Mutasyon (ölçüldü): helper'a toLowerCase() eklemek 1. testi düşürür.
describe('paletteIdentityQuery (v0.10.674)', () => {
  it('büyük/küçük harfi KORUR, yalnız kırpar', () => {
    expect(paletteIdentityQuery('  AB12cdEF34gh5678 ')).toBe('AB12cdEF34gh5678');
    expect(paletteIdentityQuery('060203vzXA0051685896')).toBe('060203vzXA0051685896');
  });

  it('kimlik kalıbı dışını reddeder (kısa, rakamsız, boşluklu)', () => {
    expect(paletteIdentityQuery('abc1')).toBeNull();
    expect(paletteIdentityQuery('abcdefghij')).toBeNull();
    expect(paletteIdentityQuery('foo bar 123')).toBeNull();
    expect(paletteIdentityQuery('')).toBeNull();
  });

  it('href değeri kodlar, harfe dokunmaz', () => {
    expect(identityTracesHref('A1:b2.c3-d4_e5')).toBe('/traces?traceId=A1%3Ab2.c3-d4_e5');
  });
});

describe('BAĞLANMA (CommandPalette.tsx)', () => {
  const src = readFileSync(resolve(__dirname, '../components/CommandPalette.tsx'), 'utf8');
  it('kimlik önerisi ham sorgudan türer, küçük harfli q ile değil', () => {
    expect(src).toContain('paletteIdentityQuery(query)');
    const i = src.indexOf('Kimlikle trace ara');
    expect(i).toBeGreaterThan(-1);
    const line = src.slice(src.lastIndexOf('\n', i), src.indexOf('\n', i));
    expect(line).toContain('identityTracesHref(idv)');
    expect(line).not.toContain('encodeURIComponent(q)');
  });
});
