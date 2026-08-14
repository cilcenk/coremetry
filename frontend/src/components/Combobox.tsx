import { useEffect, useId, useMemo, useRef, useState } from 'react';

/**
 * Free-text input with a custom dropdown panel that filters as you
 * type. Replaces the previous native <datalist> implementation —
 * datalist's browser-controlled rendering cuts off long option
 * strings when the input is narrow, which made operation /
 * peer-service pickers unreadable on /traces.
 *
 * Behaviour:
 *   - Opens on focus or arrow click; closes on outside click / Esc.
 *   - Filters options by case-insensitive substring as the user
 *     types. The current input value is the source of truth — Enter
 *     keeps whatever's typed (so the user can submit a string that
 *     isn't in the suggestion list, e.g. a brand-new search term).
 *   - Arrow keys navigate, Enter picks (or fires onEnter if there's
 *     no active highlight), Tab picks and moves focus on.
 *   - Dropdown sizes to its content via CSS (min-width = input
 *     width, max-width capped) so long span names aren't truncated
 *     mid-word.
 */
export function Combobox({
  value, onChange, options, placeholder, width, onEnter,
}: {
  value: string;
  onChange: (v: string) => void;
  options: string[];
  placeholder?: string;
  width?: number | string;
  onEnter?: () => void;
}) {
  const id = useId();
  const wrapRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLDivElement>(null);
  const [open, setOpen] = useState(false);
  const [highlight, setHighlight] = useState<number>(-1);

  // Filtered list — substring match, case-insensitive. Empty query
  // shows the full list so clicking the field reveals all options
  // (matches native <select> "open and look at everything" UX).
  // Cap to 200 rows; service / operation lists in the wild stay
  // well under this but the cap keeps render cheap on degenerate
  // inputs.
  const filtered = useMemo(() => {
    const q = value.trim().toLowerCase();
    if (!q) return options.slice(0, 200);
    return options.filter(o => o.toLowerCase().includes(q)).slice(0, 200);
  }, [value, options]);

  // Reset highlight whenever the filtered set changes — otherwise
  // the index points into a stale list and arrow nav jumps around.
  useEffect(() => { setHighlight(-1); }, [filtered]);

  // Click-outside / Esc close.
  useEffect(() => {
    if (!open) return;
    const onDoc = (e: MouseEvent) => {
      if (!wrapRef.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener('mousedown', onDoc);
    return () => document.removeEventListener('mousedown', onDoc);
  }, [open]);

  // Scroll the highlighted row into view when arrow-navigating past
  // the visible portion of the dropdown.
  useEffect(() => {
    if (!listRef.current || highlight < 0) return;
    const row = listRef.current.querySelector<HTMLElement>(`[data-i="${highlight}"]`);
    row?.scrollIntoView({ block: 'nearest' });
  }, [highlight]);

  const pick = (v: string) => {
    onChange(v);
    setOpen(false);
    setHighlight(-1);
  };

  const onKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      if (!open) setOpen(true);
      setHighlight(h => Math.min(filtered.length - 1, h + 1));
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      setHighlight(h => Math.max(-1, h - 1));
    } else if (e.key === 'Enter') {
      if (open && highlight >= 0 && highlight < filtered.length) {
        e.preventDefault();
        pick(filtered[highlight]);
      } else {
        // No highlight → take the typed value as-is and let the
        // caller submit. Common case: user typed a custom search.
        setOpen(false);
        onEnter?.();
      }
    } else if (e.key === 'Escape') {
      // Katman sözleşmesi (v0.9.950 / Ö28, E2 dalgası): bir Esc BİR
      // katman kapatır. Liste AÇIKKEN Esc'i tüketiyoruz —
      // keyboard.ts'in escLayer'ı defaultPrevented'a bakar; tüketmezsek
      // açık Combobox'lı bir Drawer'da tek Esc ikisini birden kapatır
      // (FilterBuilder bunu yaşıyordu). Liste KAPALIYKEN dokunmuyoruz:
      // olay katmana akar ve Drawer/Modal normal kapanır.
      if (open) {
        e.preventDefault();
        setOpen(false);
        setHighlight(-1);
      }
    } else if (e.key === 'Tab') {
      if (open && highlight >= 0 && highlight < filtered.length) {
        pick(filtered[highlight]);
      } else {
        setOpen(false);
      }
    }
  };

  return (
    <div ref={wrapRef} className="cb-wrap" style={{ width }}>
      <input
        ref={inputRef}
        id={id}
        value={value}
        placeholder={placeholder}
        onChange={e => { onChange(e.target.value); setOpen(true); }}
        onFocus={() => setOpen(true)}
        onClick={() => setOpen(true)}
        onKeyDown={onKeyDown}
        autoComplete="off"
        spellCheck={false}
      />
      {/* Caret indicator + clear button. Caret only when value is
          empty so the affordance pair isn't redundant. */}
      {value ? (
        <button className="cb-clear" type="button"
          aria-label="Clear"
          title="Clear"
          onClick={() => { onChange(''); inputRef.current?.focus(); setOpen(true); }}
          onMouseDown={e => e.preventDefault()}>
          ✕
        </button>
      ) : (
        <button className="cb-caret" type="button" tabIndex={-1}
          aria-label={open ? 'Close' : 'Open'}
          onClick={() => { setOpen(o => !o); inputRef.current?.focus(); }}
          onMouseDown={e => e.preventDefault()}>
          ▾
        </button>
      )}

      {open && filtered.length > 0 && (
        <div ref={listRef} className="cb-list" role="listbox">
          {filtered.map((o, i) => (
            <div
              key={o + i}
              role="option"
              aria-selected={i === highlight}
              data-i={i}
              className={`cb-row${i === highlight ? ' cb-row-on' : ''}${o === value ? ' cb-row-cur' : ''}`}
              onMouseDown={e => { e.preventDefault(); pick(o); }}
              onMouseEnter={() => setHighlight(i)}>
              {renderMatch(o, value)}
            </div>
          ))}
        </div>
      )}
      {open && filtered.length === 0 && value.trim() && (
        <div className="cb-list">
          <div className="cb-row cb-row-empty">No matches — Enter will use the typed value</div>
        </div>
      )}
    </div>
  );
}

// renderMatch highlights the matched substring inside the option
// label so the user sees why each row qualified. Bolded run is the
// first occurrence (case-insensitive); rest stays plain.
function renderMatch(option: string, query: string): React.ReactNode {
  const q = query.trim();
  if (!q) return option;
  const lc = option.toLowerCase();
  const i = lc.indexOf(q.toLowerCase());
  if (i < 0) return option;
  return (
    <>
      {option.slice(0, i)}
      <b style={{ color: 'var(--accent2)' }}>{option.slice(i, i + q.length)}</b>
      {option.slice(i + q.length)}
    </>
  );
}
