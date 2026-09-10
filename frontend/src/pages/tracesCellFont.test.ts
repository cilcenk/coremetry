import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';

// v0.10.658 — operatör (prod ekran görüntüsü): "Name kolonu neden daha büyük
// font? Diğer kolonlarla aynı olsa iyi olur." Operasyon hücresi sınıfsız
// <span> idi: tablo 12 px ama ORANTILI yazı tipi; komşu hücreler .mono
// (monospace 12 px). Aynı boyut + aynı aile: operasyon hücresi de .mono,
// taşan kısım üç nokta (cell-ellipsis), tam değer title'da.

describe('Traces NAME hücresi diğer hücrelerle aynı yazı tipi', () => {
  const src = readFileSync(new URL('./Traces.tsx', import.meta.url), 'utf8');
  it("case 'operation' hücresi .mono cell-ellipsis taşır", () => {
    // İlk eşleşme sortValue erişimcisi (`case 'operation': return r => …`); hücre olan <span>'lı.
    const i = src.indexOf("case 'operation': return <span");
    expect(i).toBeGreaterThan(-1);
    expect(src.slice(i, i + 200)).toContain('className="mono cell-ellipsis"');
  });
});
