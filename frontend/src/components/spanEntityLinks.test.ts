import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { spanAttrHref, spanEndpointHref, isEntrySpanKind } from './spanEntityLinks';

// v0.10.34 — operatör isteği: "trace http route ile endpoint arasında
// service name gibi bağlantı olsun".
//
// Span detayında `Service` tıklanabilirdi, `http.route` düz metindi —
// oysa endpoint de tam anlamıyla bir VARLIK (kendi sayfası, RED'i,
// baseline'ı, trace listesi). Eksik olan veri değil, KENARDI.

const base = { on: true, service: 'svc-a', kind: 'server', httpRoute: '/api/v1/orders' };

describe('spanAttrHref — endpoint kenarı', () => {
  it('http.route endpoint sayfasına gider', () => {
    const href = spanAttrHref('http.route', '/api/v1/orders', base);
    expect(href).toContain('/endpoint?');
    expect(href).toContain('service=svc-a');
    expect(href).toContain(encodeURIComponent('/api/v1/orders'));
  });

  // ⚠ EN KRİTİK KARAR. Endpoint kimliği ŞABLONLANMIŞ yola göre kuruluyor.
  // Prod'da ham yol ile şablon gerçekten ayrışıyor:
  //   url.path   = /BSAWEB/rest/application/x
  //   http.route = /BSAWEB/application/x        ← endpoint bu
  // Ham yoldan link kurmak var olmayan bir endpoint'e götürür: sayfa
  // açılır, BOŞ gelir, operatör "veri yok" sanır. Yanlış link,
  // linksizlikten kötüdür.
  describe('ham yol satırları LİNK OLMAZ', () => {
    for (const k of ['url.path', 'http.target', 'url.full', 'http.url']) {
      it(k, () => expect(spanAttrHref(k, '/api/v1/orders/12345', base)).toBeNull());
    }
  });

  // Endpoint tablosu yalnız kind IN ('server','consumer') span'lerinden
  // kuruluyor. Client span'inden link kurmak, ÇAĞIRANIN servisi ile
  // ÇAĞRILANIN route'unu birleştirir — var olmayan bir çift.
  describe('yalnız GİRİŞ span\'i', () => {
    for (const [kind, want] of [
      ['server', true], ['consumer', true], ['SERVER', true],
      ['client', false], ['producer', false], ['internal', false], ['', false],
    ] as const) {
      it(`${kind || '(boş)'} → ${want ? 'link' : 'yok'}`, () => {
        const href = spanAttrHref('http.route', '/x', { ...base, kind });
        expect(href === null).toBe(!want);
      });
    }
  });

  it('servis bilinmiyorsa link YOK — kimliğin yarısı eksik', () => {
    expect(spanAttrHref('http.route', '/x', { ...base, service: '' })).toBeNull();
    expect(spanAttrHref('http.route', '/x', { ...base, service: '   ' })).toBeNull();
    expect(spanAttrHref('http.route', '/x', { ...base, service: undefined })).toBeNull();
  });

  // ⚠ SUNUCUNUN ÇÖZDÜĞÜ DEĞER KAZANIR. `spans.http_route` ingest'te
  // doldurulan promoted kolon (http.route → http.target fallback);
  // attribute yalnız görüntülenen metin. İkisi ayrışırsa doğru olan
  // sunucununki — aksi hâlde /endpoints ile trace sessizce ayrışır.
  it('sunucunun route\'u attribute değerini EZER', () => {
    const href = spanAttrHref('http.route', '/ham/yol', { ...base, httpRoute: '/sablon/{id}' });
    expect(href).toContain(encodeURIComponent('/sablon/{id}'));
    expect(href).not.toContain(encodeURIComponent('/ham/yol'));
  });

  it('sunucu route\'u yoksa attribute değerine düşer', () => {
    const href = spanAttrHref('http.route', '/attr/yol', { ...base, httpRoute: undefined });
    expect(href).toContain(encodeURIComponent('/attr/yol'));
  });

  it('public görüntüleyicide (on=false) hiç link YOK', () => {
    // Kimliksiz /public/trace, alıcılarının açamayacağı uygulama-içi
    // navigasyonu reklam etmemeli.
    expect(spanAttrHref('http.route', '/x', { ...base, on: false })).toBeNull();
    expect(spanAttrHref('Service', 'svc-a', { ...base, on: false })).toBeNull();
  });

  it('servis satırları çalışmaya devam ediyor', () => {
    for (const k of ['Service', 'service.name', 'peer.service']) {
      expect(spanAttrHref(k, 'svc-a', base)).toContain('/service');
    }
  });

  it('ilgisiz anahtar link olmaz', () => {
    expect(spanAttrHref('thread.name', 'main', base)).toBeNull();
    expect(spanAttrHref('http.route', '', base)).toBeNull();
  });
});

describe('spanEndpointHref — INFO satırı', () => {
  it('giriş span\'inde href + etiket verir', () => {
    const r = spanEndpointHref(base);
    expect(r).not.toBeNull();
    expect(r!.label).toBe('/api/v1/orders');
    expect(r!.href).toContain('/endpoint?');
  });

  // Attribute listesinden BAĞIMSIZ: span http.route attribute'unu
  // taşımasa bile sunucu route'u çözmüş olabilir (http.target fallback).
  it('attribute olmasa da sunucu route\'undan çıkar', () => {
    expect(spanEndpointHref({ on: true, service: 'a', kind: 'server', httpRoute: '/x' })).not.toBeNull();
  });

  it('route yoksa satır YOK', () => {
    expect(spanEndpointHref({ ...base, httpRoute: '' })).toBeNull();
    expect(spanEndpointHref({ ...base, httpRoute: undefined })).toBeNull();
  });

  it('client span\'inde satır YOK', () => {
    expect(spanEndpointHref({ ...base, kind: 'client' })).toBeNull();
  });
});

describe('isEntrySpanKind', () => {
  it.each([['server', true], ['consumer', true], ['Server', true],
    ['client', false], ['producer', false], ['internal', false],
    ['', false], [undefined, false]] as const)(
    '%s → %s', (k, want) => expect(isEntrySpanKind(k)).toBe(want));
});

// KABLOLAMA PİNİ — saf çekirdek yeşil ama çağrılmıyorsa kenar yok.
describe('SpanDetail kablolaması', () => {
  const src = readFileSync(new URL('./SpanDetail.tsx', import.meta.url), 'utf8');

  it('Row yeni çözücüyü kullanıyor', () => {
    expect(src).toContain('spanAttrHref(k, v, links)');
  });

  it('bağlam servis + kind + route taşıyor', () => {
    // Üçü de olmadan endpoint kimliği kurulamaz.
    for (const f of ['service: span.serviceName', 'kind: span.kind', 'httpRoute: span.httpRoute']) {
      expect(src).toContain(f);
    }
  });

  it('INFO bölümünde Endpoint satırı var', () => {
    expect(src).toContain('spanEndpointHref(spanLinkCtx)');
    expect(src).toContain('<td>Endpoint</td>');
  });
});
