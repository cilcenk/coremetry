// chatInputKey — v0.10.664 (ai-ui-patterns #5): sohbet girişi <textarea>.
// Enter = gönder, Shift+Enter = yeni satır; IME birleştirme (isComposing)
// sırasında Enter gönderMEZ (Japonca/Korece klavye tuzağı). Saf ve testli;
// iki yüzey (CopilotChat, AIDrawerBody) aynı kuralı kullanır.

export interface ChatKeyLike {
  key: string;
  shiftKey?: boolean;
  altKey?: boolean;
  ctrlKey?: boolean;
  metaKey?: boolean;
  isComposing?: boolean;
  nativeEvent?: { isComposing?: boolean };
}

/** Enter (değiştiricisiz, IME dışı) → gönder; Shift/Alt/Ctrl/Meta+Enter → yeni satır. */
export function chatInputSubmitKey(e: ChatKeyLike): boolean {
  if (e.key !== 'Enter') return false;
  if (e.isComposing || e.nativeEvent?.isComposing) return false;
  return !(e.shiftKey || e.altKey || e.ctrlKey || e.metaKey);
}

/** Otomatik yükseklik: içerik kadar, en çok maxPx (kaydırma ondan sonra). */
export const CHAT_INPUT_MAX_PX = 132; // ≈ 6 satır × 18 px + dolgu

export function autoGrowTextarea(el: { style: { height: string }; scrollHeight: number }, maxPx = CHAT_INPUT_MAX_PX): void {
  el.style.height = 'auto';
  el.style.height = `${Math.min(el.scrollHeight, maxPx)}px`;
}
