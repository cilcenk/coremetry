import { describe, it, expect } from 'vitest';
import { parseRootOnlyParam, rootOnlyUrlValue, shouldDropRootOnly } from './rootOnlyFallback';

// v0.9.1372 — operatörün iki parçalı isteği ("root seçili gelsin AMA boş
// dönerse root'u bırak, sessizce") bir durum makinesi; makine yanlış
// kurulduğunda hatası GÖRÜNMEZ olur: liste doludur, yalnız yanlış
// evrenden doludur. Bu yüzden karar noktaları saf ve tablo-testli.

describe('parseRootOnlyParam', () => {
  const cases: Array<[string, string | null, boolean, boolean]> = [
    ['yok → kapalı, kurulum yok',        null,     false, false],
    ['boş → kapalı',                     '',       false, false],
    ["'true' → açık ama geri dönüş YOK", 'true',   true,  false],
    ["'auto' → açık + kurulu",           'auto',   true,  true],
    ["'false' → kapalı",                 'false',  false, false],
    ['çöp → kapalı',                     'evet',   false, false],
  ];
  for (const [name, raw, rootOnly, auto] of cases) {
    it(name, () => expect(parseRootOnlyParam(raw)).toEqual({ rootOnly, auto }));
  }

  it("'true' geri dönüşü KURMAZ — paylaşılan linkin niyeti sessizce değişemez", () => {
    // Ayırt edici vaka: iki değer de kutuyu AÇIYOR, farkları yalnız
    // `auto`. Yüklem bunları ayırmazsa operatörün elle işaretlediği
    // root filtresi de boş sonuçta düşer ve o link bir daha aynı şeyi
    // göstermez.
    expect(parseRootOnlyParam('true').rootOnly).toBe(parseRootOnlyParam('auto').rootOnly);
    expect(parseRootOnlyParam('true').auto).not.toBe(parseRootOnlyParam('auto').auto);
  });
});

describe('rootOnlyUrlValue', () => {
  it('açık → true', () => expect(rootOnlyUrlValue(true)).toBe('true'));
  it('kapalı → düşer', () => expect(rootOnlyUrlValue(false)).toBe(''));
  it("'auto' geri yazılmaz — tur döngüsü olmaz", () => {
    // Girdi olarak `auto` diye bir DURUM yok: parse onu (true, armed)
    // ikilisine çeviriyor, geri yazım yalnız somut hâli tanıyor.
    expect([rootOnlyUrlValue(true), rootOnlyUrlValue(false)]).not.toContain('auto');
  });
});

describe('shouldDropRootOnly', () => {
  const base = { auto: true, rootOnly: true, loaded: true, errored: false, rowCount: 0 };
  it('kurulu + açık + yüklendi + hatasız + sıfır satır → düşer', () => {
    expect(shouldDropRootOnly(base)).toBe(true);
  });

  const negatives: Array<[string, Partial<typeof base>]> = [
    ['kurulu değil (operatörün kendi seçimi)', { auto: false }],
    ['kutu zaten kapalı (ikinci kez düşmez)',  { rootOnly: false }],
    ['henüz yüklenmedi',                        { loaded: false }],
    ['sonuç var',                               { rowCount: 12 }],
    ['tek satır bile yeter',                    { rowCount: 1 }],
  ];
  for (const [name, patch] of negatives) {
    it(`${name} → düşmez`, () => expect(shouldDropRootOnly({ ...base, ...patch })).toBe(false));
  }

  it('HATA sıfır satır değildir — ağ hatasında filtre bırakılmaz', () => {
    // Ayırt edici vaka. `errored` kapısı olmasa da bu senaryoda rowCount
    // 0'dır ve kod "boş sonuç" sanır: operatörün sormadığı daha GENİŞ bir
    // sorguya sessizce geçer ve hatayı da örter. İki yanlış tek ekranda.
    expect(shouldDropRootOnly({ ...base, errored: true })).toBe(false);
  });
});
