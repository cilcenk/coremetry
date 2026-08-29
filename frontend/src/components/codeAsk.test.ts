import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { shouldAskForCode, type CodeAskInput } from './codeAsk';

// v0.10.153 — soru YALNIZ: kod-yetenekli tür, URL kodlu değil, ilk cevap
// bitmiş, hata yok, karar verilmemiş. Her koşulun tek başına kapıyı kapattığı
// tabloyla pinlenir; kaynak taraması Chip'in KALDIĞINI (operatör), soru
// satırının ve ikinci akışın ilk cevabı ezmediğini doğrular.
const base: CodeAskInput = { codeCapable: true, includeCode: false, hasText: true, busy: false, hasError: false, state: 'idle' };

describe('shouldAskForCode (v0.10.153)', () => {
  it('asks in the base case', () => { expect(shouldAskForCode(base)).toBe(true); });
  it.each<[string, Partial<CodeAskInput>]>([
    ['non code-capable kind', { codeCapable: false }],
    ['ai=code in URL (already with code)', { includeCode: true }],
    ['no answer yet', { hasText: false }],
    ['first stream still running', { busy: true }],
    ['error', { hasError: true }],
    ['accepted', { state: 'accepted' }],
    ['declined', { state: 'declined' }],
  ])('does not ask: %s', (_, patch) => {
    expect(shouldAskForCode({ ...base, ...patch })).toBe(false);
  });
});

describe('CopilotExplain ask-for-code flow (source scan)', () => {
  const src = readFileSync(join(__dirname, 'CopilotExplain.tsx'), 'utf8').replace(/\{\/\*[\s\S]*?\*\/\}/g, '').replace(/^\s*\/\/.*$/gm, '');
  it('the chip stays (operator) and the question row exists', () => {
    expect(src).toMatch(/Kodu da incele</);
    expect(src).toMatch(/shouldAskForCode\(/);
    expect(src).toMatch(/Kodu da inceleyeyim mi\?/);
    expect(src).toMatch(/Hayır, yeterli/);
  });
  it('the code pass streams into codeText, never into the first answer', () => {
    expect(src).toMatch(/onDelta: \(d: string\) => setCodeText\(prev => \(prev \?\? ''\) \+ d\)/);
    expect(src).toMatch(/Kod incelemesi/);
    // "Yeniden sor" hâlâ URL parametresine göre koşar (explainCacheFE pini).
    expect(src).toContain('void run(includeCode, true)');
  });
});
