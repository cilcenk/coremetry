import { describe, it, expect } from 'vitest';
import { USER_COLS, USERS_CONTENT_BUDGET, USERS_EMAIL_MIN } from './usersColumns';

// v0.9.660 — Users tablosu ekrandan taşıyordu (operatör-bildirimi).
//
// Bu testin işi tek bir sayıyı çivilemek: SABİT kolonların toplamı,
// hedef içerik genişliğinden email'in asgarisi kadar AZ olmalı. Bu
// aritmetik sayfanın içinde gömülüyken hiçbir kapı onu göremiyordu ve
// kolonlar zamanla (v0.8.403 seen, v0.8.450 lastLogin, custom role)
// sessizce bütçeyi aştı.

const fixed = USER_COLS.filter(c => !c.flex);
const flex = USER_COLS.filter(c => c.flex);
const fixedTotal = fixed.reduce((n, c) => n + (c.width ?? 0), 0);

describe('Users kolon bütçesi', () => {
  // ASIL KAPI. Taşma buradan geliyordu.
  it('sabit kolonlar email asgarisine yer bırakıyor', () => {
    expect(fixedTotal).toBeLessThanOrEqual(USERS_CONTENT_BUDGET - USERS_EMAIL_MIN);
  });

  // İki flex kolon = fixed layout artanı EŞİT böler, yani kısa bir
  // metin alanı (Team) email kadar pay alır. Primitifin belgesi de tek
  // emici öngörüyor.
  it('tam olarak BİR esneyen kolon var', () => {
    expect(flex.map(c => c.id)).toEqual(['email']);
  });

  // Fixed layout'ta auto kolon, diğerlerinin toplamı tabloyu
  // doldurduğunda 0'a çöker. Team bu yüzden ekrandan tamamen kaybolmuştu
  // — düzenlenebilir bir alan, sıfır genişlikte.
  it('Team sabit genişlikte — 0\'a çökemez', () => {
    const team = USER_COLS.find(c => c.id === 'team');
    expect(team?.flex).toBeUndefined();
    expect(team?.width).toBeGreaterThan(0);
  });

  // width'i olmayan kolon DEFAULT_W=120'ye düşer ve bütçe dışında
  // sessizce yer kaplar — bütçe hesabı da yalan söyler.
  it('her sabit kolonun bildirilmiş genişliği var', () => {
    const missing = fixed.filter(c => !c.width).map(c => c.id);
    expect(missing).toEqual([]);
  });

  // Sürükleme alt sınırı email'i okunmaz hâle getirmemeli.
  it('email sürüklenirken asgarinin altına inemiyor', () => {
    expect(USER_COLS.find(c => c.id === 'email')?.minWidth).toBe(USERS_EMAIL_MIN);
  });
});
