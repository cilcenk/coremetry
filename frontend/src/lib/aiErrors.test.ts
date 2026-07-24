import { describe, expect, it } from 'vitest';
import { aiErrorHint } from './aiErrors';

// v0.9.200 — sağlayıcı hata sınıflandırması: 429/kota, timeout, ağ.
describe('aiErrorHint', () => {
  it('maps quota/429 class', () => {
    expect(aiErrorHint('openai-compat 429: {"error":{"code":429,"message":"You exceeded your current quota"}}')).toMatch(/kotası/);
    expect(aiErrorHint('Rate limit reached')).toMatch(/kotası/);
    expect(aiErrorHint('RESOURCE_EXHAUSTED')).toMatch(/kotası/);
  });
  it('maps timeout + network classes', () => {
    expect(aiErrorHint('context deadline exceeded')).toMatch(/zaman aşımı/);
    expect(aiErrorHint('dial tcp 10.0.0.1:8000: connection refused')).toMatch(/ulaşılamıyor/);
  });
  it('returns null for unknown errors (caller shows raw)', () => {
    expect(aiErrorHint('parse error: unexpected token')).toBeNull();
  });
});
