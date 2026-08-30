// chatProfile.test.ts — v0.10.183 kablolama kapısı (çoklu model dilim C):
// seçim UI → useChatThread({profile}) → api.copilotChat(..., contextProfile)
// → gövde {context:{profile}} → sunucu WithProfile. Saf codec yok; zincir
// kaynak kapısıyla pinli ([[feedback-tested-but-unreachable]]).
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const read = (rel: string) => readFileSync(resolve(__dirname, rel), 'utf8').replace(/^\s*\/\/.*$/gm, '');

describe('sohbet model profili kablolaması (v0.10.183)', () => {
  it('api.copilotChat profile parametresini gövdeye yazar', () => {
    const src = read('../../lib/api.ts');
    expect(src).toMatch(/contextProfile\?: string/);
    expect(src).toMatch(/\.\.\.\(contextProfile \? \{ profile: contextProfile \} : \{\}\)/);
  });
  it('useChatThread opts.profile → copilotChat son argüman', () => {
    const src = read('./useChatThread.ts');
    expect(src).toMatch(/profile\?: string;/);
    expect(src).toMatch(/o\.toMs \|\| undefined, o\.profile \|\| undefined\)/);
  });
  it("iki yüzey de seçimi hook'a geçirir; seçici yalnız >1 profilde", () => {
    for (const rel of ['./AIDrawer.tsx', '../CopilotChat.tsx']) {
      const src = read(rel);
      expect(src).toMatch(/profile: profile \|\| undefined,/);
      expect(src).toMatch(/profiles\.length > 1/);
    }
  });
});
