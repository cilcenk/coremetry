// chatPageContext.pin.test.ts — v0.10.539 kaynak pini: kabuk sayfa bağlamını
// URL'den her rota değişiminde üretir ve hook'a verir; hook api'ye taşır.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const shell = readFileSync(resolve(__dirname, 'CopilotChat.tsx'), 'utf8');
const hook = readFileSync(resolve(__dirname, 'ai', 'useChatThread.ts'), 'utf8');

describe('sayfa bağlamı kablolaması', () => {
  it('CopilotChat → useChatThread({ page }) → api.copilotChat(…, page, pinnedPage)', () => {
    expect(shell).toContain('pageContext(loc.pathname, loc.search)');
    expect(shell).toMatch(/useChatThread\(\{[\s\S]*?\n\s+page,/);
    expect(hook).toContain('o.page || undefined, o.pinnedPage || undefined');
  });
});
