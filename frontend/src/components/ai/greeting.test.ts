import { describe, it, expect } from 'vitest';
import { greetHello, greetStatus, agoTR, newestP1 } from './greeting';

// v0.9.528 — karşılama saf olduğu için sözleşmesi burada pinli.
// Kritik nokta: karşılama bir İDDİA taşıyor ("3 açık P1 var"). Yanlış
// iddia, operatörün sohbeti kapatmasına yol açar — bu yüzden yükleme
// hâli ile "gerçekten sıfır" hâli ASLA aynı cümleye düşmemeli.

const NOW = 1_800_000_000_000; // sabit "şimdi" (ms)
const nsAgo = (mins: number) => (NOW - mins * 60_000) * 1e6;

describe('greetHello', () => {
  it('adı varsa isimle hitap eder', () => {
    expect(greetHello('Fatih')).toBe('Merhaba Fatih.');
  });
  it('ad yoksa isimsiz — uydurmaz', () => {
    expect(greetHello()).toBe('Merhaba 👋');
    expect(greetHello('')).toBe('Merhaba 👋');
    expect(greetHello('   ')).toBe('Merhaba 👋');
  });
});

describe('greetStatus', () => {
  it('YÜKLENİRKEN boş döner — "P1 yok" yanlış iddia olurdu', () => {
    expect(greetStatus(undefined, NOW)).toBe('');
  });

  it('gerçekten sıfırsa bunu açıkça söyler', () => {
    expect(greetStatus([], NOW)).toBe('Şu an açık P1 yok — sistem sakin görünüyor.');
  });

  it('tek P1 tekil dille anlatılır', () => {
    expect(greetStatus([{ service: 'payment-api', startedAt: nsAgo(12) }], NOW))
      .toBe('1 açık P1 var: payment-api, 12 dk önce.');
  });

  it('çoklu P1: sayı + EN YENİSİ', () => {
    const got = greetStatus([
      { service: 'checkout', startedAt: nsAgo(90) },
      { service: 'payment-api', startedAt: nsAgo(12) },
      { service: 'auth', startedAt: nsAgo(300) },
    ], NOW);
    expect(got).toBe('3 açık P1 var — en yenisi payment-api, 12 dk önce.');
  });

  it('en yeniyi bulmak için giriş SIRASINA güvenmez', () => {
    // Sunucu sıralaması değişirse (öncelik, sonra süre…) karşılama
    // sessizce yanlış servisi göstermemeli.
    const got = greetStatus([
      { service: 'eski', startedAt: nsAgo(500) },
      { service: 'yeni', startedAt: nsAgo(3) },
    ], NOW);
    expect(got).toContain('yeni');
    expect(got).not.toContain('eski');
  });

  it('servissiz uyarıda ad uydurmaz', () => {
    expect(greetStatus([{ startedAt: nsAgo(5) }], NOW)).toBe('1 açık P1 var — 5 dk önce.');
    expect(greetStatus([{ startedAt: nsAgo(5) }, { startedAt: nsAgo(50) }], NOW))
      .toBe('2 açık P1 var — en yenisi 5 dk önce.');
  });
});


describe('agoTR', () => {
  it('her birim ayrı ayrı doğru — birim karışması tekrar eden hata sınıfı', () => {
    expect(agoTR(nsAgo(0.2), NOW)).toBe('az önce');
    expect(agoTR(nsAgo(1), NOW)).toBe('1 dk önce');
    expect(agoTR(nsAgo(12), NOW)).toBe('12 dk önce');
    expect(agoTR(nsAgo(59), NOW)).toBe('59 dk önce');
    expect(agoTR(nsAgo(60), NOW)).toBe('1 sa önce');
    expect(agoTR(nsAgo(300), NOW)).toBe('5 sa önce');
    expect(agoTR(nsAgo(60 * 24), NOW)).toBe('1 gün önce');
    expect(agoTR(nsAgo(60 * 24 * 3), NOW)).toBe('3 gün önce');
  });

  it('gelecek zaman damgası negatife düşmez (saat kayması)', () => {
    expect(agoTR((NOW + 60_000) * 1e6, NOW)).toBe('az önce');
  });
});

describe('newestP1', () => {
  it('tek elemanlı listede onu döner', () => {
    const one = { service: 'a', startedAt: nsAgo(5) };
    expect(newestP1([one])).toBe(one);
  });
});
