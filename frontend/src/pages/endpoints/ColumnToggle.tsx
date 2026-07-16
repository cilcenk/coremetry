import { useEffect, useRef, useState } from 'react';
import { Columns3 } from 'lucide-react';
import { Button } from '@/components/ui';

// ColumnToggle — "Columns" affordance for /endpoints (v0.8.574, audit
// seçenek 3): a checkbox popover over the FIXED column schema, unlike
// ColumnManager which picks from an open attribute-key catalogue.
// Visibility state lives in the URL (?cols=, endpointCols.ts codec) —
// the caller owns it; this component only renders + toggles.
//
// Anchored right:0 — the trigger sits at the right end of the controls
// row, so the panel opens LEFTWARD into the content area. (Mirror-image
// of the ColumnManager v0.8.76 lesson, where a left-edge trigger had to
// anchor left:0 for the same reason.)
export function ColumnToggle({ columns, visible, onChange }: {
  columns: { id: string; label: string }[];
  visible: Set<string>;
  onChange: (next: Set<string>) => void;
}) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const onDoc = (e: MouseEvent) => {
      if (!ref.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener('mousedown', onDoc);
    return () => document.removeEventListener('mousedown', onDoc);
  }, [open]);

  const hiddenCount = columns.length - visible.size;

  const toggle = (id: string) => {
    const next = new Set(visible);
    if (next.has(id)) next.delete(id); else next.add(id);
    // Never allow an empty table — the last visible column stays.
    if (next.size === 0) return;
    onChange(next);
  };

  return (
    <div ref={ref} style={{ position: 'relative', display: 'inline-block' }}>
      <Button variant="secondary" size="sm"
        onClick={() => setOpen(o => !o)}
        title="Show / hide table columns (persists in the URL)">
        <Columns3 size={12} strokeWidth={1.75} />
        Columns{hiddenCount > 0 ? ` (${hiddenCount} hidden)` : ''}
      </Button>
      {open && (
        <div style={{
          position: 'absolute', right: 0, top: 'calc(100% + 4px)', zIndex: 60,
          minWidth: 180,
          background: 'var(--bg2)', border: '1px solid var(--border)',
          borderRadius: 6, boxShadow: '0 8px 24px rgba(0,0,0,0.30)',
          padding: 6,
        }}>
          {columns.map(c => (
            <label key={c.id}
              style={{
                display: 'flex', alignItems: 'center', gap: 7,
                padding: '4px 8px', fontSize: 12, cursor: 'pointer',
                borderRadius: 3, userSelect: 'none',
              }}
              onMouseEnter={e => (e.currentTarget.style.background = 'var(--bg3)')}
              onMouseLeave={e => (e.currentTarget.style.background = 'transparent')}>
              <input type="checkbox"
                checked={visible.has(c.id)}
                onChange={() => toggle(c.id)} />
              {c.label}
            </label>
          ))}
          {hiddenCount > 0 && (
            <div style={{ borderTop: '1px solid var(--border)', marginTop: 6, paddingTop: 6 }}>
              <Button variant="ghost" size="sm" style={{ width: '100%' }}
                onClick={() => onChange(new Set(columns.map(c => c.id)))}>
                Show all
              </Button>
            </div>
          )}
        </div>
      )}
    </div>
  );
}
