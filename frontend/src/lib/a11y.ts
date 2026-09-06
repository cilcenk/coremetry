// a11y.ts — v0.10.394 (dış skill denetimi D3): tıklanabilir satırın klavye
// eşdeğeri TEK yerde. 42 tıklanabilir <tr>'nin 37'sinde tabIndex yoktu;
// AnomaliesPage'in v0.9 kalıbı (tabIndex=0, role=button, Enter/Space)
// burada paylaşılır. Satırın kendi <a>/<button> hücreleri kendi olaylarını
// stopPropagation ile keser (mevcut davranış); Enter/Space yalnız satır
// odaktayken satırı etkinleştirir.
import type { KeyboardEvent, MouseEvent } from 'react';

// rowKeyboard — v0.10.455 (D3 dilim 3): yalnız KLAVYE yarısı. Tık işleyicisi
// olayı okuyan satırlar için (Ctrl/⌘-tık ile farklı davranan lejant/metrik
// satırları): onClick aynen kalır, Enter/Space düz etkinleştirmeyi çağırır.
export function rowKeyboard<E extends HTMLElement = HTMLTableRowElement>(onActivate: () => void): {
  role: 'button';
  tabIndex: 0;
  onKeyDown: (e: KeyboardEvent<E>) => void;
} {
  const { role, tabIndex, onKeyDown } = rowActivation<E>(onActivate);
  return { role, tabIndex, onKeyDown };
}

export function rowActivation<E extends HTMLElement = HTMLTableRowElement>(onActivate: () => void): {
  role: 'button';
  tabIndex: 0;
  onClick: (e: MouseEvent<E>) => void;
  onKeyDown: (e: KeyboardEvent<E>) => void;
} {
  return {
    role: 'button',
    tabIndex: 0,
    onClick: () => onActivate(),
    onKeyDown: (e) => {
      if (e.target !== e.currentTarget) return; // hücre içi input/link kendi Enter'ını korur
      if (e.key === 'Enter' || e.key === ' ') {
        e.preventDefault();
        onActivate();
      }
    },
  };
}
