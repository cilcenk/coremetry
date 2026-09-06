import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';

// drawerChatAlwaysOpen.test.ts — v0.10.82, operatör isteği:
// "Chat'te devam et demesine gerek yok, kullanıcı isterse hemen
// yazabilsin oraya."
//
// Çekmecede sohbet bir `open` kapısının arkasındaydı: composer'ı görmek
// için önce "💬 Chat'te devam et" düğmesine basmak gerekiyordu. Kapı
// salt aşamalı-gösterimdi ve bir TIK VERGİSİYDİ — composer'ın mount'u
// bedava (useChatThread yalnız send'de istek atar), yani korunan hiçbir
// maliyet yoktu.
//
// ⚠ Satır-içi yüzeylerdeki "Chat'te devam et" köprüsü (CopilotExplain,
// !auto dalı) BİLEREK duruyor: o bir kapı değil, global pencereye
// navigasyon — çekmecesi olmayan yüzeylerin tek sohbet yolu.

// v0.10.483 — gövde AIDrawerBody.tsx'e taşındı (AIDrawer.tsx artık CopilotChat'e delege eder).
const drawer = () => readFileSync(new URL('./AIDrawerBody.tsx', import.meta.url), 'utf8');

describe('çekmece sohbeti hep açık', () => {
  it('open kapısı ve devam düğmesi KALKTI', () => {
    const src = drawer();
    // ⚠ Çıplak "Chat'te devam et" araması dosyanın YORUMLARINI ısırır
    // (v0.9.479 tarihçesi + v0.10.82 gerekçesi) — gate kendi metnini
    // ısırır sınıfı. JSX'e özgü biçim aranıyor: emoji+ok yalnız düğmedeydi.
    expect(src).not.toContain("💬 Chat'te devam et →");
    expect(src).not.toContain('const [open, setOpen]');
  });

  it('composer explain varken koşulsuz çiziliyor', () => {
    const src = drawer();
    // Bağlamsız sohbet muhafızı DURUYOR (bağlam kurulamadıysa sohbet yok
    // — v0.9.479'un operatör raporu); onun ötesinde kapı yok.
    expect(src).toContain('if (!explain) return null;');
  });

  it('satır-içi köprü (CopilotExplain !auto) korunuyor — o kapı değil navigasyon', () => {
    const inline = readFileSync(new URL('../CopilotExplain.tsx', import.meta.url), 'utf8');
    expect(inline).toContain("💬 Chat'te devam et →");
    expect(inline).toContain('{!auto && (');
  });
});
