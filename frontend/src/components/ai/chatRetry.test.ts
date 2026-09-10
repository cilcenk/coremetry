import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { failedQuestion, dropFailedTail } from './chatRetry';
import type { ChatTurn } from '@/lib/types';

// v0.10.650 — ai-ui-patterns #3: hata sonrası "↺ Yeniden dene". Saf
// yardımcılar + BAĞLANMA pinleri (hook'ta retry, iki yüzeyde onRetry,
// balonda düğme). Saf yeşil ama kablosuz = bug aynen durur.

const u = (text: string): ChatTurn => ({ role: 'user', text });
const a = (over: Partial<ChatTurn> = {}): ChatTurn => ({ role: 'assistant', text: '', ...over });

describe('failedQuestion / dropFailedTail', () => {
  it('hatalı asistan turu + önceki soru → soru; kuyruk düşer', () => {
    const turns = [u('ilk'), a({ text: 'cevap' }), u('checkout neden yavaş?'), a({ error: 'deadline' })];
    expect(failedQuestion(turns)).toBe('checkout neden yavaş?');
    expect(dropFailedTail(turns)).toEqual([u('ilk'), a({ text: 'cevap' })]);
  });
  it('akan (pending) tur başarısız değildir', () => {
    const turns = [u('soru'), a({ error: 'x', pending: true })];
    expect(failedQuestion(turns)).toBeNull();
    expect(dropFailedTail(turns)).toBe(turns);
  });
  it('durdurulan tur operatörün kararıdır, yeniden denenmez', () => {
    expect(failedQuestion([u('soru'), a({ error: 'x', stopped: true })])).toBeNull();
  });
  it('hatasız kuyruk / son tur kullanıcı / boş liste → null', () => {
    expect(failedQuestion([u('soru'), a({ text: 'tam' })])).toBeNull();
    expect(failedQuestion([u('soru')])).toBeNull();
    expect(failedQuestion([])).toBeNull();
  });
});

describe('BAĞLANMA (kaynak pinleri)', () => {
  const src = (f: string) => readFileSync(new URL(f, import.meta.url), 'utf8');
  it('useChatThread retry sunar ve göndermeden ÖNCE kuyruğu turnsRef\'ten düşürür', () => {
    const hook = src('./useChatThread.ts');
    expect(hook).toContain('const retry = useCallback(');
    // send geçmişi turnsRef.current'tan kurar; state güncellemesi asenkron
    // olduğundan ref de aynı anda kırpılmalı, yoksa soru geçmişte iki kez.
    const i = hook.indexOf('turnsRef.current = trimmed');
    expect(i).toBeGreaterThan(-1);
    expect(i).toBeLessThan(hook.indexOf('void send(q)', i));
    expect(hook).toMatch(/return \{[^}]*\bretry\b[^}]*\}/);
  });
  it('iki yüzey de son hatalı tura onRetry geçirir', () => {
    for (const f of ['../CopilotChat.tsx', './AIDrawerBody.tsx']) {
      expect(src(f), f).toContain('onRetry={');
      expect(src(f), f).toContain('retry');
    }
  });
  it('balon onRetry varken "↺ Yeniden dene" düğmesi çizer', () => {
    const b = src('./ChatBubble.tsx');
    expect(b).toContain('onRetry?: () => void');
    expect(b).toContain('↺ Yeniden dene');
  });
});
