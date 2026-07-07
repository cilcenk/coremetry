import { describe, it, expect, beforeEach } from 'vitest';
import { sanitizeRedirect, savePostLoginRedirect, consumePostLoginRedirect } from './postLoginRedirect';

// v0.8.367 — post-login deep-link restore. The sanitizer is the
// security boundary: only same-origin in-app paths may be restored.
describe('sanitizeRedirect', () => {
  it('accepts an in-app deep link with query + hash', () => {
    const url = '/traces?range=1h&sort=duration&filters=%5B%7B%22k%22%3A%22db.statement%22%7D%5D#top';
    expect(sanitizeRedirect(url)).toBe(url);
  });

  it('rejects absolute and protocol-relative URLs (open redirect)', () => {
    expect(sanitizeRedirect('https://evil.example/phish')).toBeNull();
    expect(sanitizeRedirect('//evil.example/phish')).toBeNull();
    expect(sanitizeRedirect('javascript:alert(1)')).toBeNull();
  });

  it('rejects /login and public surfaces', () => {
    expect(sanitizeRedirect('/login')).toBeNull();
    expect(sanitizeRedirect('/login?error=x')).toBeNull();
    expect(sanitizeRedirect('/public/trace?token=abc')).toBeNull();
  });

  it('rejects empty / null', () => {
    expect(sanitizeRedirect(null)).toBeNull();
    expect(sanitizeRedirect('')).toBeNull();
    expect(sanitizeRedirect('traces')).toBeNull();
  });
});

describe('save + consume round-trip', () => {
  beforeEach(() => sessionStorage.clear());

  it('restores once, then clears', () => {
    savePostLoginRedirect('/service?name=archive-service&tab=details');
    expect(consumePostLoginRedirect()).toBe('/service?name=archive-service&tab=details');
    expect(consumePostLoginRedirect()).toBeNull();
  });

  it('never stores a rejected value', () => {
    savePostLoginRedirect('//evil.example');
    expect(consumePostLoginRedirect()).toBeNull();
  });

  it('sanitizes on read too (storage tampered between visits)', () => {
    sessionStorage.setItem('coremetry-post-login-redirect', 'https://evil.example');
    expect(consumePostLoginRedirect()).toBeNull();
  });
});
