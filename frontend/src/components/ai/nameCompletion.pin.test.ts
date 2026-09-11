import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

// v0.10.687 — D4 ad tamamlama bağlanma pini: CopilotChat girişi hook'u
// çağırır, popup çizer, klavye dalı gönderim kuralından ÖNCE gelir
// (Enter açık listede seçer, gönder değil), a11y nitelikleri var; hook boş
// sorguda istek atmaz ve sunucu aramasını (api.serviceNames) kullanır —
// istemcide katalog süzme YOK (picker kuralı).
const chat = readFileSync(resolve(__dirname, '../CopilotChat.tsx'), 'utf8');
const hook = readFileSync(resolve(__dirname, 'useNameCompletion.ts'), 'utf8');

describe('D4 ad tamamlama (v0.10.687)', () => {
  it('CopilotChat: hook + popup + a11y', () => {
    expect(chat).toContain('useNameCompletion(input, caret)');
    expect(chat).toContain('<NameCompletionPopup');
    expect(chat).toContain('aria-autocomplete="list"');
    expect(chat).toContain('aria-activedescendant=');
  });
  it('klavye dalı gönderimden önce', () => {
    const i = chat.indexOf('if (completion.open) {');
    const j = chat.indexOf('if (chatInputSubmitKey(e))', i);
    expect(i).toBeGreaterThan(-1);
    expect(j).toBeGreaterThan(i);
    const block = chat.slice(i, j);
    for (const k of ["'ArrowDown'", "'ArrowUp'", "'Enter' || e.key === 'Tab'", "'Escape'"]) expect(block).toContain(k);
  });
  it('hook: sunucu araması, boşta istek yok, 180 ms debounce', () => {
    expect(hook).toContain('api.serviceNames(dq, 20)');
    expect(hook).toContain('enabled: dq.length > 0');
    expect(hook).toContain('180)');
  });
});
