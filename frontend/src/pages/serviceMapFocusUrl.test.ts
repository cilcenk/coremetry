import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { serviceFromRoute } from '@/lib/chatContext';

// serviceMapFocusUrl.test.ts — v0.10.76.
//
// /service-map odağı localStorage'dan HATIRLANIYOR ama ilk mount'ta
// URL'ye yazılmıyordu: commitFocus çağrılmıyor ve otomatik-odak efekti
// odağı canlı bulunca sessizce erken dönüyordu.
//
// ⚠ İki şeyi birden bozuyordu ve ikisi de sessiz:
//   • sohbet bağlamı rotadan servisi okuyor; göremeyince soru FİLO
//     GENELİNE gidiyor (operatör ekranda checkout'a bakarken cevap
//     tüm filo hakkında),
//   • "Copy link" ekrandaki görünümü üretmiyor — deponun "URL = tek
//     gerçek kaynak" değişmezinin ihlali (v0.8.256/265/267 sınıfı).

const page = () => readFileSync(new URL('./ServiceMap.tsx', import.meta.url), 'utf8');

describe('/service-map odağı URL ile senkron', () => {
  it('hatırlanan odak URL yokken commitFocus ile yazılıyor', () => {
    const src = page();
    // Erken dönüş artık KOŞULLU: URL'de varsa dön, yoksa YAZ.
    expect(src).toContain('if (fromUrl) return;');
    expect(src).toContain('commitFocus(focus);');
  });

  it('commitFocus URL\'yi gerçekten yazıyor', () => {
    const src = page();
    expect(src).toContain("p.set('focus', v)");
    expect(src).toContain('{ replace: true }');
  });

  // Zincirin öbür ucu: sohbet bağlamı `?focus=`i OKUYOR olmalı.
  // Biri düşerse senkron anlamsız kalır.
  it('sohbet bağlamı /service-map için focus paramını okuyor', () => {
    expect(serviceFromRoute('/service-map', '?focus=checkout-service'))
      .toBe('checkout-service');
    expect(serviceFromRoute('/service-map', '')).toBe('');
  });
});
