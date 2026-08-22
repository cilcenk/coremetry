import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { decodeCsvSet, encodeCsvSet, readInboxTeam, INBOX_TEAM_PARAM } from './inboxUrl';

// v0.8.291 — /inbox facets move to the URL. Pin the codec: absent = default,
// invalid tokens dropped, order canonicalised to `allowed`, default selection
// serialises to null (param omitted so a default view's link stays clean).
const PRIO = ['P1', 'P2', 'P3'] as const;
const PRIO_DFLT = ['P1', 'P2'] as const;
const KIND = ['problem', 'exception', 'anomaly'] as const;

describe('decodeCsvSet', () => {
  it('null / empty falls back to default', () => {
    expect(decodeCsvSet(null, PRIO, PRIO_DFLT)).toEqual(['P1', 'P2']);
    expect(decodeCsvSet('', PRIO, PRIO_DFLT)).toEqual(['P1', 'P2']);
  });
  it('parses a valid subset', () => {
    expect(decodeCsvSet('P1', PRIO, PRIO_DFLT)).toEqual(['P1']);
    expect(decodeCsvSet('P2,P3', PRIO, PRIO_DFLT)).toEqual(['P2', 'P3']);
  });
  it('drops invalid tokens + dedupes + trims', () => {
    expect(decodeCsvSet(' P1 , nope , P1 , P3 ', PRIO, PRIO_DFLT)).toEqual(['P1', 'P3']);
  });
  it('all-invalid falls back to default (never empty)', () => {
    expect(decodeCsvSet('garbage', PRIO, PRIO_DFLT)).toEqual(['P1', 'P2']);
  });
  it('works for kind with an all-selected default', () => {
    expect(decodeCsvSet(null, KIND, KIND)).toEqual(['problem', 'exception', 'anomaly']);
    expect(decodeCsvSet('anomaly', KIND, KIND)).toEqual(['anomaly']);
  });
});

describe('encodeCsvSet', () => {
  it('returns null when selection equals default (param omitted)', () => {
    expect(encodeCsvSet(['P1', 'P2'], PRIO, PRIO_DFLT)).toBeNull();
    expect(encodeCsvSet(['P2', 'P1'], PRIO, PRIO_DFLT)).toBeNull(); // order-independent
    expect(encodeCsvSet(['anomaly', 'problem', 'exception'], KIND, KIND)).toBeNull();
  });
  it('serialises a non-default selection in allowed order', () => {
    expect(encodeCsvSet(['P3', 'P1'], PRIO, PRIO_DFLT)).toBe('P1,P3');
    expect(encodeCsvSet(['P1'], PRIO, PRIO_DFLT)).toBe('P1');
  });
  it('round-trips through decode', () => {
    const enc = encodeCsvSet(['P1', 'P3'], PRIO, PRIO_DFLT);
    expect(decodeCsvSet(enc, PRIO, PRIO_DFLT)).toEqual(['P1', 'P3']);
  });
});

// ─── ?team= (v0.9.1246) ────────────────────────────────────────────────
//
// Operatör: "Takımımın exceptionları dediğinde o takım filtreli exceptions
// açabilir." Sohbetin derin linki /inbox?kind=exception&team=<AD> yazıyor;
// bu codec o paramı sayfaya taşıyan tek okuma noktası.
//
// KISA KOD: gerçek takım adları "SY"/"UG" gibi 2 harfli olabilir, yani
// hiçbir katmanda uzunluk varsayımı olamaz. HARF KASASI bilerek
// DEĞİŞTİRİLMİYOR: sunucu katlıyor (NormTeamName) ve link kataloğun
// yazımını taşıyor — burada küçültmek çipi katalogla çeliştirirdi.
describe('readInboxTeam (v0.9.1246)', () => {
  const read = (qs: string) => readInboxTeam(new URLSearchParams(qs));

  it('paramı okur', () => {
    expect(read('team=payments')).toBe('payments');
    expect(read('kind=exception&team=payments')).toBe('payments');
  });

  it('2 harflik kısa kodlar tam yurttaş', () => {
    expect(read('team=SY')).toBe('SY');
    expect(read('team=UG')).toBe('UG');
    expect(read('team=sy')).toBe('sy'); // kasa korunur — sunucu katlıyor
  });

  it('yazımı olduğu gibi taşır (Türkçe + boşluk + tire)', () => {
    expect(read('team=' + encodeURIComponent('Ödeme Takımı'))).toBe('Ödeme Takımı');
    expect(read('team=' + encodeURIComponent('SY-Dijital'))).toBe('SY-Dijital');
  });

  it('yok / boş / yalnız boşluk = süzgeç yok', () => {
    expect(read('')).toBe('');
    expect(read('kind=exception')).toBe('');
    expect(read('team=')).toBe('');
    expect(read('team=%20%20')).toBe('');
  });

  it('kenar boşlukları kırpılır (elle düzenlenen URL)', () => {
    expect(read('team=%20SY%20')).toBe('SY');
  });

  it('komşu paramı okumaz (K4: owner/sre AYRI eksen)', () => {
    expect(read('owner=payments&sre=core')).toBe('');
    expect(INBOX_TEAM_PARAM).toBe('team');
  });
});

// KAYNAK PİNİ — codec'in sayfaya BAĞLI olduğu. Sözleşmenin diğer yarısı
// (link ile param adının eşleşmesi) Go tarafında: api/inbox_team_filter_test.go
// TestInboxTeamParamPairing. Buradaki pin, okumanın istekten ve GÖRÜNÜR
// çipten kopmasını yakalıyor: URL'de duran ama hiçbir şeyi daraltmayan bir
// param, ya da hiç görünmeden daraltan bir filtre — ikisi de sessiz.
describe('Inbox sayfası ?team= kablolaması', () => {
  const src = readFileSync(resolve(__dirname, '../pages/Inbox.tsx'), 'utf8')
    .replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '');

  it('paramı codec ile okuyup isteğe geçiriyor', () => {
    expect(src).toContain('readInboxTeam(searchParams)');
    expect(src).toContain('team: teamFilter || undefined');
  });

  it('süzgeç GÖRÜNÜR: çip + × temizleme', () => {
    expect(src).toContain('takım: {teamFilter}');
    expect(src).toContain("setTeamFilter('')");
    expect(src).toContain('setParam(INBOX_TEAM_PARAM,');
  });

  it('daraltma sayıldığı yerde de sayılıyor (scanCapped uyarısı)', () => {
    expect(src).toMatch(/const anyFilter = [^\n]*teamFilter/);
  });
});
