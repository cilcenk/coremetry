// @vitest-environment jsdom
//
// KqlSearchInput.contract.test.tsx — v0.9.971.
//
// ORİJİNAL BELİRTİ: operatör `level:err` yazıp iki noktayı GERİ SİLİNCE
// alan-adı listesi açılmıyordu. Bir tuşa daha basınca açılıyordu, yani
// kusur kendini onarıyor ve tam da bu yüzden "bazen çıkmıyor" diye
// yaşanıyordu.
//
// KÖK NEDEN — iki effect AYNI commit'te `open`u ters yönde yazıyor.
// Değer-token'ı effect'i `!token` dalında koşulsuz `setOpen(false)`
// diyor; alan-adı effect'i `setOpen(true)`. React effect'leri BİLDİRİM
// SIRASINDA koşturuyor, ad effect'i önce geldiği için son söz kapatana
// kalıyor. Sonraki render'da ad effect'inin bağımlılıkları değişmediği
// için tekrar koşmuyor → liste kapalı kalıyor.
//
// KAYNAK TARAMASI BURADA YETMEZ: iki effect de tek başına doğru; hata
// yalnız GERÇEK sıralamada görünür. Bu yüzden jsdom render'ı — Modal /
// Drawer sözleşme testlerinin gerekçesiyle aynı.
import { describe, it, expect, afterEach, vi } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act } from 'react';
import { useState } from 'react';
import { KqlSearchInput } from './KqlSearchInput';

// Değer tamamlaması ağ turu ister; bu testin konusu DEĞİL. Boş dönmek
// gerçek CH arka ucunun davranışı (FieldValues stub'ı) ile de aynı.
vi.mock('@/lib/api', () => ({
  api: { logsFieldValues: vi.fn().mockResolvedValue({ values: [] }) },
}));

const FIELDS = ['level', 'level.name', 'service.name', 'attributes.CHANNEL_CODE'];

let root: Root | null = null;
let host: HTMLDivElement | null = null;

afterEach(() => {
  act(() => { root?.unmount(); });
  host?.remove();
  root = null; host = null;
});

/** Kontrollü sarmalayıcı — /logs'un kendi semantiği (value + onChange). */
function Harness({ fieldsTotal }: { fieldsTotal?: number }) {
  const [v, setV] = useState('');
  return (
    <KqlSearchInput value={v} onChange={setV} fields={FIELDS} fieldsTotal={fieldsTotal} />
  );
}

/** React'in kendi value setter'ını atlayarak gerçek input olayı üretir. */
function type(input: HTMLInputElement, text: string) {
  const setter = Object.getOwnPropertyDescriptor(
    window.HTMLInputElement.prototype, 'value')!.set!;
  setter.call(input, text);
  input.setSelectionRange(text.length, text.length);
  input.dispatchEvent(new Event('input', { bubbles: true }));
}

function mount(fieldsTotal?: number): HTMLInputElement {
  host = document.createElement('div');
  document.body.appendChild(host);
  root = createRoot(host);
  act(() => { root!.render(<Harness fieldsTotal={fieldsTotal} />); });
  return host.querySelector('input')!;
}

/** Açılır listedeki satır metinleri (başlık hariç). */
function rows(): string[] {
  const box = host!.querySelector('div[style*="position: absolute"]');
  if (!box) return [];
  return Array.from(box.children).slice(1).map(c => c.textContent ?? '');
}

describe('KqlSearchInput — alan adı listesi açılışı', () => {
  it('düz önekte açılır (taban davranış)', () => {
    const input = mount();
    act(() => { type(input, 'lev'); });
    expect(rows()).toContain('level');
  });

  it('İKİ NOKTA GERİ SİLİNİNCE de açılır — effect yarışı yok', () => {
    // v0.9.971 kusuru: `level:` → `level` geçişinde değer-token'ı
    // effect'i `setOpen(false)` diyor ve ad effect'ini eziyordu.
    const input = mount();
    act(() => { type(input, 'level:'); });
    act(() => { type(input, 'level'); });
    expect(rows()).toContain('level');
  });

  it('değer konumunda alan adı ÖNERİLMEZ — iki kaynak çakışmaz', () => {
    const input = mount();
    act(() => { type(input, 'level:er'); });
    expect(rows()).not.toContain('level');
  });
});
