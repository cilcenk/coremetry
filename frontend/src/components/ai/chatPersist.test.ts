import { describe, expect, it } from 'vitest';
import type { ChatTurn } from '@/lib/types';
import {
  PERSIST_MESSAGE_CAP, hasCompletedExchange, persistMessages, restoreTurns,
} from './chatPersist';

// v0.9.1139 (AI Faz 4.1) — arşive NE gider, ekrana NE geri döner.
//
// Üç iddia sessizce kırılabilir:
//   1. HATA turları arşive girmemeli. "Sunucuya ulaşamadım" satırı
//      geçmişte hiçbir işe yaramaz; dahası bir sonraki gönderimde tele
//      geri konup modele "önceki cevabım buydu" diye sunulurdu;
//   2. AKAN (pending) tur yarım metin taşır — kaydetme akış BİTİNCE
//      tetiklenir ama zamanlayıcı yarışırsa yarım cevap kalıcılaşır;
//   3. Geri yüklenen cevaplar exchangeId TAŞIMAZ, yani 👍/👎 gizli
//      kalır (arşiv cevabına oy = ölü affordance, v0.9.592 dersi).

const t = (turn: Partial<ChatTurn> & { role: ChatTurn['role'] }): ChatTurn => turn as ChatTurn;

describe('persistMessages', () => {
  it('tamamlanmış turları role+text olarak taşır', () => {
    expect(persistMessages([
      t({ role: 'user', text: 'checkout yavaş mı?' }),
      t({ role: 'assistant', text: 'p95 480ms', exchangeId: 'x1', steps: ['red'] }),
    ])).toEqual([
      { role: 'user', text: 'checkout yavaş mı?' },
      { role: 'assistant', text: 'p95 480ms' },
    ]);
  });

  it('hata / akan / boş turlar DÜŞER', () => {
    expect(persistMessages([
      t({ role: 'user', text: 'soru' }),
      t({ role: 'assistant', text: 'yarım', pending: true }),
      t({ role: 'assistant', text: '', error: 'stream kesildi' }),
      t({ role: 'assistant', text: '   ' }),
    ])).toEqual([{ role: 'user', text: 'soru' }]);
  });

  it('son 40 ile sınırlı (tavan sunucuda da var)', () => {
    const many = Array.from({ length: 90 }, (_, i) =>
      t({ role: i % 2 ? 'assistant' : 'user', text: `m${i}` }));
    const got = persistMessages(many);
    expect(got.length).toBe(PERSIST_MESSAGE_CAP);
    // En YENİLER kalır.
    expect(got[got.length - 1].text).toBe('m89');
    expect(got[0].text).toBe('m50');
  });
});

describe('restoreTurns', () => {
  it('mesajları çizilebilir turlara çevirir', () => {
    expect(restoreTurns([
      { role: 'user', text: 'soru' },
      { role: 'assistant', text: 'cevap' },
    ])).toEqual([
      { role: 'user', text: 'soru' },
      { role: 'assistant', text: 'cevap' },
    ]);
  });

  it('geri yüklenen cevap exchangeId TAŞIMAZ (oylama gizli kalır)', () => {
    const [, answer] = restoreTurns([
      { role: 'user', text: 'soru' },
      { role: 'assistant', text: 'cevap' },
    ]);
    expect(answer.exchangeId).toBeUndefined();
  });

  it('boş/eksik girdi boş turlar', () => {
    expect(restoreTurns(undefined)).toEqual([]);
    expect(restoreTurns([])).toEqual([]);
    expect(restoreTurns([{ role: 'assistant', text: '  ' }])).toEqual([]);
  });
});

describe('hasCompletedExchange', () => {
  it('soru + tamamlanmış cevap varsa true', () => {
    expect(hasCompletedExchange([
      t({ role: 'user', text: 'soru' }),
      t({ role: 'assistant', text: 'cevap' }),
    ])).toBe(true);
  });

  it('yalnız soru (ya da yalnız hatalı cevap) varsa false — boş kabuk açılmaz', () => {
    expect(hasCompletedExchange([t({ role: 'user', text: 'soru' })])).toBe(false);
    expect(hasCompletedExchange([
      t({ role: 'user', text: 'soru' }),
      t({ role: 'assistant', text: '', error: 'kesildi' }),
    ])).toBe(false);
    expect(hasCompletedExchange([])).toBe(false);
  });
});
