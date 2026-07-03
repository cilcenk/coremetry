import { useEffect, useRef, useState } from 'react';
import { Button } from '@/components/ui';
import { api } from '@/lib/api';
import { toast } from '@/lib/toast';
import { useAuth } from '@/components/AuthProvider';

// TopologyHiddenControl (v0.8.241, operator-requested) — the global
// hidden-pattern list ("kafka:log*", "kafka:bsa*", …). Matching nodes
// are dropped SERVER-side from every topology view, for everyone —
// policy, not a personal toggle. Editor+ can edit; viewers see the
// list read-only. One pattern per line; * and ? glob wildcards match
// against the node name with its "queue:" prefix stripped.
export function TopologyHiddenControl() {
  const { user } = useAuth();
  const canEdit = user?.role === 'admin' || user?.role === 'editor';
  const [open, setOpen] = useState(false);
  const [patterns, setPatterns] = useState<string[]>([]);
  const [draft, setDraft] = useState('');
  const [busy, setBusy] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    api.getTopologyHidden()
      .then(d => setPatterns(d.patterns ?? []))
      .catch(() => { /* toolbar chrome — the graph itself surfaces errors */ });
  }, []);

  useEffect(() => {
    if (!open) return;
    const onDoc = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener('mousedown', onDoc);
    return () => document.removeEventListener('mousedown', onDoc);
  }, [open]);

  const startEdit = () => {
    setDraft(patterns.join('\n'));
    setOpen(o => !o);
  };

  const save = async () => {
    setBusy(true);
    try {
      const next = draft.split('\n').map(p => p.trim()).filter(Boolean);
      const res = await api.putTopologyHidden(next);
      setPatterns(res.patterns);
      setOpen(false);
      toast.success('Hidden patterns saved — graph updates on the next refresh (≤60s cache).');
    } catch (e) {
      toast.error('Save failed: ' + (e instanceof Error ? e.message : String(e)));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div ref={ref} style={{ position: 'relative', display: 'inline-block' }}>
      <Button variant="secondary" size="sm" onClick={startEdit}
        title="Globally hidden topology nodes (glob patterns, matched with the queue: prefix stripped). Applied server-side for every user.">
        Hidden ({patterns.length})
      </Button>
      {open && (
        <div style={{
          position: 'absolute', top: '100%', right: 0, zIndex: 40, marginTop: 4,
          width: 280, padding: 10, borderRadius: 6,
          background: 'var(--bg2)', border: '1px solid var(--border)',
          boxShadow: '0 4px 16px rgba(0,0,0,0.35)',
        }}>
          <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 6 }}>
            One glob per line (e.g. <code>kafka:log*</code>). Matching nodes
            never render — for anyone.
          </div>
          {canEdit ? (
            <>
              <textarea value={draft} onChange={e => setDraft(e.target.value)}
                rows={5} spellCheck={false}
                style={{ width: '100%', fontFamily: 'var(--mono, monospace)', fontSize: 12, resize: 'vertical' }} />
              <div style={{ display: 'flex', gap: 6, marginTop: 8, justifyContent: 'flex-end' }}>
                <Button variant="ghost" size="sm" onClick={() => setOpen(false)}>Cancel</Button>
                <Button variant="primary" size="sm" disabled={busy} onClick={save}>
                  {busy ? 'Saving…' : 'Save'}
                </Button>
              </div>
            </>
          ) : (
            <pre style={{ margin: 0, fontSize: 12, whiteSpace: 'pre-wrap' }}>
              {patterns.length ? patterns.join('\n') : '(none)'}
            </pre>
          )}
        </div>
      )}
    </div>
  );
}
