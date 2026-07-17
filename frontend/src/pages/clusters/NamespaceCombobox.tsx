import { useEffect, useMemo, useRef, useState } from 'react';

// NamespaceCombobox — namespace typeahead (v0.9.34, design handoff
// "Namespace typeahead"). Native <select> DEĞİL (yüzlerce seçenek):
// focus dropdown'ı açar, "N of M" başlık, büyük-küçük duyarsız
// substring filtre, ~80 render-cap + "+K more — refine search",
// "All namespaces" reset, ✕ seçili olanı temizler. Dışarı tık/blur
// kapatır (ColumnManager v0.8.76 anchor dersi: left:0, içeriğe açılır).
const RENDER_CAP = 80;

export function NamespaceCombobox({ namespaces, value, onPick, onClear }: {
  namespaces: string[];
  value: string;
  onPick: (ns: string) => void;
  onClear: () => void;
}) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState('');
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const onDoc = (e: MouseEvent) => {
      if (!ref.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener('mousedown', onDoc);
    return () => document.removeEventListener('mousedown', onDoc);
  }, [open]);

  const q = query.trim().toLowerCase();
  const filtered = useMemo(
    () => (q ? namespaces.filter(n => n.toLowerCase().includes(q)) : namespaces),
    [namespaces, q]);
  const shown = filtered.slice(0, RENDER_CAP);
  const overflow = filtered.length - shown.length;

  const pick = (ns: string) => { onPick(ns); setOpen(false); setQuery(''); };

  return (
    <div ref={ref} style={{ position: 'relative', display: 'inline-block' }}>
      <input
        value={open ? query : value}
        onFocus={() => setOpen(true)}
        onChange={e => { setQuery(e.target.value); if (!open) setOpen(true); }}
        placeholder={value || 'Filter namespaces…'}
        style={{
          width: 220, padding: '4px 10px', fontSize: 12,
          background: 'var(--bg)', color: 'var(--text)',
          border: '1px solid var(--border)', borderRadius: 4,
        }} />
      {value && !open && (
        <button type="button" onClick={onClear}
          title="Clear namespace"
          style={{
            position: 'absolute', right: 6, top: '50%', transform: 'translateY(-50%)',
            all: 'unset', cursor: 'pointer', color: 'var(--text3)', fontSize: 12,
          }}>✕</button>
      )}
      {open && (
        <div style={{
          position: 'absolute', left: 0, top: 'calc(100% + 4px)', zIndex: 60,
          minWidth: 240, maxWidth: 320,
          background: 'var(--bg2)', border: '1px solid var(--border)',
          borderRadius: 6, boxShadow: '0 8px 24px rgba(0,0,0,0.30)', padding: 6,
        }}>
          <div style={{
            fontSize: 10, color: 'var(--text3)', padding: '2px 8px 6px',
            textTransform: 'uppercase', letterSpacing: '.06em',
          }}>
            {filtered.length} of {namespaces.length} namespaces
          </div>
          <div style={{ maxHeight: 300, overflowY: 'auto' }}>
            <div onClick={() => { onClear(); setOpen(false); setQuery(''); }}
              style={{ padding: '5px 8px', fontSize: 12, cursor: 'pointer', color: 'var(--accent2)', borderRadius: 3 }}
              onMouseEnter={e => (e.currentTarget.style.background = 'var(--bg3)')}
              onMouseLeave={e => (e.currentTarget.style.background = 'transparent')}>
              All namespaces
            </div>
            {shown.map(n => (
              <div key={n} onClick={() => pick(n)}
                style={{
                  padding: '5px 8px', fontSize: 12, cursor: 'pointer', borderRadius: 3,
                  fontFamily: 'ui-monospace, monospace',
                  background: n === value ? 'var(--bg3)' : undefined,
                }}
                onMouseEnter={e => (e.currentTarget.style.background = 'var(--bg3)')}
                onMouseLeave={e => (e.currentTarget.style.background = n === value ? 'var(--bg3)' : 'transparent')}>
                {n}
              </div>
            ))}
            {overflow > 0 && (
              <div style={{ padding: '6px 8px', fontSize: 11, color: 'var(--text3)', fontStyle: 'italic' }}>
                +{overflow} more — refine search
              </div>
            )}
            {filtered.length === 0 && (
              <div style={{ padding: 8, fontSize: 11, color: 'var(--text3)', fontStyle: 'italic' }}>
                No namespace matches “{query}”.
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  );
}
