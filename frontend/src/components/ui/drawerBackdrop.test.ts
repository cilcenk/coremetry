import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

// v0.9.654 — operatör: "CoSRE drawer gibi çıksa … Chat'ten devam et
// özelliği drawerdı."
//
// Sohbet kendi sağ-alt yüzen panelini taşıyordu; Explain (AIDrawer)
// paylaşılan Drawer primitifini kullanıyordu. İki AI yüzeyi iki ayrı
// kabuk gibi duruyordu.
//
// Taşımanın BEDELİ vardı ve o yüzden Drawer'a overlay'siz bir kip
// eklendi: operatör sohbet açıkken tabloyu kaydırıyor, başka bir trace
// açıyor, sonra sorusunu yazıyor. Overlay bunu imkânsız kılardı.

const drawer = readFileSync(resolve(__dirname, './Drawer.tsx'), 'utf8');
const chat = readFileSync(resolve(__dirname, '../CopilotChat.tsx'), 'utf8');

describe('Drawer overlay kipi', () => {
  it('backdrop VARSAYILAN olarak açık — inceleme çekmeceleri modal kalıyor', () => {
    expect(drawer).toMatch(/backdrop\s*=\s*true/);
  });

  it('overlay koşullu çiziliyor', () => {
    expect(drawer).toContain('{backdrop && (');
  });

  // Overlay yoksa "dışarı tıkla kapat" da yok: sayfayla çalışmak
  // sohbeti kapatmamalı. Esc/✕ her iki kipte de çalışıyor.
  it('kapatma yolları duruyor', () => {
    // v0.9.950 (E2/Ö28) — Esc artık KATMAN yığınından geliyor. Eski pin
    // elle yazılmış document dinleyicisini arıyordu; o dinleyici Ö28'in
    // kök nedeniydi (bir ⌘K'yı kapatan Esc çekmeceyi de götürüyordu).
    // Sözleşme AYNI: Esc çekmeceyi kapatır — yalnız çekmece EN ÜSTTEYKEN.
    expect(drawer).toContain('useEscLayer(true, onClose)');
    expect(drawer).toContain('aria-label="Close"');
  });
});

describe('CoSRE sohbeti', () => {
  it('paylaşılan Drawer primitifini kullanıyor', () => {
    expect(chat).toContain("from '@/components/ui/Drawer'");
    expect(chat).toContain('<Drawer');
  });

  // Sohbet bir özneyi İNCELEMİYOR, ona EŞLİK ediyor.
  it('overlay KAPALI — sayfa kullanılabilir kalıyor', () => {
    expect(chat).toContain('backdrop={false}');
  });

  it('kendi yüzen panel kabuğu kalmadı', () => {
    // Eski kabuğun imzası: sağ-alt sabit konum + kendi gölgesi.
    expect(chat).not.toContain("boxShadow: '0 8px 40px rgba(0,0,0,0.5)'");
  });
});

// Explain'in davranışı DEĞİŞMEMELİ: o bir özneyi inceliyor, modal doğru.
describe('Explain çekmecesi etkilenmedi', () => {
  const ai = readFileSync(resolve(__dirname, '../ai/AIDrawer.tsx'), 'utf8');
  it('AIDrawer backdrop kapatmıyor', () => {
    expect(ai).not.toContain('backdrop={false}');
  });
});
