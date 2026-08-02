import { useCallback, useEffect, useRef, useState } from 'react';
import { api } from '@/lib/api';
import type { ChatMessage, ChatTurn } from '@/lib/types';
import { rateTurn } from './ChatBubble';

// useChatThread — sohbet turlarının state'i + gönderme döngüsü
// (v0.9.479). CopilotChat.tsx'in içinden çıkarıldı; AI çekmecesindeki
// sohbet AYNI çekirdeği kullanır (ikinci bir chat implementasyonu yok).
// Yüzeyler yalnız KABUKTA ayrışır: global pencere sağ-alt FAB + kendi
// drawer'ı, çekmece sohbeti ise açıklamanın altındaki bölüm.
//
// Konuşma efemer (sunucuda satır yok): her gönderimde tüm geçmiş
// /api/copilot/chat'e postlanır, SSE tüketilir.

export interface ChatThreadOpts {
  // Context-awareness (v0.9.164/184) — bulunulan sayfanın servisi ve
  // seçili operasyonu; sunucudaki guided router varsayılan alır.
  service?: string;
  operation?: string;
  // explain (v0.9.479) — AI çekmecesindeki açıklamanın metni. Sunucu
  // narration bloğuna katar; boşken tüm davranış global chat'inkiyle
  // aynıdır (internal/api/copilot_drawer.go).
  explain?: string;
  // subject (v0.9.482) — çekmecenin öznesi, `?ai=` kodeği biçiminde
  // (formatAiParam). Sunucu bundan ilgili explain'in HAM KANITINI
  // (trace span'leri + ilişkili loglar / exception paketi) yeniden kurar;
  // açıklamanın metni takip sorularına yetmiyordu (operatör raporu).
  // Global sohbette YOKTUR — orada guided/tool yolları veriyi kendi çeker.
  subject?: string;
  // seed (v0.9.479) — tele giden ama EKRANDA ÇİZİLMEYEN ön turlar.
  // Çekmece, öznesinin doğal sorusunu buraya koyar: sunucudaki
  // takip-devralma (v0.9.410) onu "önceki soru" olarak görür, böylece
  // "peki hata logları?" gibi takipler guided router'da servise oturur.
  seed?: ChatMessage[];
  // rangeS (v0.9.529) — EKRANDAKİ zaman aralığı, saniye. Soru açık bir
  // pencere taşımıyorsa sunucu sabit 30dk yerine bunu kullanır; soru
  // pencere taşıyorsa ("son 24 saatte…") soru kazanır.
  rangeS?: number;
  // trace (v0.9.537) — EKRANDAKİ trace ID'si (/trace?id=). "bu trace
  // neden yavaş" gibi ID'siz sorular sunucuda buna oturur.
  trace?: string;
}

export function useChatThread(opts: ChatThreadOpts = {}) {
  const [turns, setTurns] = useState<ChatTurn[]>([]);
  const [busy, setBusy] = useState(false);
  // send stabil kimlikli (useCallback []) — güncel seçenekleri/turları
  // ref üstünden okur, böylece her render'da yeniden kurulmaz ve
  // CopilotChat'in `coremetry:ai-ask` köprüsü bayat closure çağırmaz.
  const optsRef = useRef(opts);
  optsRef.current = opts;
  const turnsRef = useRef(turns);
  turnsRef.current = turns;
  const busyRef = useRef(false);
  const abortRef = useRef<AbortController | null>(null);

  useEffect(() => () => abortRef.current?.abort(), []);

  const send = useCallback(async (text: string) => {
    const q = text.trim();
    if (!q || busyRef.current) return;
    const o = optsRef.current;
    const history: ChatMessage[] = [
      ...(o.seed ?? []),
      ...turnsRef.current.filter(t => !t.error).map(t => ({ role: t.role, text: t.text })),
      { role: 'user', text: q },
    ];
    setTurns(prev => [
      ...prev,
      { role: 'user', text: q },
      { role: 'assistant', text: '', steps: [], pending: true },
    ]);
    busyRef.current = true;
    setBusy(true);
    const ac = new AbortController();
    abortRef.current = ac;

    const patchLast = (fn: (t: ChatTurn) => ChatTurn) =>
      setTurns(prev => prev.map((t, i) => (i === prev.length - 1 ? fn(t) : t)));

    try {
      await api.copilotChat(history, (e) => {
        if (e.kind === 'step') {
          patchLast(t => ({ ...t, steps: [...(t.steps ?? []), e.tool] }));
        } else if (e.kind === 'delta') {
          patchLast(t => ({ ...t, text: (t.text ?? '') + e.text }));
        } else if (e.kind === 'answer') {
          patchLast(t => ({ ...t, text: e.text, exchangeId: e.exchangeId, sources: e.sources, suggestions: e.suggestions, links: e.links, pending: false }));
        } else if (e.kind === 'error') {
          patchLast(t => ({ ...t, error: e.error, pending: false }));
        } else if (e.kind === 'done') {
          patchLast(t => ({ ...t, pending: false }));
        }
      }, ac.signal, o.service || undefined, o.operation || undefined, o.explain || undefined,
        o.subject || undefined, o.rangeS || undefined, o.trace || undefined);
    } catch (err) {
      patchLast(t => ({ ...t, error: err instanceof Error ? err.message : String(err), pending: false }));
    } finally {
      busyRef.current = false;
      setBusy(false);
      abortRef.current = null;
    }
  }, []);

  const rate = useCallback((idx: number, verdict: 1 | -1) => {
    rateTurn(turnsRef.current, idx, verdict, setTurns);
  }, []);

  const clear = useCallback(() => setTurns([]), []);

  // Takip çipleri yalnız son tur TAMAMLANMIŞ bir asistan cevabıysa görünür.
  const last = turns[turns.length - 1];
  const showFollowups = !busy && !!last && last.role === 'assistant' && !last.pending && !last.error && !!last.text;

  return { turns, busy, send, rate, clear, last, showFollowups };
}
