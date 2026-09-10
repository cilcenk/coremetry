import type { ChatTurn } from '@/lib/types';

// chatRetry — v0.10.650 (ai-ui-patterns bulgusu #3). Hata sonrası soru
// KAYBOLUYORDU: submit input'u göndermeden önce siliyor, hatalı asistan turu
// kırmızı kalıyor ve operatörün tek yolu soruyu yeniden yazmaktı. InsightCard
// "↺ Yeniden dene" taşıyor, sohbet taşımıyordu.
//
// Saf yardımcılar: hook `retry()` bunlarla başarısız kuyruğu (soru + hatalı
// cevap) düşürüp aynı soruyu yeniden gönderir. Kuyruk düşmezse model aynı
// soruyu geçmişte iki kez görür (send geçmişi error'suz turlardan kurar —
// soru turu error taşımaz).

/**
 * failedQuestion — son tur hatayla bitmiş bir asistan turuysa ve ondan önceki
 * tur kullanıcı sorusuysa o soruyu döndürür; aksi hâlde null. Akan (pending)
 * tur "başarısız" değildir; durdurulan tur (stopped) da değildir — o
 * operatörün kararıdır.
 */
export function failedQuestion(turns: ChatTurn[]): string | null {
  const n = turns.length;
  if (n < 2) return null;
  const last = turns[n - 1];
  const prev = turns[n - 2];
  if (last.role !== 'assistant' || !last.error || last.pending || last.stopped) return null;
  if (prev.role !== 'user' || !prev.text || !prev.text.trim()) return null;
  return prev.text;
}

/** dropFailedTail — başarısız kuyruk varsa soru + hatalı cevabı düşürür. */
export function dropFailedTail(turns: ChatTurn[]): ChatTurn[] {
  return failedQuestion(turns) === null ? turns : turns.slice(0, -2);
}
