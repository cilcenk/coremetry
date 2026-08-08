import { describe, expect, it } from 'vitest';
import {
  hasMeaningfulParams, exploreQuerySig, nextExploreKey,
  EXPLORE_ENTRY_KEY, type ExploreKeyState,
} from './exploreRouteKey';

// v0.9.805 regression — ExploreInner URL'i mount başına BİR KEZ çözüyor
// (builderSeed guard) ve remount yalnız `key={meaningful?'workspace':'entry'}`
// sınırında oluyordu. Çalışan bir Explore'da saved view uygulamak URL'i
// değiştiriyor ama anahtar sabit kaldığı için ekran ESKİ sorguda kalıyordu;
// ilk düzenlemede de state→URL yazımı saved view'i eziyordu.
//
// Düzeltmenin tuzağı: imzayı düz katarsak operatörün HER düzenlemesi (state
// → URL yazımı) remount tetikler. Aşağıdaki tablo üç davranışı birlikte
// pinliyor: kendi-yazımı remount ETMEZ · saved-view remount EDER · range
// değişimi remount ETMEZ.

const A_QUERY = '?q=%7B%22queries%22%3A%5B%7B%22l%22%3A%22A%22%7D%5D%7D&range=30m';
const B_QUERY = '?q=%7B%22queries%22%3A%5B%7B%22l%22%3A%22B%22%7D%5D%7D&range=30m';

const at = (search: string): ExploreKeyState =>
  ({ key: `workspace:${exploreQuerySig(search)}`, sig: exploreQuerySig(search) });

describe('hasMeaningfulParams', () => {
  it.each([
    ['', false],
    ['?range=30m', false],
    ['?range=6h', false],
    ['?q=abc', true],
    ['?q=abc&range=30m', true],
    ['?result=traces', true],
    ['?source=logs', true],
  ])('%s → %s', (search, want) => {
    expect(hasMeaningfulParams(new URLSearchParams(search))).toBe(want);
  });
});

describe('exploreQuerySig', () => {
  it('range imzayı ETKİLEMEZ', () => {
    expect(exploreQuerySig('?q=abc&range=30m')).toBe(exploreQuerySig('?q=abc&range=7d'));
    expect(exploreQuerySig('?q=abc')).toBe(exploreQuerySig('?q=abc&range=1h'));
  });

  it('param SIRASI imzayı etkilemez', () => {
    expect(exploreQuerySig('?result=traces&filters=x&range=30m'))
      .toBe(exploreQuerySig('?range=30m&filters=x&result=traces'));
  });

  it('boş değerli paramlar imzayı etkilemez', () => {
    expect(exploreQuerySig('?q=abc&dsl=')).toBe(exploreQuerySig('?q=abc'));
  });

  it('farklı sorgular farklı imza üretir', () => {
    expect(exploreQuerySig(A_QUERY)).not.toBe(exploreQuerySig(B_QUERY));
    expect(exploreQuerySig('?result=traces')).not.toBe(exploreQuerySig('?result=repeats'));
    // Sadece filtresi değişen bir traces saved view'i de AYRI sorgudur —
    // yalnız `q`'ya bakan bir imza bunu kaçırırdı.
    expect(exploreQuerySig('?result=traces&filters=a'))
      .not.toBe(exploreQuerySig('?result=traces&filters=b'));
  });

  it('anlamlı paramsız arama boş imza verir', () => {
    expect(exploreQuerySig('?range=30m')).toBe('');
    expect(exploreQuerySig('')).toBe('');
  });
});

describe('nextExploreKey — üç zorunlu davranış', () => {
  it('KENDİ YAZIMI remount ETMEZ (operatör builder’ı düzenliyor)', () => {
    const prev = at(A_QUERY);
    // ExploreInner navigate'ten önce yeni URL'ini bildirdi.
    const next = nextExploreKey(prev, B_QUERY, exploreQuerySig(B_QUERY));
    expect(next.key).toBe(prev.key);          // ← REMOUNT YOK
    expect(next.sig).toBe(exploreQuerySig(B_QUERY)); // imza yine de ilerledi
  });

  it('SAVED VIEW remount EDER (bildirim eski sorguda kalmış)', () => {
    const prev = at(A_QUERY);
    // Bildirim hâlâ A: URL'i değiştiren biz değiliz.
    const next = nextExploreKey(prev, B_QUERY, exploreQuerySig(A_QUERY));
    expect(next.key).not.toBe(prev.key);      // ← REMOUNT
    expect(next.key).toBe(`workspace:${exploreQuerySig(B_QUERY)}`);
  });

  it('RANGE DEĞİŞİMİ remount ETMEZ', () => {
    const prev = at('?q=abc&range=30m');
    const next = nextExploreKey(prev, '?q=abc&range=7d', exploreQuerySig('?q=abc&range=30m'));
    expect(next).toBe(prev);                  // aynı nesne — hiçbir şey değişmedi
  });

  it('range değişimi bildirim HİÇ yokken de remount etmez', () => {
    const prev = at('?q=abc&range=30m');
    expect(nextExploreKey(prev, '?q=abc&range=7d', null)).toBe(prev);
  });
});

describe('nextExploreKey — sınırlar ve idempotans', () => {
  it('paramsız → entry', () => {
    expect(nextExploreKey(null, '', null).key).toBe(EXPLORE_ENTRY_KEY);
    expect(nextExploreKey(null, '?range=30m', null).key).toBe(EXPLORE_ENTRY_KEY);
  });

  it('entry→entry aynı nesneyi döndürür (range gezinmesi)', () => {
    const prev = nextExploreKey(null, '?range=30m', null);
    expect(nextExploreKey(prev, '?range=7d', null)).toBe(prev);
  });

  it('entry→workspace HER ZAMAN remount (state URL’den tohumlanır)', () => {
    const prev = nextExploreKey(null, '?range=30m', null);
    // Bu geçişi tetikleyen yazım bizim olsa bile taze mount doğru davranış.
    const next = nextExploreKey(prev, A_QUERY, exploreQuerySig(A_QUERY));
    expect(next.key).toBe(`workspace:${exploreQuerySig(A_QUERY)}`);
    expect(next.key).not.toBe(prev.key);
  });

  it('workspace→entry remount', () => {
    const prev = at(A_QUERY);
    const next = nextExploreKey(prev, '?range=30m', null);
    expect(next.key).toBe(EXPLORE_ENTRY_KEY);
  });

  it('aynı search ile ikinci çağrı prev’i aynen döndürür (StrictMode güvenli)', () => {
    const first = nextExploreKey(null, A_QUERY, null);
    const second = nextExploreKey(first, A_QUERY, null);
    expect(second).toBe(first);
    const third = nextExploreKey(second, A_QUERY, exploreQuerySig(A_QUERY));
    expect(third).toBe(first);
  });

  it('kendi yazımından SONRA gelen saved view yine remount eder', () => {
    // A → (düzenleme) B → saved view C: B yankısı yutulur, C remount eder.
    let s = at(A_QUERY);
    const afterEdit = nextExploreKey(s, B_QUERY, exploreQuerySig(B_QUERY));
    expect(afterEdit.key).toBe(s.key);
    s = afterEdit;
    const savedView = '?q=CCC&range=30m';
    // Bildirim hâlâ B (en son biz B yazmıştık).
    const next = nextExploreKey(s, savedView, exploreQuerySig(B_QUERY));
    expect(next.key).toBe(`workspace:${exploreQuerySig(savedView)}`);
    expect(next.key).not.toBe(s.key);
  });

  it('ESKİ bir sorguya dönen saved view remount eder (bildirim tek değerli)', () => {
    // A → B → A' düzenlemeleri; sonra B'yi seçen bir saved view. Bildirimleri
    // KÜME olarak biriktirseydik B "bizim yazımız" sanılıp yutulurdu.
    let s = at(A_QUERY);
    s = nextExploreKey(s, B_QUERY, exploreQuerySig(B_QUERY));
    const aPrime = '?q=AAA2&range=30m';
    s = nextExploreKey(s, aPrime, exploreQuerySig(aPrime));
    const next = nextExploreKey(s, B_QUERY, exploreQuerySig(aPrime));
    expect(next.key).toBe(`workspace:${exploreQuerySig(B_QUERY)}`);
  });
});
