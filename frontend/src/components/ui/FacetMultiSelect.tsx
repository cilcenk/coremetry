import { useEffect, useRef, useState } from 'react';

// FacetMultiSelect — v0.9.357 (operator-approved mockup C).
//
// A compact, counted multi-select dropdown for the Problems filter bar. The
// chip rows (mockup A, v0.9.356) solved the click cost but kept two visual
// languages on one bar: chips for priority/kind, <select>s for Owner/SRE.
// The operator's call: "C daha uygun bizim kuruma" — one language.
//
// The button label SUMMARISES state ("Exceptions +3 kapalı") so the bar can
// be read without opening anything; the panel holds checkbox rows with the
// SAME counts the chips carried (density moves one click away, never
// disappears), a per-row "sadece" (the isolate gesture the chart legends and
// v0.9.356's chips already taught), and a "tümünü seç" footer.
//
// House rules honoured: no new packages, token-level CSS in globals.css
// (.fsel*), URL writes stay in the caller's codec, and the pure summary
// logic is exported for vitest.

export type FacetOption = { value: string; label: string; count: number };

// facetSummary — the button text. Pure; tested in FacetMultiSelect.test.ts.
//   all selected  → "tümü"
//   ≤2 selected   → labels joined " + ", then "+N kapalı" if any are off
//   ≥3 selected   → "N seçili +M kapalı"
export function facetSummary(selected: string[], total: number): string {
  const off = total - selected.length;
  if (off <= 0) return 'tümü';
  const head = selected.length <= 2 ? selected.join(' + ') : `${selected.length} seçili`;
  return `${head} +${off} kapalı`;
}

export function FacetMultiSelect({ label, options, selected, onToggle, onSolo, onAll }: {
  label: string;
  options: FacetOption[];
  selected: Set<string>;
  onToggle: (v: string) => void;
  onSolo: (v: string) => void;
  onAll: () => void;
}) {
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);

  // Click-outside + Esc close. Listener only while open, so a page full of
  // these costs nothing at rest.
  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (rootRef.current && !rootRef.current.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') setOpen(false); };
    document.addEventListener('mousedown', onDown);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('mousedown', onDown);
      document.removeEventListener('keydown', onKey);
    };
  }, [open]);

  const selLabels = options.filter(o => selected.has(o.value)).map(o => o.label);
  const allOn = selLabels.length >= options.length;

  return (
    <div className="fsel" ref={rootRef}>
      <button type="button" className="fsel-btn" aria-expanded={open}
        onClick={() => setOpen(o => !o)}>
        <span className="fsel-l">{label}:</span> <b>{facetSummary(selLabels, options.length)}</b> ▾
      </button>
      {open && (
        <div className="fsel-panel" role="listbox" aria-label={label}>
          {options.map(o => {
            const on = selected.has(o.value);
            const lastOn = on && selLabels.length === 1;
            return (
              <div key={o.value} className="fsel-row" role="option" aria-selected={on}
                onClick={() => { if (!lastOn) onToggle(o.value); }}>
                <input type="checkbox" readOnly checked={on} tabIndex={-1} />
                {o.label}
                {!lastOn && (
                  <button type="button" className="fsel-solo" title={`Yalnız ${o.label}`}
                    onClick={e => { e.stopPropagation(); onSolo(o.value); setOpen(false); }}>
                    sadece
                  </button>
                )}
                <span className="fsel-n">{o.count}</span>
              </div>
            );
          })}
          {!allOn && (
            <>
              <div className="fsel-sep" />
              <button type="button" className="fsel-all" onClick={() => { onAll(); }}>
                tümünü seç
              </button>
            </>
          )}
        </div>
      )}
    </div>
  );
}
