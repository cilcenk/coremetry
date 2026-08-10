import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { canAddCustomColumn, CUSTOM_COL_KEY_RE } from './customColumn';

// customColumn.test.ts — v0.9.870.
//
// Kaynak: tutarlılık denetimi (docs/audit/frontend-consistency-audit.md) mK3.
//
// SEMPTOM: ColumnManager'ın boş-sonuç mesajı "Press Enter to add it as a
// custom column." diyordu, input'ta onKeyDown YOKTU. Enter hiçbir şey
// yapmıyordu. Operatör aradığı attr'ı bulamayınca panelin kendi söylediğini
// yapıyor ve hiçbir şey olmuyordu — MT3'ün (sahte tık vaadi) klavye sürümü.
//
// KORUNAN ÖZELLİK: vaat ile tetikleyicinin AYNI koşula bağlı olması. İkisi
// ayrı ayrı yazılırsa mesajın göründüğü ama Enter'ın (ya da butonun)
// çalışmadığı bir aralık geri doğar; kırık vaadin kaynağı tam olarak buydu.

describe('canAddCustomColumn — mK3 kırık "Press Enter" vaadi', () => {
  const base = { query: 'tenant.id', keysLoaded: true, filteredCount: 0 };

  it.each([
    ['tipik semconv anahtarı',        { ...base },                                   true],
    ['baştaki/sondaki boşluk kırpılır', { ...base, query: '  tenant.id  ' },          true],
    ['tire ve alt tire',              { ...base, query: 'x-request_id' },             true],
    ['sadece rakam',                  { ...base, query: '42' },                       true],
    ['boş sorgu',                     { ...base, query: '' },                         false],
    ['yalnız boşluk',                 { ...base, query: '   ' },                      false],
    ['anahtarlar HENÜZ yüklenmedi',   { ...base, keysLoaded: false },                 false],
    ['eşleşen anahtar VAR',           { ...base, filteredCount: 3 },                  false],
    ['içinde boşluk',                 { ...base, query: 'tenant id' },                false],
    ['geçersiz karakter (:)',         { ...base, query: 'tenant:id' },                false],
    ['geçersiz karakter (/)',         { ...base, query: 'http/route' },               false],
    ['tek başına eğik çizgi',         { ...base, query: '*' },                        false],
  ])('%s', (_name, input, want) => {
    expect(canAddCustomColumn(input)).toBe(want);
  });

  it('keysLoaded=false ile filteredCount=0 birlikte YANILTICI', () => {
    // Yükleme sırasında filtrelenmiş liste de boştur. O anda "eşleşme yok,
    // özel kolon ekle" demek yalan olurdu — anahtarlar henüz gelmedi.
    expect(canAddCustomColumn({ query: 'db.system', keysLoaded: false, filteredCount: 0 })).toBe(false);
    expect(canAddCustomColumn({ query: 'db.system', keysLoaded: true, filteredCount: 0 })).toBe(true);
  });

  it('anahtar deseni OTel semconv adlarını kapsıyor', () => {
    for (const k of ['http.route', 'db.statement', 'messaging.destination.name', 'k8s.pod.name'])
      expect(CUSTOM_COL_KEY_RE.test(k), k).toBe(true);
  });
});

describe('ColumnManager — vaat ve tetikleyici tek koşula bağlı', () => {
  const src = readFileSync(resolve(__dirname, '../components/ColumnManager.tsx'), 'utf8');

  it('input Enter\'ı gerçekten dinliyor', () => {
    // mK3'ün ta kendisi: bu satır YOKTU.
    expect(src).toMatch(/onKeyDown=\{e =>/);
    expect(src).toMatch(/e\.key === 'Enter'/);
    expect(src).toContain('addCustom()');
  });

  it('koşul TEK yerde — buton ve mesaj aynı predicate\'i okuyor', () => {
    // Elle çoğaltılmış koşul (eski `/^[a-zA-Z0-9._-]+$/.test(query.trim())`
    // satır içi kopyası) geri gelmemeli: iki kopya ayrışır ve vaat yeniden
    // tetikleyiciden kopar.
    expect(src).not.toMatch(/\/\^\[a-zA-Z0-9\._-\]\+\$\/\.test/);
    expect(src).toContain('canAddCustomColumn({');
    // canAddCustom en az üç yerde okunuyor: mesaj, buton görünürlüğü, addCustom.
    expect(src.split('canAddCustom').length - 1).toBeGreaterThanOrEqual(4);
  });
});
