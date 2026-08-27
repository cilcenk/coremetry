// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach } from 'vitest';
import { readAiCodeParam, writeAiCodeParam } from '@/lib/aiSubject';

// aiCodeParam.test.tsx — v0.10.81, operatör-bildirimli:
// "Kodu da incele dediğimde URL'de parametre gelmediği için paylaştıktan
// sonra tekrar basmak gerekiyor."
//
// Paylaşılan Explain linki kodsuz açılıyordu; alıcı kutuya yeniden
// basıyor = İKİNCİ bir yerel LLM turu. URL-tek-gerçek-kaynak ailesinin
// (v0.8.256/265/267, v0.10.76) çekmece üyesi.
//
// ⚠ v0.10.60 ("her açılışta kapalı") KORUNUYOR: param uygulama içi her
// setSubject'te silinir; yalnız paylaşılan/yapıştırılan linkte yaşar.

const setUrl = (search: string) =>
  window.history.replaceState({}, '', '/trace' + search);

afterEach(() => setUrl(''));

describe('aicode URL paramı', () => {
  it('yaz/oku gidiş-dönüşü ve yabancı param koruması', () => {
    setUrl('?ai=trace:abc&span=s1');
    writeAiCodeParam(true);
    expect(window.location.search).toContain('aicode=1');
    expect(window.location.search).toContain('ai=trace%3Aabc');
    expect(window.location.search).toContain('span=s1'); // yabancı korunur
    expect(readAiCodeParam()).toBe(true);
    writeAiCodeParam(false);
    expect(window.location.search).not.toContain('aicode');
    expect(window.location.search).toContain('span=s1');
  });

  it('param yokken kapalı — v0.10.60 varsayılanı', () => {
    setUrl('?ai=trace:abc');
    expect(readAiCodeParam()).toBe(false);
  });

});
