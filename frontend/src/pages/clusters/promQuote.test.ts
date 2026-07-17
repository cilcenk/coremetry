import { describe, it, expect } from 'vitest';
import { promQuote } from './promQuote';

// v0.9.44 — PromQL etiket kaçışlama: ", \ ve newline'lı değerler
// yapıştırılabilir sorguyu bozamaz.
describe('promQuote', () => {
  it('passes plain DNS-1123 values through', () => {
    expect(promQuote('coremetry-go-demo-7b794685f9-glltq')).toBe(
      'coremetry-go-demo-7b794685f9-glltq');
  });
  it('escapes double quotes', () => {
    expect(promQuote('prod"cluster')).toBe('prod\\"cluster');
  });
  it('escapes backslashes before quotes (order matters)', () => {
    expect(promQuote('a\\b"c')).toBe('a\\\\b\\"c');
  });
  it('escapes newlines', () => {
    expect(promQuote('x\ny')).toBe('x\\ny');
  });
  it('keeps empty string empty', () => {
    expect(promQuote('')).toBe('');
  });
});
