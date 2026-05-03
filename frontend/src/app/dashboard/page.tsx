'use client';
import { Suspense, useEffect, useState } from 'react';
import { useRouter, useSearchParams } from 'next/navigation';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { useAuth } from '@/components/AuthProvider';
import { PanelRenderer } from '@/components/dashboard/PanelRenderer';
import { PanelEditor, defaultConfig } from '@/components/dashboard/PanelEditor';
import { api } from '@/lib/api';
import type { Dashboard, Panel, PanelType, TimeRange } from '@/lib/types';

// Wrapper handles the Suspense requirement of useSearchParams() in App
// Router with static export.
export default function DashboardPage() {
  return <Suspense fallback={<Spinner />}><Inner /></Suspense>;
}

function Inner() {
  const sp = useSearchParams();
  const router = useRouter();
  const { user } = useAuth();
  const id = sp.get('id') ?? '';
  const startInEdit = sp.get('edit') === '1';

  const [range, setRange] = useState<TimeRange>({ preset: '1h' });
  const [doc, setDoc] = useState<Dashboard | null | undefined>(undefined);
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState<Dashboard | null>(null);
  const [editingPanel, setEditingPanel] = useState<string | null>(null); // panel id
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!id) return;
    setDoc(undefined);
    api.getDashboard(id).then(d => {
      setDoc(d);
      // panels arrives as JSON-encoded string on the wire (json.RawMessage),
      // normalize it to an array for our local state.
      const panels = normalizePanels(d.panels);
      setDraft({ ...d, panels });
      if (startInEdit && user?.role === 'admin') setEditing(true);
    }).catch(() => setDoc(null));
  }, [id]);

  if (!id) return <Empty icon="◫" title="No dashboard selected" />;
  if (doc === undefined) return <Spinner />;
  if (doc === null) return <Empty icon="⚠" title="Dashboard not found" />;
  if (!draft) return <Spinner />;

  const isAdmin = user?.role === 'admin';
  const panels: Panel[] = draft.panels;

  const updatePanel = (panel: Panel) => {
    setDraft({ ...draft, panels: panels.map(p => p.id === panel.id ? panel : p) });
  };
  const addPanel = (type: PanelType) => {
    const p: Panel = {
      id: rid(), type, title: `New ${type}`, width: 2, config: defaultConfig(type),
    };
    setDraft({ ...draft, panels: [...panels, p] });
    setEditingPanel(p.id);
  };
  const deletePanel = (id: string) => {
    setDraft({ ...draft, panels: panels.filter(p => p.id !== id) });
    setEditingPanel(null);
  };
  const save = async () => {
    setBusy(true);
    try {
      const updated = await api.updateDashboard(id, {
        name: draft.name, description: draft.description, panels: draft.panels,
      });
      setDoc({ ...updated, panels: normalizePanels(updated.panels) });
      setEditing(false);
    } finally {
      setBusy(false);
    }
  };
  const cancel = () => {
    setDraft({ ...doc, panels: normalizePanels(doc.panels) });
    setEditing(false);
    setEditingPanel(null);
  };
  const removeDashboard = async () => {
    if (!confirm('Delete this dashboard?')) return;
    await api.deleteDashboard(id);
    router.push('/dashboards');
  };

  const editingPanelObj = editingPanel ? panels.find(p => p.id === editingPanel) : null;

  return (
    <>
      <Topbar title={draft.name} range={range} onRangeChange={setRange} />
      <div id="content">
        <div className="controls" style={{ marginBottom: 14 }}>
          {editing ? (
            <>
              <input value={draft.name} placeholder="Dashboard name"
                onChange={e => setDraft({ ...draft, name: e.target.value })}
                style={{ width: 220 }} />
              <input value={draft.description} placeholder="Description"
                onChange={e => setDraft({ ...draft, description: e.target.value })}
                style={{ width: 320 }} />
              <AddPanelMenu onAdd={addPanel} />
              <span style={{ marginLeft: 'auto' }} />
              <button className="sec" onClick={cancel}>Cancel</button>
              <button onClick={save} disabled={busy}>{busy ? 'Saving…' : 'Save'}</button>
            </>
          ) : (
            <>
              {draft.description && (
                <span style={{ color: 'var(--text2)', fontSize: 12 }}>{draft.description}</span>
              )}
              <span style={{ marginLeft: 'auto' }} />
              {isAdmin && (
                <>
                  <button className="sec" onClick={removeDashboard}
                    style={{ color: 'var(--err)' }}>Delete</button>
                  <button onClick={() => setEditing(true)}>Edit</button>
                </>
              )}
            </>
          )}
        </div>

        {panels.length === 0 ? (
          <Empty icon="◫" title="No panels yet">
            {editing ? 'Use "+ Add panel" above to start building.'
                     : 'Click Edit to add panels.'}
          </Empty>
        ) : (
          <div style={{
            display: 'grid', gridTemplateColumns: 'repeat(4, 1fr)', gap: 12,
          }}>
            {panels.map(p => (
              <div key={p.id} style={{
                gridColumn: `span ${Math.max(1, Math.min(4, p.width))}`,
                background: 'var(--bg2)', border: '1px solid var(--border)',
                borderRadius: 6, padding: 10,
                position: 'relative',
              }}>
                <div style={{
                  display: 'flex', alignItems: 'center', marginBottom: 6,
                  fontSize: 12, color: 'var(--text2)',
                }}>
                  <span style={{ fontWeight: 600, color: 'var(--text)' }}>{p.title}</span>
                  {editing && (
                    <span style={{ marginLeft: 'auto', display: 'flex', gap: 4 }}>
                      <button className="sec" onClick={() => setEditingPanel(p.id)}
                        style={{ padding: '2px 7px', fontSize: 11 }}>Edit</button>
                      <button className="sec" onClick={() => deletePanel(p.id)}
                        style={{ padding: '2px 7px', fontSize: 11, color: 'var(--err)' }}>×</button>
                    </span>
                  )}
                </div>
                <PanelRenderer panel={p} range={range} />
              </div>
            ))}
          </div>
        )}

        {editingPanelObj && (
          <PanelEditor panel={editingPanelObj}
            onChange={updatePanel}
            onClose={() => setEditingPanel(null)}
            onDelete={() => deletePanel(editingPanelObj.id)} />
        )}
      </div>
    </>
  );
}

function AddPanelMenu({ onAdd }: { onAdd: (t: PanelType) => void }) {
  const [open, setOpen] = useState(false);
  return (
    <div style={{ position: 'relative' }}>
      <button onClick={() => setOpen(o => !o)}>+ Add panel</button>
      {open && (
        <div style={{
          position: 'absolute', top: '100%', left: 0, marginTop: 4,
          background: 'var(--bg2)', border: '1px solid var(--border)',
          borderRadius: 6, padding: 4, zIndex: 50, minWidth: 180,
          boxShadow: '0 8px 24px rgba(0,0,0,0.3)',
        }}>
          {(['metric', 'spanmetric', 'stat', 'markdown'] as PanelType[]).map(t => (
            <button key={t}
              onClick={() => { onAdd(t); setOpen(false); }}
              style={{
                display: 'block', width: '100%', textAlign: 'left',
                padding: '6px 10px', background: 'transparent', border: 'none',
                color: 'var(--text2)', fontSize: 13, cursor: 'pointer',
              }}
              onMouseEnter={e => (e.currentTarget.style.background = 'var(--bg)')}
              onMouseLeave={e => (e.currentTarget.style.background = 'transparent')}>
              {t === 'metric' && 'Metric (line)'}
              {t === 'spanmetric' && 'Span aggregation (line)'}
              {t === 'stat' && 'Stat (single value)'}
              {t === 'markdown' && 'Markdown / notes'}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

function rid(): string {
  return Math.random().toString(36).slice(2, 10);
}

// Backend returns panels as a JSON-encoded string (json.RawMessage). Some
// endpoints (PUT) round-trip it as an array. Normalize both to Panel[].
function normalizePanels(raw: unknown): Panel[] {
  if (Array.isArray(raw)) return raw as Panel[];
  if (typeof raw === 'string') {
    try { const parsed = JSON.parse(raw); return Array.isArray(parsed) ? parsed : []; }
    catch { return []; }
  }
  return [];
}
