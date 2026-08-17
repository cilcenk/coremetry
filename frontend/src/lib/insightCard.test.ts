import { describe, it, expect } from 'vitest';
import { insightHasEvidence, insightHrefInternal, insightTone } from './insightCard';
import type { InsightResponse } from './types';

// v0.9.1130 (AI Faz 2.2) — kart yardımcılarının saf sözleşmesi.
//
// Üçü de SESSİZCE yanlış olabilen kararlar: yanlış renk (operatörü olmayan
// bir olaya koşturur), yanlış navigasyon (router'a mutlak URL), boş panel.
// Tablo testi bu yüzden şiddet kümesinin TAMAMINI + kümenin DIŞINI
// geziyor — v0.6.36'nın "değer+birim üreten şablon her birimi testler"
// kuralının eşlemeler için karşılığı.

describe('insightTone', () => {
  const CASES: Array<[string | undefined, string | null]> = [
    ['ok',      'b-ok'],
    ['warn',    'b-warn'],
    ['err',     'b-err'],
    // '' contract.go'da GEÇERLİ bir değer: "nötr bilgi" (servis adı,
    // operasyon sayısı). Rozet YOK demek, bilinmeyen demek değil.
    ['',        null],
    [undefined, null],
    // Boşluk/büyük harf tel üstünden gelebilir; renk bir yazım hatasına
    // takılıp kaybolmamalı.
    [' WARN ',  'b-warn'],
    ['Err',     'b-err'],
    // KÜMENİN DIŞI → nötr. Yarın sunucu 'fatal' eklerse kart onu err
    // sanıp yanlış renkte basmaz; renksiz basar ve değer okunur kalır.
    ['fatal',   null],
    ['info',    null],
    ['critical', null],
  ];

  for (const [input, want] of CASES) {
    it(`${JSON.stringify(input)} → ${want ?? 'nötr'}`, () => {
      expect(insightTone(input)).toBe(want);
    });
  }
});

describe('insightHrefInternal', () => {
  // Sunucunun BUGÜN ürettiği beş şekil (links.go) — hepsi router'a gider.
  const INTERNAL = [
    '/problems?exc=abc&range=custom:1-2',
    '/trace?id=deadbeef',
    '/traces?hasError=true&rootOnly=false&service=payment',
    '/logs?service=payment&severity=17',
    '/service?name=payment',
    '/ai',
  ];
  for (const href of INTERNAL) {
    it(`iç: ${href}`, () => expect(insightHrefInternal(href)).toBe(true));
  }

  // `//host` tek `/` testini GEÇER ama protokol-göreli MUTLAK bir URL'dir:
  // router'a verilirse uygulama sessizce başka bir siteye gider.
  const EXTERNAL = [
    '//evil.example/x',
    'https://grafana.example/d/abc',
    'http://x.example',
    'mailto:ops@example.com',
    'javascript:alert(1)',
    '',
  ];
  for (const href of EXTERNAL) {
    it(`dış: ${JSON.stringify(href)}`, () => expect(insightHrefInternal(href)).toBe(false));
  }
});

describe('insightHasEvidence', () => {
  const base = (over: Partial<InsightResponse>): InsightResponse =>
    ({ prose: '', signals: [], links: [], ...over });

  it('null cevap → kanıt yok', () => {
    expect(insightHasEvidence(null)).toBe(false);
  });

  it('tamamen boş cevap → kanıt yok (Empty çizilir)', () => {
    expect(insightHasEvidence(base({}))).toBe(false);
  });

  it('yalnız boşluk prose → kanıt yok', () => {
    expect(insightHasEvidence(base({ prose: '   \n' }))).toBe(false);
  });

  it('tek sinyal yeter', () => {
    expect(insightHasEvidence(base({
      signals: [{ kind: 'generic', label: 'Servis', value: 'payment' }],
    }))).toBe(true);
  });

  it('tek pivot yeter', () => {
    expect(insightHasEvidence(base({
      links: [{ label: 'Servis', href: '/service?name=payment' }],
    }))).toBe(true);
  });

  // Sinyalsiz ama anlatısı olan cevap hâlâ okunacak bir karttır: sunucu
  // projeksiyonu hepsini elemiş olabilir, model yine bir şey demiştir.
  it('sinyalsiz anlatı da kanıttır', () => {
    expect(insightHasEvidence(base({ prose: 'Havuz doygunluğu.' }))).toBe(true);
  });
});
