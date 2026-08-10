// `/` kısayolunun hedefi — UX denetimi E5 / Ö31 (v0.9.951).
//
// ORİJİNAL BELİRTİ: /traces'te `/` basınca imleç servis picker'ına değil
// "Trace ID…" kutusuna düşüyordu. Operatör servis aramak isterken
// trace-id kutusunda buluyordu kendini — belgelenmiş davranışın
// regresyonu.
//
// KÖK NEDEN ÖZELLİKLE SİNSİ: mekanizma v0.5.454'te TAM BU VAKA için
// yazılmıştı ve GlobalShortcuts'ın yorumu bunu kelimesi kelimesine
// söylüyor ("the Service picker on /traces, not the trace-id lookup
// which is the first DOM input"). Ama `data-shortcut-search` işareti
// /traces'e HİÇ konmamıştı: kod doğru, kablo yok. Yorum bir sözleşme
// gibi okunuyordu, oysa yalnız bir NİYETTİ.

import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

const SRC = join(__dirname, '..');
const read = (rel: string) => readFileSync(join(SRC, rel), 'utf8');

describe('GlobalShortcuts — çözümleme sırası', () => {
  const gs = read('components/GlobalShortcuts.tsx');

  it('AÇIK işaret fallback’ten ÖNCE denenir', () => {
    const opted = gs.indexOf("querySelectorAll<HTMLElement>('[data-shortcut-search]')");
    const fallback = gs.indexOf('input[type="text"]');
    expect(opted).toBeGreaterThan(-1);
    expect(fallback).toBeGreaterThan(-1);
    expect(opted, 'işaret fallback’ten sonra aranırsa opt-in hiçbir işe yaramaz').toBeLessThan(fallback);
  });

  it('fallback checkbox/radio’ya DÜŞMEZ', () => {
    // `input:not([type])` + text/search — bir onay kutusu odaklanırsa
    // `/` görünürde hiçbir şey yapmamış gibi olurdu.
    expect(gs).toMatch(/input\[type="text"\], input\[type="search"\], input:not\(\[type\]\), textarea/);
  });
});

describe('/traces — işaret KONULDU (Ö31 kapandı)', () => {
  const traces = read('pages/Traces.tsx');

  it('servis picker `/` hedefi olarak işaretli', () => {
    expect(traces).toMatch(/<ServicePicker[\s\S]{0,400}?shortcutSearch\s*\/>/);
  });

  it('Trace ID kutusu DOM’da hâlâ ÖNCE — yani işaret gerçekten gerekli', () => {
    // Bu satır düzeltmenin gerekçesini canlı tutuyor: biri kutuları
    // yeniden sıralarsa ve işaret düşerse, test artık koruma sağlamaz
    // sanılmasın diye premise burada ölçülüyor.
    const traceIdAt = traces.indexOf('placeholder="Trace ID…"');
    const pickerAt = traces.indexOf('<ServicePicker');
    expect(traceIdAt).toBeGreaterThan(-1);
    expect(pickerAt).toBeGreaterThan(traceIdAt);
  });
});

describe('ServicePicker — opt-in kablosu', () => {
  const sp = read('components/ServicePicker.tsx');

  it('bayrak input’a data-shortcut-search basar', () => {
    expect(sp).toMatch(/shortcutSearch \? \{ 'data-shortcut-search': '' \}/);
  });

  it('bayraksız picker işaret TAŞIMAZ — sayfa başına tek hedef', () => {
    // Koşulsuz basmak, bir sayfadaki HER picker'ı hedef yapardı ve
    // querySelectorAll ilkini seçerdi: yine DOM sırası kumarı.
    expect(sp).not.toMatch(/data-shortcut-search=""/);
    expect(sp).toMatch(/\.\.\.\(shortcutSearch \?/);
  });
});
