// v0.9.603 (traces D2) — iptal ile zaman aşımının AYRI şeyler olduğu.
//
// Öncesi: `request()` her AbortError'ı "Request timed out after 60s"
// diye yeniden fırlatıyordu. Yani operatör aralığı değiştirip eski
// isteği iptal ettiğinde ekrana ZAMAN AŞIMI hatası düşerdi —
// kullanıcının kendi eylemi hata gibi görünürdü.
//
// Ayrıca çağıran bir signal verdiği anda 60s emniyet tavanı
// KAYBOLUYORDU (`if (!signal)`), yani iptal edilebilirlik kazanmanın
// bedeli asılı kalma riskiydi. İkisi birbirinin alternatifi değil.
import { describe, it, expect } from 'vitest';
import { CanceledError, isCanceled } from './api';

describe('isCanceled', () => {
  it('CanceledError yakalanır', () => {
    expect(isCanceled(new CanceledError())).toBe(true);
  });

  it('zaman aşımı iptal SAYILMAZ', () => {
    // Bu ayrım davranışsal: iptal sessizce yutulur, zaman aşımı
    // operatöre GÖSTERİLİR ("daha dar aralık dene"). Karıştırılırsa ya
    // gerçek zaman aşımı gizlenir ya her aralık değişimi hata basar.
    expect(isCanceled(new Error('Request timed out after 60s'))).toBe(false);
  });

  it('sıradan hata iptal sayılmaz', () => {
    expect(isCanceled(new Error('HTTP 500: boom'))).toBe(false);
    expect(isCanceled(null)).toBe(false);
    expect(isCanceled(undefined)).toBe(false);
    expect(isCanceled('iptal')).toBe(false);
  });

  it('ad üzerinden de tanınır (sınıf kimliği kaybolsa bile)', () => {
    // Bundle sınırları arasında instanceof güvenilmez olabilir; ad
    // kontrolü ikinci bir yol.
    const e = new Error('x');
    e.name = 'CanceledError';
    expect(isCanceled(e)).toBe(true);
  });
});
