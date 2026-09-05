// useFocusTrap — v0.10.389 (dış skill denetimi D2): Tab hapsi TEK yerde.
//
// Modal'ın v0.9.924'te kurduğu hapis (mK2) kanonik Drawer'a hiç
// uygulanmamıştı: perde açıkken Tab çekmecenin son öğesinden arkadaki
// sayfaya kaçıyordu ve `role="dialog"` bile beyan edilmiyordu. Aynı
// mantığın ikinci kopyası yerine ortak kanca — Modal da buradan geçer.
//
// Sözleşme (Modal.contract.test'in pinlediği):
//   • Liste HER Tab'da yeniden toplanır (koşullu render'lı formlarda bayat
//     liste odağı var olmayan öğeye gönderirdi).
//   • Görünürlük süzgeci öznitelik tabanlı (offsetParent jsdom'da null).
//   • Odaklanabilir yoksa Tab kökte kalır.
//   • Esc BURADA DEĞİL (useEscLayer katman yığını).
import { useEffect, type RefObject } from 'react';

export const FOCUSABLE_SELECTOR =
  'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])';

export function useFocusTrap(rootRef: RefObject<HTMLElement | null>, active: boolean): void {
  useEffect(() => {
    if (!active) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Tab') return;
      const root = rootRef.current;
      if (!root) return;
      const items = [...root.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR)]
        .filter(el => !el.hasAttribute('disabled')
          && !el.hidden
          && el.getAttribute('aria-hidden') !== 'true');
      if (items.length === 0) { e.preventDefault(); root.focus(); return; }
      const first = items[0];
      const last = items[items.length - 1];
      const active = document.activeElement;
      if (e.shiftKey && (active === first || active === root)) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && active === last) {
        e.preventDefault();
        first.focus();
      }
    };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, [rootRef, active]);
}
