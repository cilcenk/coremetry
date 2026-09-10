// @vitest-environment jsdom
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { isNearBottom, findScrollParent, STICK_THRESHOLD_PX } from './stickToBottom';

// v0.10.650 — ai-ui-patterns #4: oto-kaydırma yalnız dipteyken yapışır.
// Saf eşik + kap bulucu + BAĞLANMA pinleri (iki yüzey de hook'u kullanır,
// her-delta'da-dibe-zorla deseni kalktı).

describe('isNearBottom', () => {
  it('tam dipte → true', () => {
    expect(isNearBottom({ scrollTop: 900, scrollHeight: 1000, clientHeight: 100 })).toBe(true);
  });
  it('eşik içinde → true, eşik dışında → false', () => {
    expect(isNearBottom({ scrollTop: 900 - STICK_THRESHOLD_PX, scrollHeight: 1000, clientHeight: 100 })).toBe(true);
    expect(isNearBottom({ scrollTop: 900 - STICK_THRESHOLD_PX - 1, scrollHeight: 1000, clientHeight: 100 })).toBe(false);
  });
  it('yukarıda okuyan kullanıcı → false (yapışma kapanır)', () => {
    expect(isNearBottom({ scrollTop: 0, scrollHeight: 5000, clientHeight: 600 })).toBe(false);
  });
  it('içerik kaptan kısaysa → true', () => {
    expect(isNearBottom({ scrollTop: 0, scrollHeight: 300, clientHeight: 600 })).toBe(true);
  });
});

describe('findScrollParent', () => {
  it('kendisi kaydırılabilirse kendisi; değilse en yakın overflow-y auto/scroll atası; yoksa null', () => {
    const outer = document.createElement('div'); outer.style.overflowY = 'auto';
    const mid = document.createElement('div');
    const inner = document.createElement('div');
    outer.appendChild(mid); mid.appendChild(inner); document.body.appendChild(outer);
    expect(findScrollParent(outer)).toBe(outer);
    expect(findScrollParent(inner)).toBe(outer);
    const lone = document.createElement('div'); document.body.appendChild(lone);
    expect(findScrollParent(lone)).toBeNull();
    expect(findScrollParent(null)).toBeNull();
    outer.remove(); lone.remove();
  });
});

describe('BAĞLANMA (kaynak pinleri)', () => {
  const src = (f: string) => readFileSync(new URL(f, import.meta.url), 'utf8');
  it('iki yüzey de useStickToBottom kullanır ve her turns değişiminde dibe zorlamaz', () => {
    const chat = src('../CopilotChat.tsx');
    const drawer = src('./AIDrawerBody.tsx');
    expect(chat).toContain('useStickToBottom(');
    expect(drawer).toContain('useStickToBottom(');
    // Eski desenler: her delta'da koşulsuz dibe.
    expect(chat).not.toContain("scrollRef.current?.scrollTo({ top: scrollRef.current.scrollHeight, behavior: 'smooth' });\n  }, [turns, open]);");
    expect(drawer).not.toContain("endRef.current?.scrollIntoView({ block: 'nearest', behavior: 'smooth' });\n  }, [turns]);");
  });
  it('kullanıcı sorusu gönderince pin (koşulsuz dip)', () => {
    for (const f of ['../CopilotChat.tsx', './AIDrawerBody.tsx']) {
      const s = src(f);
      const submit = s.indexOf('const submit = ');
      expect(submit, f).toBeGreaterThan(-1);
      expect(s.slice(submit, submit + 200), f).toContain('pinBottom()');
    }
  });
});
