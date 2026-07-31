import { describe, expect, it } from 'vitest';
import { mdLite } from './ChatBubble';

// v0.9.419 pinleri — mdLite (v0.9.479'da CopilotChat.tsx'ten ai/ChatBubble.tsx'e
// taşındı; davranış aynı): 32-hex trace id'ler data-nav linkine döner
// (href SALT hex'ten kurulur — injection yüzeyi yok), escape HER ZAMAN
// linkify'dan önce (XSS), kod/kalın davranışı değişmedi.
describe('mdLite', () => {
  const tid = 'a1b2c3d4e5f60718293a4b5c6d7e8f90';

  it('32-hex trace id → data-nav link', () => {
    const html = mdLite(`trace ${tid} yavaş`);
    expect(html).toContain(`<a href="/trace?id=${tid}" data-nav="1">${tid}</a>`);
  });

  it('31 hex ya da hex-olmayan token linklenmez', () => {
    expect(mdLite(tid.slice(0, 31))).not.toContain('<a ');
    expect(mdLite('g'.repeat(32))).not.toContain('<a ');
    // 33-hex: \b sınırı içinde 32'lik alt-parça yakalanmamalı
    expect(mdLite(tid + '0')).not.toContain('<a ');
  });

  it('escape linkify\'dan ÖNCE — HTML enjekte edilemez', () => {
    const html = mdLite(`<img src=x onerror=alert(1)> ${tid}`);
    expect(html).not.toContain('<img');
    expect(html).toContain('&lt;img');
    expect(html).toContain('data-nav');
  });

  it('kod + kalın davranışı korunur', () => {
    expect(mdLite('a `k` **b**')).toBe('a <code>k</code> <b>b</b>');
  });
});
