import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { facetSummary } from './FacetMultiSelect';

// v0.9.357 — the button label is the whole point of mockup C: the bar must be
// readable WITHOUT opening the panel. If this lies, the operator has to open
// every dropdown to know what they're looking at.
describe('facetSummary', () => {
  it('full selection reads tümü', () => {
    expect(facetSummary(['P1', 'P2', 'P3'], 3)).toBe('tümü');
  });
  it('short selections list the labels and count the closed', () => {
    expect(facetSummary(['Exceptions'], 4)).toBe('Exceptions +3 kapalı');
    expect(facetSummary(['P1', 'P2'], 3)).toBe('P1 + P2 +1 kapalı');
  });
  it('long selections collapse to a count', () => {
    expect(facetSummary(['a', 'b', 'c'], 4)).toBe('3 seçili +1 kapalı');
  });
  it('over-selection never goes negative', () => {
    // Defensive: a stale selected set larger than options must not render
    // "+-1 kapalı".
    expect(facetSummary(['a', 'b'], 2)).toBe('tümü');
  });
});

// ── mK9 (v0.9.922) — `role="option"` sözünü TUTMALI ────────────────────
// Bir ARIA rolü bir sözdür: `role="option"` "buraya odaklanabilirsin,
// Enter/Space seçer" demektir. Satır bu sözü tutmadan rolü taşıyorsa
// ekran okuyucu kullanıcısına var olmayan bir yetenek duyurulur —
// yanlış duyurulan bir rol, rolsüz bir div'den KÖTÜDÜR (v0.9.900'ün
// dangling `aria-controls` dersinin aynısı).
//
// Ayrıca `.fsel-solo` `visibility: hidden` ile başlıyor; o değer öğeyi
// odak sırasından da ÇIKARIR, yani "sadece" kısayoluna Tab'la asla
// ulaşılamazdı. `:focus-within` tek erişim yolu.
describe('mK9 — facet satırı klavyeyle kullanılabilir', () => {
  const src = readFileSync(resolve(__dirname, 'FacetMultiSelect.tsx'), 'utf8');
  const css = readFileSync(resolve(__dirname, '..', '..', 'styles', 'globals.css'), 'utf8');

  it('role="option" satırı odaklanabilir', () => {
    const i = src.indexOf('role="option"');
    expect(i).toBeGreaterThan(-1);
    expect(src.slice(i, i + 400)).toContain('tabIndex');
  });

  it('Enter/Space seçimi tetikliyor', () => {
    const i = src.indexOf('role="option"');
    const body = src.slice(i, i + 900);
    expect(body).toContain('onKeyDown');
    expect(body).toContain("'Enter'");
  });

  it('gizli "sadece" butonu odakla görünür oluyor', () => {
    const FW = ':focus' + '-within';
    expect(css.includes(`.fsel-row${FW} .fsel-solo`)).toBe(true);
  });
});
