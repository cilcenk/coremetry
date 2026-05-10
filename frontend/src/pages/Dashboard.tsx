import { Suspense, useEffect, useMemo, useState } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { useAuth } from '@/components/AuthProvider';
import { PanelRenderer } from '@/components/dashboard/PanelRenderer';
import { PanelEditor, defaultConfig } from '@/components/dashboard/PanelEditor';
import { VariablesBar } from '@/components/dashboard/VariablesBar';
import type { DashboardVariable } from '@/lib/types';
import { api } from '@/lib/api';
import type { Dashboard, Panel, PanelType, TimeRange } from '@/lib/types';

// Wrapper handles the Suspense requirement of useSearchParams() in App
// Router with static export.
export default function DashboardPage() {
  return <Suspense fallback={<Spinner />}><Inner /></Suspense>;
}

function Inner() {
  const [sp] = useSearchParams();
  const navigate = useNavigate();
  const { user } = useAuth();
  const id = sp.get('id') ?? '';
  const startInEdit = sp.get('edit') === '1';

  const [range, setRange] = useState<TimeRange>({ preset: '15m' });
  const [doc, setDoc] = useState<Dashboard | null | undefined>(undefined);
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState<Dashboard | null>(null);
  const [editingPanel, setEditingPanel] = useState<string | null>(null); // panel id
  const [busy, setBusy] = useState(false);
  // Resolved values for the dashboard's Grafana-style variables.
  // URL-persisted so reloads + share-links keep the choice.
  // Empty value for a variable means "all" — the renderer drops any
  // predicate line that references the empty variable so the panel
  // shows aggregates across the relevant universe.
  const [varValues, setVarValues] = useState<Record<string, string>>(() => {
    const init: Record<string, string> = {};
    sp.forEach((v, k) => {
      // Reserve route params like id/edit/range for their own slots; everything
      // else becomes a candidate variable value. Cheaper than parsing the
      // dashboard's variable list synchronously here.
      if (k === 'id' || k === 'edit' || k === 'range') return;
      init[k] = v;
    });
    return init;
  });

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

  // Mirror the variable values into the URL so the selection survives
  // reloads + is shareable. One param per variable name. Empty values
  // get removed so we don't accumulate dead params on toggle.
  //
  // NOTE: defined BEFORE the early returns below — Rules of Hooks
  // require every hook to be called on every render in the same
  // order. Putting this after the early returns crashed the page on
  // second render (when doc finished loading) because the hook count
  // changed mid-mount.
  useEffect(() => {
    const url = new URL(window.location.href);
    const reserved = new Set(['id', 'edit', 'range']);
    for (const key of Array.from(url.searchParams.keys())) {
      if (!reserved.has(key)) url.searchParams.delete(key);
    }
    for (const [k, v] of Object.entries(varValues)) {
      if (v) url.searchParams.set(k, v);
    }
    window.history.replaceState({}, '', url.toString());
  }, [varValues]);

  // Parsed variable definitions (live from the dashboard doc) drive
  // both the picker bar and what gets substituted into panels. Same
  // Rules-of-Hooks reasoning as the URL mirror — declared before any
  // conditional returns.
  const variables: DashboardVariable[] = useMemo(() => {
    const raw = doc?.variables;
    if (!raw) return [];
    if (Array.isArray(raw)) return raw as DashboardVariable[];
    try {
      const parsed = JSON.parse(raw as unknown as string);
      return Array.isArray(parsed) ? parsed : [];
    } catch { return []; }
  }, [doc?.variables]);

  if (!id) return <Empty icon="◫" title="No dashboard selected" />;
  if (doc === undefined) return <Spinner />;
  if (doc === null) return <Empty icon="⚠" title="Dashboard not found" />;
  if (!draft) return <Spinner />;

  const isAdmin = user?.role === 'admin' || user?.role === 'editor';
  const panels: Panel[] = draft.panels ?? [];

  const updatePanel = (panel: Panel) => {
    setDraft({ ...draft, panels: panels.map(p => p.id === panel.id ? panel : p) });
  };
  const addPanel = (type: PanelType) => {
    const p: Panel = {
      id: rid(), type,
      title: type === 'row' ? 'New row' : `New ${type}`,
      // Row markers always span the full grid; everything else defaults
      // to half-width and the user can resize via the editor.
      width: type === 'row' ? 4 : 2,
      config: defaultConfig(type),
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
    navigate('/dashboards');
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

        {/* Grafana-style variables bar — only renders when the
            dashboard declares variables. Each variable's selection
            persists in the URL as ?<name>=<value> and the renderer
            substitutes ${name} into panel DSLs / service / groupBy. */}
        {!editing && variables.length > 0 && (
          <VariablesBar
            variables={variables}
            values={varValues}
            onChange={(k, v) => setVarValues(prev => ({ ...prev, [k]: v }))}
          />
        )}

        {panels.length === 0 ? (
          <Empty icon="◫" title="No panels yet">
            {editing ? 'Use "+ Add panel" above to start building.'
                     : 'Click Edit to add panels.'}
          </Empty>
        ) : (
          <DashboardGrid
            panels={panels}
            range={range}
            vars={varValues}
            editing={editing}
            onEditPanel={setEditingPanel}
            onDeletePanel={deletePanel}
            dashboardId={id} />
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
          {(['row', 'metric', 'spanmetric', 'stat', 'markdown'] as PanelType[]).map(t => (
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

// Grafana-style row layout: panels of type 'row' act as collapsible
// section headers. All non-row panels following a row marker (until
// the next row) belong to that row's grid. Panels before any row
// marker form an implicit "default" row at the top.
//
// Per-row collapse state is local component state, keyed by panel id —
// not persisted across reloads (matches Grafana's default behaviour;
// add a localStorage layer if users start asking for it).
function DashboardGrid({
  panels, range, vars, editing, onEditPanel, onDeletePanel, dashboardId,
}: {
  panels: Panel[];
  range: TimeRange;
  vars?: Record<string, string>;
  editing: boolean;
  onEditPanel: (id: string) => void;
  onDeletePanel: (id: string) => void;
  // Cursor-sync key passed to every chart panel — every chart on
  // the dashboard hovers in lockstep so the operator reads 8
  // panels as one view.
  dashboardId: string;
}) {
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());

  // Bucket panels into row groups.
  type RowGroup = { rowPanel: Panel | null; key: string; panels: Panel[] };
  const groups: RowGroup[] = [];
  let cur: RowGroup = { rowPanel: null, key: '__head', panels: [] };
  groups.push(cur);
  for (const p of panels) {
    if (p.type === 'row') {
      cur = { rowPanel: p, key: p.id, panels: [] };
      groups.push(cur);
    } else {
      cur.panels.push(p);
    }
  }
  // Drop the implicit head if it ended up empty (i.e. the dashboard
  // starts with an explicit row).
  const visible = groups.filter(g => g.rowPanel || g.panels.length > 0);

  return (
    <div>
      {visible.map(g => {
        const isCollapsed = g.rowPanel ? collapsed.has(g.rowPanel.id) : false;
        return (
          <div key={g.key} style={{ marginBottom: 14 }}>
            {g.rowPanel && (
              <div className="dash-row-header"
                   onClick={() => {
                     if (!g.rowPanel) return;
                     const next = new Set(collapsed);
                     next.has(g.rowPanel.id) ? next.delete(g.rowPanel.id) : next.add(g.rowPanel.id);
                     setCollapsed(next);
                   }}>
                <span className="dash-row-toggle">{isCollapsed ? '▶' : '▼'}</span>
                <span className="dash-row-title">{g.rowPanel.title || 'Row'}</span>
                <span className="dash-row-count">
                  {g.panels.length} panel{g.panels.length === 1 ? '' : 's'}
                </span>
                {editing && (
                  <span style={{ marginLeft: 8, display: 'flex', gap: 4 }} onClick={e => e.stopPropagation()}>
                    <button className="sec" onClick={() => g.rowPanel && onEditPanel(g.rowPanel.id)}
                      style={{ padding: '2px 7px', fontSize: 11 }}>Edit</button>
                    <button className="sec" onClick={() => g.rowPanel && onDeletePanel(g.rowPanel.id)}
                      style={{ padding: '2px 7px', fontSize: 11, color: 'var(--err)' }}>×</button>
                  </span>
                )}
              </div>
            )}
            {!isCollapsed && g.panels.length > 0 && (
              <div style={{
                display: 'grid', gridTemplateColumns: 'repeat(4, 1fr)', gap: 12,
                marginTop: g.rowPanel ? 8 : 0,
              }}>
                {g.panels.map(p => (
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
                          <button className="sec" onClick={() => onEditPanel(p.id)}
                            style={{ padding: '2px 7px', fontSize: 11 }}>Edit</button>
                          <button className="sec" onClick={() => onDeletePanel(p.id)}
                            style={{ padding: '2px 7px', fontSize: 11, color: 'var(--err)' }}>×</button>
                        </span>
                      )}
                    </div>
                    <PanelRenderer panel={p} range={range} vars={vars}
                                   syncKey={`dashboard:${dashboardId}`} />
                  </div>
                ))}
              </div>
            )}
          </div>
        );
      })}
    </div>
  );
}
