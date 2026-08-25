import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { isAbortError, settleStoppedTurn } from './chatAbort';
import type { ChatTurn } from '@/lib/types';

// v0.10.23 — Copilot denetimi: iptal affordance'ı YOKTU. AbortController
// kuruluydu ama yalnız unmount'ta ateşleniyordu ve CopilotChat AppShell'de
// KALICI monte, yani çekmeceyi kapatmak bile akışı durdurmuyordu.
// Operatörün yapabileceği tek şey beklemekti — input da disabled={busy}
// olduğu için yeni soru bile yazamıyordu.

const turn = (o: Partial<ChatTurn> = {}): ChatTurn =>
  ({ role: 'assistant', text: '', pending: true, ...o } as ChatTurn);

describe('isAbortError', () => {
  // Tarayıcılar ve polyfill'ler farklı şekiller üretiyor. Yanlış negatif,
  // kullanıcıya SAHTE BİR ARIZA göstermek demek.
  it.each([
    ['DOMException şekli', { name: 'AbortError', message: 'x' }],
    ['Chrome metni', new Error('signal is aborted without reason')],
    ['düz ad', new Error('AbortError')],
    ['Firefox metni', new Error('The operation was aborted.')],
  ])('%s → iptal', (_n, err) => expect(isAbortError(err)).toBe(true));

  it.each([
    ['ağ hatası', new Error('dial tcp 10.0.0.1:8000: connection refused')],
    ['sağlayıcı 500', new Error('openai-compat 500: internal error')],
    ['null', null],
    ['undefined', undefined],
    ['boş metin', new Error('')],
  ])('%s → iptal DEĞİL', (_n, err) => expect(isAbortError(err)).toBe(false));
});

describe('settleStoppedTurn', () => {
  it('⚠ AKAN METİN KORUNUR', () => {
    // Operatör çoğu zaman "yeterince gördüm" ya da "yanlış yola gitti"
    // diye durduruyor. O ana kadarki metni silmek, durdurma sebebinin
    // kendisini yok etmek olurdu.
    const t = settleStoppedTurn(turn({ text: 'checkout-service p99 340ms ve' }));
    expect(t.text).toBe('checkout-service p99 340ms ve');
    expect(t.stopped).toBe(true);
    expect(t.pending).toBe(false);
  });

  it('HATA olarak işaretlenmez — bu bir arıza değil, kullanıcının kararı', () => {
    const t = settleStoppedTurn(turn({ text: 'yarım', error: 'AbortError' }));
    expect(t.error).toBeUndefined();
  });

  it('metin hiç gelmediyse balon BOŞ kalmaz', () => {
    // Boş bir balon "cevap geldi ama boş" izlenimi verir.
    expect(settleStoppedTurn(turn({ text: '' })).text).toBe('Durduruldu.');
    expect(settleStoppedTurn(turn({ text: '   ' })).text).toBe('Durduruldu.');
  });

  it('diğer alanlar korunur', () => {
    const t = settleStoppedTurn(turn({ text: 'x', steps: ['a'], exchangeId: 'e1' }));
    expect(t.steps).toEqual(['a']);
    expect(t.exchangeId).toBe('e1');
  });
});

// KABLOLAMA PİNİ — saf çekirdek yeşil ama çağrılmıyorsa kusur yerinde
// kalır (bu depoda tekrar eden sınıf: v0.9.1334, v0.10.11).
describe('kablolama', () => {
  const hook = readFileSync(new URL('./useChatThread.ts', import.meta.url), 'utf8');
  const chat = readFileSync(new URL('../CopilotChat.tsx', import.meta.url), 'utf8');

  it('hook stop() dışa veriyor', () => {
    expect(hook).toContain('const stop = useCallback(');
    expect(hook).toContain('abortRef.current?.abort()');
    expect(hook).toMatch(/return \{[^}]*\bstop\b/);
  });

  it('catch dalı iptali arızadan AYIRIYOR', () => {
    // Ayrılmazsa kasıtlı bir kullanıcı eylemi kırmızı hata balonu olur.
    expect(hook).toContain('if (isAbortError(err))');
    expect(hook).toContain('patchLast(settleStoppedTurn)');
  });

  it('CopilotChat durdurma düğmesini çiziyor ve stop\'a bağlıyor', () => {
    expect(chat).toContain('onClick={stop}');
    expect(chat).toContain('Durdur');
  });

  it('akarken Gönder yerine Durdur çıkıyor', () => {
    // İki düğme yan yana durursa hangisinin etkin olduğu belirsizleşir.
    expect(chat).toContain('{busy ? (');
  });
});
