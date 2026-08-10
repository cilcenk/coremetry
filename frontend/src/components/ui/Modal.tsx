import { useEffect, useRef, type ReactNode } from 'react';
import { createPortal } from 'react-dom';

// Modal — focus-trap dialog with backdrop click + ESC close.
// Replaces the hand-rolled inline-styled modals (NewIncident,
// ChangePassword, panel-editor flyouts) with a single accessible
// pattern. Renders into a portal at document.body so the dialog
// isn't trapped inside the parent's overflow/transform context.
//
// Accessibility:
//   - role="dialog" + aria-modal="true" via .modal-dialog — v0.9.924'e
//     kadar bu bir YALANDI: Tab diyaloğun dışına çıkabiliyordu. Artık
//     Tab/Shift+Tab diyaloğun içinde DÖNÜYOR, yani rol doğru.
//   - focuses the first focusable element inside on open, and
//     restores focus to the trigger element on close.
//   - ESC closes (matches OS convention).
//   - Backdrop click closes; pointer events on dialog stop
//     propagation so a click inside doesn't close.

export interface ModalProps {
  open: boolean;
  onClose: () => void;
  title?: ReactNode;
  // footer is optional — for a "Cancel / Save" action row that
  // sits below the dialog body with its own divider + bg2 strip.
  footer?: ReactNode;
  children?: ReactNode;
  // initialFocus selector — defaults to the first focusable
  // descendant. Useful when you want the email input focused
  // instead of the close button.
  initialFocus?: string;
  // size cap — default 480px works for 90% of forms. 'lg' raises
  // it to 720px for things like the panel editor.
  size?: 'sm' | 'md' | 'lg';
}

const sizeMax: Record<NonNullable<ModalProps['size']>, number> = {
  sm: 360,
  md: 480,
  lg: 720,
};

export function Modal({
  open, onClose, title, footer, children, initialFocus, size = 'md',
}: ModalProps) {
  const dialogRef = useRef<HTMLDivElement>(null);
  const lastFocusRef = useRef<Element | null>(null);

  useEffect(() => {
    if (!open) return;
    lastFocusRef.current = document.activeElement;

    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.stopPropagation();
        onClose();
        return;
      }
      // mK2 (v0.9.924) — Tab hapsi. `aria-modal="true"` bugüne kadar
      // YALAN söylüyordu: rol "arkadaki her şey inert" diye duyuruyordu
      // ama Tab diyaloğun son öğesinden çıkıp altındaki sayfaya
      // geçiyordu. Odak görünmez bir yere kayınca kullanıcı hangi
      // öğede olduğunu kaybediyor ve diyaloğa dönüş yolu kalmıyor —
      // yanlış duyurulan bir rolün klasik bedeli.
      if (e.key !== 'Tab') return;
      const root = dialogRef.current;
      if (!root) return;
      // Liste HER Tab'da YENİDEN toplanıyor. Bir kere toplanıp
      // saklanırsa koşullu render'lı formlarda (bir seçim yeni alanlar
      // açıyor, bir hata satırı beliriyor) bayat liste üzerinden
      // sarılır ve odak var olmayan bir öğeye gönderilir.
      const items = [...root.querySelectorAll<HTMLElement>(
        'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])')]
        // Görünürlük süzgeci `offsetParent`la YAPILAMAZ: o düzen
        // gerektirir, jsdom'da her zaman null döner ve testte hapis
        // tamamen ölür — yani kapıyı kuramazdık. Öznitelik tabanlı
        // süzgeç hem tarayıcıda hem testte aynı sonucu verir.
        // Bilinen sınır: CSS ile gizlenmiş (visibility/display) bir
        // odaklanabilir öğe listede kalır. Tarayıcıda o öğe zaten
        // odaklanamaz, dolayısıyla hapis SIZDIRMAZ — en kötü ihtimalle
        // Tab bir tur yerinde sayar.
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

    // Defer the focus call to the next tick — without this the
    // portal hasn't mounted into the DOM yet on the first render
    // pass, so querySelector returns null and focus stays on the
    // trigger (often a Link) which screen readers then announce
    // out-of-context.
    const t = setTimeout(() => {
      const root = dialogRef.current;
      if (!root) return;
      const target = (initialFocus
        ? root.querySelector<HTMLElement>(initialFocus)
        : root.querySelector<HTMLElement>(
            'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])'))
        ?? root;
      target.focus({ preventScroll: true });
    }, 0);

    // Lock body scroll while the modal is open — feels janky
    // when the page underneath drifts on mobile.
    const prevOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';

    return () => {
      clearTimeout(t);
      document.removeEventListener('keydown', onKey);
      document.body.style.overflow = prevOverflow;
      // Restore focus to whatever owned it before the modal opened
      // (typically the button that triggered the open).
      if (lastFocusRef.current instanceof HTMLElement) {
        lastFocusRef.current.focus({ preventScroll: true });
      }
    };
  }, [open, onClose, initialFocus]);

  if (!open) return null;
  return createPortal(
    <div className="modal-backdrop"
         onMouseDown={e => { if (e.target === e.currentTarget) onClose(); }}>
      <div ref={dialogRef}
           className="modal-dialog"
           role="dialog"
           aria-modal="true"
           aria-labelledby={title ? 'modal-title' : undefined}
           tabIndex={-1}
           style={{ maxWidth: sizeMax[size] }}
           onMouseDown={e => e.stopPropagation()}>
        {title && (
          <div className="modal-header">
            <span id="modal-title">{title}</span>
            <button type="button"
                    className="modal-close"
                    onClick={onClose}
                    aria-label="Close dialog">×</button>
          </div>
        )}
        <div className="modal-body">{children}</div>
        {footer && <div className="modal-footer">{footer}</div>}
      </div>
    </div>,
    document.body,
  );
}
