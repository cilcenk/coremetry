// toolSteps.test.ts — v0.10.161 (Copilot araç-çağrısı şeffaflık paneli, seçenek A).
// Saf çekirdek: özet satırı sayıları, ToolErrorJSON ayrıştırma, ilk satır,
// görünür satır kesimi, bütçe aşımı algısı. Sözleşme (yargıç must-fix'leri):
//   - toplam süre YALNIZ tüm yürütülen adımların durationMs'i varsa; yoksa null («—»)
//   - guided rozeti step.origin'den, delta'dan DEĞİL
//   - hata sayacı ok=false olan HER sonuç (timeout + tekrar koruması dâhil)
import { describe, it, expect } from 'vitest';
import { summarizeSteps, parseToolError, previewFirstLine, visibleRows, isDeadlineError, VISIBLE_ROWS } from './toolSteps';
import type { ChatStepDetail } from '@/lib/types';

const d = (i: number, over: Partial<ChatStepDetail> = {}): ChatStepDetail => ({ i, tool: `t${i}`, ok: true, preview: 'x', ...over });

describe('summarizeSteps', () => {
  it('sayar: araç, hata, toplam süre (hepsi ölçülmüşse)', () => {
    const s = summarizeSteps([d(1, { durationMs: 210 }), d(2, { durationMs: 340, ok: false }), d(3, { durationMs: 20000, ok: false })]);
    expect(s).toMatchObject({ count: 3, errors: 2, totalMs: 20550, guided: false, pending: 0 });
  });
  it('bir adımın süresi yoksa toplam null («—»), bilinmeyen sayılır', () => {
    const s = summarizeSteps([d(1, { durationMs: 210 }), d(2)]);
    expect(s.totalMs).toBeNull();
    expect(s.unknownDuration).toBe(1);
  });
  it('sonucu gelmemiş adım pending sayılır ve hataya girmez', () => {
    const s = summarizeSteps([d(1, { durationMs: 5 }), { i: 2, tool: 'search_traces' }]);
    expect(s.pending).toBe(1);
    expect(s.errors).toBe(0);
    expect(s.totalMs).toBeNull();
  });
  it('tur bittiyse kanıtsız adım «sürüyor» DEĞİL «kanıt yok» (sunucu boş metinde step-result yayınlamaz)', () => {
    const s = summarizeSteps([d(1, { durationMs: 5 }), { i: 2, tool: 'search_traces' }], true);
    expect(s.pending).toBe(0);
    expect(s.noEvidence).toBe(1);
    expect(s.totalMs).toBeNull();
  });
  it('guided = TÜM adımlar origin=guided (karışık → false)', () => {
    expect(summarizeSteps([d(1, { origin: 'guided' }), d(2, { origin: 'guided' })]).guided).toBe(true);
    expect(summarizeSteps([d(1, { origin: 'guided' }), d(2)]).guided).toBe(false);
    expect(summarizeSteps([]).guided).toBe(false);
  });
});

describe('parseToolError', () => {
  it('ToolErrorJSON şeklini ayrıştırır', () => {
    const p = parseToolError('{"error":"timeout","retryable":true,"hint":"pencereyi daralt","detail":"code: 159"}');
    expect(p).toEqual({ cls: 'timeout', retryable: true, hint: 'pencereyi daralt', detail: 'code: 159' });
  });
  it('JSON değilse ya da error alanı yoksa null', () => {
    expect(parseToolError('error: upstream 502')).toBeNull();
    expect(parseToolError('{"ok":false}')).toBeNull();
    expect(parseToolError('')).toBeNull();
  });
});

describe('previewFirstLine + visibleRows + deadline', () => {
  it('ilk satır, kırpılmış, boşsa «(boş)»', () => {
    expect(previewFirstLine('{"services":[1]}\nsecond', 8)).toBe('{"servic…');
    expect(previewFirstLine('', 20)).toBe('(boş)');
    expect(previewFirstLine(undefined, 20)).toBe('');
  });
  it('kapalıyken ilk 5, açıkken hepsi', () => {
    const rows = Array.from({ length: 12 }, (_, i) => d(i + 1));
    expect(visibleRows(rows, false).length).toBe(VISIBLE_ROWS);
    expect(visibleRows(rows, true).length).toBe(12);
    expect(visibleRows(rows.slice(0, 3), false).length).toBe(3);
  });
  it('bütçe aşımı metni (chatDeadlineMessageTR) algılanır, öteki hatalar değil', () => {
    expect(isDeadlineError('Bu alışveriş 3 dakika tavanına dayandı ve durduruldu.')).toBe(true);
    expect(isDeadlineError('openai-compat call: connection refused')).toBe(false);
    expect(isDeadlineError(undefined)).toBe(false);
  });
});
