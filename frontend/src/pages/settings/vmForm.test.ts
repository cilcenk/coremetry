import { describe, expect, it } from 'vitest';
import { vmFloorToForm, vmFloorToWire } from './vmForm';

// v0.9.1164 — "Rate penceresi tabanı" kutusunun çevirisi.
//
// Bu testin var olma sebebi tek bir dal: boş kutunun tele NE gönderdiği.
// Yanlış tarafa düşerse hiçbir şey patlamaz — kutu doğru görünür, kayıt
// başarılı olur, ve ayar sessizce ya donar ya sıfırlanır. aiTuning'in
// (v0.9.1120) tam olarak bu kusuru vardı, o yüzden aynı tabloyu bu alan
// için de yürütüyoruz.

describe('vmFloorToForm — sunucu → kutu', () => {
  it.each([
    // ÜÇ "unset" yazılışı, üçü de boş kutu olmalı. `undefined` en olası
    // olanı: backend alanı `omitempty` ile yazıyor, yani varsayılan
    // kurulumda JSON'da hiç GÖRÜNMEZ.
    ['alan hiç gelmedi (omitempty)', undefined, ''],
    ['null', null, ''],
    ['sıfır sentinel', 0, ''],
    // Gerçek değerler dizeye döner.
    ['alt sınır', 10, '10'],
    ['tipik değer', 30, '30'],
    ['varsayılanın kendisi açıkça kaydedilmiş', 300, '300'],
    ['üst sınır', 3600, '3600'],
  ])('%s → %o', (_name, input, want) => {
    expect(vmFloorToForm(input as number | null | undefined)).toBe(want);
  });

  it('0 GEÇERLİ BİR PENCERE DEĞİL, o yüzden falsy kontrolü doğru kontrol', () => {
    // aiTuning'de temperature için bu ters olurdu (0.0 geçerli bir sıcaklık).
    // Burada sıfır saniyelik taban diye bir şey yok — VM `[0s]` reddeder — bu
    // yüzden 0'ı "unset" saymak bir bilgi kaybı değil, sentinel'in tanımı.
    expect(vmFloorToForm(0)).toBe('');
  });
});

describe('vmFloorToWire — kutu → PUT', () => {
  it.each([
    // Boş ve ayrıştırılamaz girdi aynı yere gider: sıfırlama işareti.
    ['boş', '', 0],
    ['yalnız boşluk', '   ', 0],
    ['harf', 'abc', 0],
    ['yarım sayı', '1e', 0],
    // Normal yol.
    ['tam sayı', '60', 60],
    ['baş/son boşluk kırpılır', '  60  ', 60],
    ['ondalık yuvarlanır', '59.6', 60],
    ['aşağı yuvarlanır', '59.2', 59],
    // ARALIK DIŞI DEĞER OLDUĞU GİBİ GİDER. Sunucu 400 + sınırları döner;
    // burada kırpmak, operatörün sormadığı bir pencereyi sessizce
    // kaydetmek olurdu.
    ['sınırın altı olduğu gibi gider', '5', 5],
    ['sınırın üstü olduğu gibi gider', '99999', 99999],
    ['negatif olduğu gibi gider', '-60', -60],
  ])('%s: %o → %o', (_name, input, want) => {
    expect(vmFloorToWire(input as string)).toBe(want);
  });

  it('NaN/Infinity tele hiç çıkmaz', () => {
    // Bunlar JSON'da `null` olur ve Go tarafında 0'a düşer — yani sonuç
    // aynı olurdu, ama tel gövdesi artık tipini yalan söylüyor olurdu
    // (`rateWindowFloorS: number` değil).
    for (const v of ['NaN', 'Infinity', '-Infinity', '1/0']) {
      const out = vmFloorToWire(v);
      expect(Number.isFinite(out)).toBe(true);
      expect(out).toBe(0);
    }
  });
});

describe('gidiş-dönüş', () => {
  it('sunucudan gelen değer, dokunulmadan kaydedilince AYNI kalır', () => {
    // Bu döngü kırılırsa alakasız bir sebeple (token döndürmek, VM kapatmak)
    // basılan her Kaydet ayarı kaydırır.
    for (const stored of [10, 30, 60, 300, 900, 3600]) {
      expect(vmFloorToWire(vmFloorToForm(stored))).toBe(stored);
    }
  });

  it('unset kalır: boş kutu tekrar boş kutuya döner', () => {
    for (const stored of [undefined, null, 0] as (number | null | undefined)[]) {
      const box = vmFloorToForm(stored);
      expect(box).toBe('');
      // Tel 0 taşır, backend `omitempty` ile onu yeniden ATLAR, dolayısıyla
      // bir sonraki GET yine boş kutu verir. Sabit nokta.
      expect(vmFloorToWire(box)).toBe(0);
      expect(vmFloorToForm(vmFloorToWire(box))).toBe('');
    }
  });
});
