import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { PANEL_UNITS, isKnownUnit, normalizeUnit } from './units';

// v0.10.506 (D9) — tek birim sözlüğü: takma adlar kanonik, bilinmeyen korunur.
describe('normalizeUnit', () => {
  it('takma adlar kanonik yazıma', () => {
    for (const [raw, want] of [
      ['msec', 'ms'], ['Milliseconds', 'ms'], [' ms ', 'ms'],
      ['sec', 's'], ['seconds', 's'],
      ['percent', '%'], ['pct', '%'], ['%', '%'],
      ['B', 'bytes'], ['byte', 'bytes'],
      ['req/s', 'rps'], ['RPS', 'rps'],
    ] as const) expect(normalizeUnit(raw), raw).toBe(want);
  });
  it('boş/undefined → ""; bilinmeyen özel ek olduğu gibi (kayıtlı dashboard uyumu)', () => {
    expect(normalizeUnit(undefined)).toBe('');
    expect(normalizeUnit('  ')).toBe('');
    expect(normalizeUnit('req/dk')).toBe('req/dk');
  });
  it('seçici listesi kanonik değerlerden oluşur', () => {
    for (const p of PANEL_UNITS) expect(normalizeUnit(p.value)).toBe(p.value);
    expect(isKnownUnit('ms')).toBe(true);
    expect(isKnownUnit('req/dk')).toBe(false);
  });
});

// Kaynak pinleri: editördeki birim alanları UnitSelect; karo ve eksen normalizeUnit.
describe('dashboard birim alanı (v0.10.506)', () => {
  const editor = readFileSync(resolve(__dirname, '../components/dashboard/PanelEditor.tsx'), 'utf8');
  const renderer = readFileSync(resolve(__dirname, '../components/dashboard/PanelRenderer.tsx'), 'utf8');
  it('serbest metin birim kutusu kalmadı; UnitSelect en az 4 yerde', () => {
    expect(editor).not.toMatch(/placeholder="(ms \/ % \/ rps|% \/ ms \/ rps)"/);
    expect((editor.match(/<UnitSelect\b/g) ?? []).length).toBeGreaterThanOrEqual(4);
  });
  it('karo ve eksen aynı sözlükten okur', () => {
    expect(renderer).toContain('fmtSmart(value, normalizeUnit(unit))');
    expect(renderer).toContain('unit = normalizeUnit(unit);');
  });
});
