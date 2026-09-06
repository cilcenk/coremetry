import { useRef, type CSSProperties, type KeyboardEvent, type ReactNode } from 'react';

// TabStrip — v0.10.456 (dış skill denetimi D5): sekme şeridi ATOMU. 18 şerit
// `.tab-strip` sınıfıyla elle yazılıyordu, sıfırında role="tablist" yoktu:
// ekran okuyucu sekme olduğunu bilmiyor, ok tuşlarıyla gezilemiyordu.
// Görsel dil AYNI (.tab-strip / > button / .active CSS'i korunur); atom
// yalnız anatomiyi ve ARIA'yı tek yere alır:
//   role=tablist + aria-label, her düğme role=tab + aria-selected,
//   roving tabindex (yalnız etkin sekme Tab durağı), ←/→ (Home/End) odağı
//   taşır VE etkinleştirir (WAI-ARIA otomatik etkinleştirme), type=button.
// `children` düğmelerden SONRA çizilir (şeridin sağındaki linkler için).
export interface TabItem<K extends string> {
  key: K;
  label: ReactNode;
  title?: string;
  disabled?: boolean;
}

export function TabStrip<K extends string>({ tabs, value, onChange, ariaLabel, className, style, stopPropagation, children }: {
  tabs: ReadonlyArray<TabItem<K>>;
  value: K;
  onChange: (key: K) => void;
  /** Şeridin adı (ekran okuyucu): "Trace görünümü", "Servis sekmeleri". */
  ariaLabel: string;
  className?: string;
  style?: CSSProperties;
  /** Tıklanabilir bir satırın İÇİNDEKİ şerit (LogTable): tık satıra sızmasın. */
  stopPropagation?: boolean;
  children?: ReactNode;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const enabled = tabs.filter(t => !t.disabled);
  const onKeyDown = (e: KeyboardEvent<HTMLButtonElement>, idx: number) => {
    const cur = enabled.findIndex(t => t.key === tabs[idx].key);
    if (cur < 0 || enabled.length === 0) return;
    let next = -1;
    if (e.key === 'ArrowRight' || e.key === 'ArrowDown') next = (cur + 1) % enabled.length;
    else if (e.key === 'ArrowLeft' || e.key === 'ArrowUp') next = (cur - 1 + enabled.length) % enabled.length;
    else if (e.key === 'Home') next = 0;
    else if (e.key === 'End') next = enabled.length - 1;
    if (next < 0) return;
    e.preventDefault();
    const target = enabled[next];
    onChange(target.key);
    const btn = ref.current?.querySelector<HTMLButtonElement>(`button[data-tab-key="${CSS.escape(target.key)}"]`);
    btn?.focus();
  };
  return (
    <div ref={ref} role="tablist" aria-label={ariaLabel}
      className={['tab-strip', className].filter(Boolean).join(' ')} style={style}>
      {tabs.map((t, i) => {
        const active = t.key === value;
        return (
          <button key={t.key} type="button" role="tab" data-tab-key={t.key}
            aria-selected={active} tabIndex={active ? 0 : -1}
            className={active ? 'active' : ''} title={t.title} disabled={t.disabled}
            onClick={e => { if (stopPropagation) e.stopPropagation(); onChange(t.key); }}
            onKeyDown={e => onKeyDown(e, i)}>
            {t.label}
          </button>
        );
      })}
      {children}
    </div>
  );
}
