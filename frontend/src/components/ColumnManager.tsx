import { useEffect, useMemo, useRef, useState } from 'react';
import { api } from '@/lib/api';

// ColumnManager — "+ Column" affordance shared by trace tables on
// /traces and /explore. Click opens a Combobox-style picker fed by
// /api/attribute-keys (live span + resource attribute keys observed
// in the last hour) merged with a list of common semconv keys; each
// pick fires `onAdd(key)`. Caps at 8 user columns to keep the
// backend SELECT projection bounded.
//
// Performance: the attribute-keys fetch runs once per panel open
// (not on every render); result is cached in state. The trace-list
// refetch the caller triggers on each addition is bounded by the
// 8-col cap.
export function ColumnManager({ cols, onAdd }: {
  cols: string[];
  onAdd: (k: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const [keys, setKeys] = useState<string[] | null>(null);
  const [query, setQuery] = useState('');
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open || keys !== null) return;
    api.attributeKeys('1h', 500)
      .then(res => {
        const live = (res ?? []).map(r => r.key);
        const seed = [
          'http.method', 'http.route', 'http.status_code', 'http.url',
          'rpc.system', 'rpc.service', 'rpc.method',
          'db.system', 'db.statement', 'db.operation', 'db.name',
          'messaging.system', 'messaging.destination.name', 'messaging.operation',
          'peer.service', 'server.address', 'kind',
        ];
        setKeys([...new Set([...seed, ...live])].sort());
      })
      .catch(() => setKeys([]));
  }, [open, keys]);

  useEffect(() => {
    if (!open) return;
    const onDoc = (e: MouseEvent) => {
      if (!ref.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener('mousedown', onDoc);
    return () => document.removeEventListener('mousedown', onDoc);
  }, [open]);

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    const all = keys ?? [];
    const remaining = all.filter(k => !cols.includes(k));
    if (!q) return remaining.slice(0, 50);
    return remaining.filter(k => k.toLowerCase().includes(q)).slice(0, 50);
  }, [keys, query, cols]);

  const atLimit = cols.length >= 8;

  return (
    <div ref={ref} style={{ position: 'relative', display: 'inline-block' }}>
      <button type="button" disabled={atLimit}
        onClick={() => setOpen(o => !o)}
        title={atLimit ? 'Column limit reached (8)' : 'Add an attribute column'}
        style={{
          padding: '2px 8px', fontSize: 11, fontWeight: 600,
          background: 'transparent', color: 'var(--accent2)',
          border: '1px dashed var(--border)', borderRadius: 4,
          cursor: atLimit ? 'not-allowed' : 'pointer',
        }}>
        + Column
      </button>
      {open && (
        <div style={{
          // v0.8.76 — operator-reported: the picker "didn't appear" on /traces.
          // It rendered but anchored right:0 (opening LEFTWARD), and since the
          // "+ Column" button sits near the left of the toolbar, the panel body
          // extended under the sidebar — whose stacking context painted over it.
          // Anchor left:0 so it opens rightward into the content area instead.
          position: 'absolute', left: 0, top: 'calc(100% + 4px)', zIndex: 60,
          minWidth: 280, maxWidth: 360,
          background: 'var(--bg2)', border: '1px solid var(--border)',
          borderRadius: 6, boxShadow: '0 8px 24px rgba(0,0,0,0.30)',
          padding: 6,
        }}>
          <input autoFocus
            value={query} onChange={e => setQuery(e.target.value)}
            placeholder="Filter attribute keys…"
            style={{ width: '100%', marginBottom: 6, fontSize: 12 }} />
          <div style={{ maxHeight: 280, overflowY: 'auto' }}>
            {keys === null && (
              <div style={{ padding: 8, fontSize: 11, color: 'var(--text3)' }}>Loading…</div>
            )}
            {keys && filtered.length === 0 && (
              <div style={{ padding: 8, fontSize: 11, color: 'var(--text3)', fontStyle: 'italic' }}>
                {query.trim()
                  ? `No keys match "${query}". Press Enter to add it as a custom column.`
                  : 'No more attribute keys to add.'}
              </div>
            )}
            {filtered.map(k => (
              <div key={k}
                onClick={() => { onAdd(k); setOpen(false); setQuery(''); }}
                style={{
                  padding: '5px 8px', fontSize: 12, cursor: 'pointer',
                  fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
                  borderRadius: 3,
                }}
                onMouseEnter={e => (e.currentTarget.style.background = 'var(--bg3)')}
                onMouseLeave={e => (e.currentTarget.style.background = 'transparent')}>
                {k}
              </div>
            ))}
          </div>
          {query.trim() && keys && filtered.length === 0 && /^[a-zA-Z0-9._-]+$/.test(query.trim()) && (
            <button type="button"
              onClick={() => { onAdd(query.trim()); setOpen(false); setQuery(''); }}
              style={{
                width: '100%', marginTop: 4, fontSize: 11,
                padding: '4px 8px',
              }}>
              Add custom column &quot;{query.trim()}&quot;
            </button>
          )}
          <div style={{ fontSize: 10, color: 'var(--text3)', padding: '6px 8px 0', borderTop: '1px solid var(--border)', marginTop: 6 }}>
            keys from spans seen in the last 1h
          </div>
        </div>
      )}
    </div>
  );
}
