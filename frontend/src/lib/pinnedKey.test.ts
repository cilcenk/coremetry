import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

// v0.9.1046 (Faz 0.8) — pinlenen servisler TEK localStorage anahtarında
// yaşar: 'coremetry.pinnedServices' (recentServices.ts). /services sayfası
// kendi 'coremetry-pinned-services' anahtarını yazıyordu; aynı yıldız iki
// depoya gidiyor, ⌘K ile tablo birbirini görmüyordu. Bu pin ikinci bir
// yazıcının yeniden doğmasını engeller — eski anahtar yalnız TAŞIMA için
// okunabilir, asla yazılamaz.

const SRC = resolve(__dirname, '..');

describe('pinned-services tek anahtar (v0.9.1046)', () => {
  it("kanonik anahtar 'coremetry.pinnedServices'", () => {
    const src = readFileSync(resolve(SRC, 'lib/recentServices.ts'), 'utf8');
    expect(src).toContain("const PINNED_KEY = 'coremetry.pinnedServices'");
  });

  it('Services.tsx eski anahtara YAZMIYOR (yalnız taşıma okuması + silme)', () => {
    const src = readFileSync(resolve(SRC, 'pages/Services.tsx'), 'utf8');
    expect(src).not.toMatch(/setItem\(\s*['"`]coremetry-pinned-services/);
    expect(src).not.toMatch(/setItem\(\s*PINNED_KEY/);
    // Pin/unpin tek kaynaktan geçmeli.
    expect(src).toContain('toggleServicePin(');
  });
});
