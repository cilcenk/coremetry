// v0.10.598 — özet Problem'de kanıt paneli: istek atılmaz, erken döner.
// Yorumlar süzülür; iddialar koda çapalı.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';

const SRC = readFileSync(new URL('./ExternalEvidencePanel.tsx', import.meta.url), 'utf8');
const CODE = SRC.replace(/\/\/.*$/gm, '').replace(/\/\*[\s\S]*?\*\//g, '');

describe('ExternalEvidencePanel — özet Problem', () => {
  it('/rootcause isteği özet türünde kapalı', () => {
    expect(CODE).toMatch(/enabled:\s*!summary/);
  });
  it('özet dalı, ⏳ "henüz toplanmadı" dalından ÖNCE döner', () => {
    const s = CODE.indexOf('if (summary) {');
    const wait = CODE.indexOf('Kanıt henüz toplanmadı');
    expect(s).toBeGreaterThan(-1);
    expect(wait).toBeGreaterThan(s);
  });
});
