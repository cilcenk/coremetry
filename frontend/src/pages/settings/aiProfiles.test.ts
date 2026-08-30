// aiProfiles.test.ts — v0.10.176 sözleşmesi.
import { describe, it, expect } from 'vitest';
import { slugifyProfileId, PROFILE_ID_RE, tuningSummary, endpointLabel, profileUsable } from './aiProfiles';

describe('aiProfiles yardımcıları', () => {
  it('slug: Türkçe harfler, boşluk, sınır; sunucu regex\'iyle uyumlu', () => {
    for (const [inp, want] of [['Büyük Gemma 31B', 'buyuk-gemma-31b'], ['  --Küçük/qwen_8b  ', 'kucuk-qwen_8b'], ['Şu İş', 'su-is'], ['***', '']] as const) {
      expect(slugifyProfileId(inp)).toBe(want);
      if (want) expect(PROFILE_ID_RE.test(want)).toBe(true);
    }
    expect(slugifyProfileId('x'.repeat(60)).length).toBe(40);
  });
  it('tuning özeti: eksikler yazılmaz; sıfır sıcaklık bir DEĞERDİR', () => {
    expect(tuningSummary({})).toBe('küresel');
    expect(tuningSummary({ maxTokens: 8192, temperature: 0, timeoutS: 60 })).toBe('8k tok · t=0 · 60 s');
    expect(tuningSummary({ maxTokens: 512, temperature: null })).toBe('512 tok');
  });
  it('endpoint etiketi + kullanılabilirlik', () => {
    expect(endpointLabel('openai', 'http://vllm:8000/v1')).toBe('http://vllm:8000/v1');
    expect(endpointLabel('openai')).toBe('api.openai.com');
    expect(endpointLabel('anthropic')).toBe('api.anthropic.com');
    expect(profileUsable({ provider: 'openai', hasKey: false, baseUrl: 'http://x' })).toBe(true);
    expect(profileUsable({ provider: 'anthropic', hasKey: false })).toBe(false);
    expect(profileUsable({ provider: 'anthropic', hasKey: true })).toBe(true);
  });
});
