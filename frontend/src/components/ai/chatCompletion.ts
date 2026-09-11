// chatCompletion — v0.10.687 (D4 "girişte ad tamamlama", cosre-chat-parity
// planı; ai-ui-patterns #7). SAF; React yok. Testi chatCompletion.test.ts.
//
// Tetik (plan §soru 2 cevabı: hem açık hem otomatik):
//   - '@' öneki: 1+ karakter → AÇIK tetik (Slack mention alışkanlığı).
//   - öneksiz: token ≥3 karakter VE '-' içeriyor (bu kurulumdaki servis adları
//     tireli) → OTOMATİK tetik. Sıradan sözcükler ("neden", "yavaş") sunucuya
//     GİTMEZ — picker kuralı: her tuşta katalog çekilmez.
export interface CompletionQuery {
  query: string;
  /** Token'ın metindeki aralığı ('@' dahil) — applyCompletion bunu değiştirir. */
  start: number;
  end: number;
  explicit: boolean;
}

export function completionQuery(text: string, caret: number): CompletionQuery | null {
  const upto = text.slice(0, Math.max(0, Math.min(caret, text.length)));
  const m = /(\S+)$/.exec(upto);
  if (!m) return null;
  const token = m[1];
  const start = upto.length - token.length;
  const end = upto.length;
  if (token.startsWith('@')) {
    const q = token.slice(1);
    return q.length >= 1 ? { query: q, start, end, explicit: true } : null;
  }
  if (token.length >= 3 && token.includes('-')) return { query: token, start, end, explicit: false };
  return null;
}

/** applyCompletion — token'ı kanonik adla değiştirir + boşluk; imleç adın sonrasında. */
export function applyCompletion(text: string, q: CompletionQuery, name: string): { text: string; caret: number } {
  const next = text.slice(0, q.start) + name + ' ' + text.slice(q.end);
  return { text: next, caret: q.start + name.length + 1 };
}

/** moveHighlight — listede sarmalı gezinme. */
export function moveHighlight(i: number, delta: number, n: number): number {
  if (n <= 0) return 0;
  return (((i + delta) % n) + n) % n;
}
