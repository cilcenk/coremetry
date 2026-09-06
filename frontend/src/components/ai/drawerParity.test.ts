import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';

// drawerParity — v0.10.461 (operatör: "Explain trace çekirdeği CoSRE'nin
// temeli olsun. CoSRE drawer daha küçük ve kötü gözüküyor. Explain trace
// drawer'ı sohbette kullanalım"). İki AI çekmecesinin görsel sözleşmesi
// TEK kaynaktan okunur; bu test kaymayı yakalar (drawerBackdrop.test.ts
// deseni: kaynak pinleri, çünkü genişlik/sınıf sözleşmesi runtime'da
// ölçülemez).
const read = (p: string) => readFileSync(resolve(__dirname, p), 'utf8');
const chat = read('../CopilotChat.tsx');
const ai = read('AIDrawer.tsx');
const bubble = read('ChatBubble.tsx');
const explain = read('../CopilotExplain.tsx');
const css = read('../../styles/globals.css');

describe('AI çekmeceleri aynı genişlik', () => {
  it('v0.10.483 — TEK çekmece: AIDrawer CopilotChat\'e delege eder, kendi Drawer\'ı yok', () => {
    expect(ai).toContain('<CopilotChat />');
    expect(ai).not.toContain('<Drawer');
    expect(chat).toContain('width={expanded ? 1100 : AI_DRAWER_WIDTH}');
    expect(chat).not.toMatch(/width=\{expanded \? 1100 : 480\}/);
    // Özne aynı kabukta: CopilotChat ?ai= öznesini okur ve AIDrawerBody çizer.
    expect(chat).toContain('useAiSubject()');
    expect(chat).toContain('<AIDrawerBody');
  });
  it('AppShell yalnız CopilotChat mount eder (iki çekmece yok)', () => {
    const shell = read('../AppShell.tsx');
    expect(shell).toContain('<CopilotChat />');
    expect(shell).not.toContain('<AIDrawer />');
  });
});

describe('asistan cevabı = Explain kartı', () => {
  it('sınıf globals.css\'te tanımlı', () => {
    expect(css).toContain('.ai-answer-card {');
  });
  it('CopilotExplain kartı sınıfı kullanıyor (inline kopya kalmadı)', () => {
    expect(explain).toContain('className="ai-answer-card"');
    expect(explain).not.toContain("background: 'color-mix(in srgb, var(--accent) 8%, transparent)'");
  });
  it('ChatBubble asistan turu aynı kart, tam genişlik; kullanıcı balonu değişmedi', () => {
    expect(bubble).toContain("className={isUser ? undefined : 'ai-answer-card'}");
    expect(bubble).toContain("alignSelf: isUser ? 'flex-end' : 'stretch'");
    expect(bubble).not.toContain("background: isUser ? 'var(--accent2)' : 'var(--bg2)'");
  });
});

describe('CoSRE başlığı AIDrawer anatomisi', () => {
  it('meta şeridi: kapsam + model çipi; eski kapsam bandı yok', () => {
    expect(chat).toContain('<span className="k">model</span>');
    expect(chat).not.toContain('sorular bu servise scope\'lanır');
  });
  it('v0.10.483 — Explain hedefi (?ai=) aynı çekmecede açıklama kipine geçer; kapatma yok', () => {
    expect(chat).not.toContain('if (/[?&]ai=/.test(to)) setOpen(false);');
    expect(chat).toContain('const drawerOpen = open || subject !== null;');
  });
});
