import { Suspense, useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { Button } from '@/components/ui';
import { useAuth } from '@/components/AuthProvider';
import { PanelRenderer, applyVarsToMetric, applyVarsToSpan, type PanelDataOverride } from '@/components/dashboard/PanelRenderer';
import { PanelEditor, defaultConfig } from '@/components/dashboard/PanelEditor';
import { VariablesBar } from '@/components/dashboard/VariablesBar';
import type { DashboardVariable } from '@/lib/types';
import { api } from '@/lib/api';
import { useUrlRange } from '@/lib/useUrlRange';
import { useContentWidth } from '@/lib/useContentWidth';
import { quantizeWidth } from '@/lib/chartStep';
import { effectivePanelStep, estimatePanelPx } from '@/components/dashboard/panelStep';
import type {
  Dashboard, Panel, PanelType, TimeRange,
  MetricPanelConfig, SpanMetricPanelConfig,
} from '@/lib/types';
import { timeRangeToNs } from '@/lib/utils';
import { serializeDashboard, suggestedFilename } from '@/lib/dashboardIO';

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

  const [range, setRange] = useUrlRange('30m');
  // Stable drag-to-zoom handler (v0.8.520). Passing a fresh arrow here rode
  // down through every panel to MultiLineChart; the primitive now tracks onZoom
  // by presence, but keeping this referentially stable also spares the panel
  // subtree a needless re-render. setRange is itself stable (useUrlRange) and
  // the closure captures no `range`, so there's no stale-window risk.
  const handleZoom = useCallback((fromUnixSec: number, toUnixSec: number) => {
    // Dynatrace-style click-drag-to-zoom: dragging a horizontal range on any
    // panel updates the dashboard's time range so every panel re-fetches for
    // the new window. Without this the chart visually zoomed but the metric
    // queries still covered the full original window — confusing.
    setRange({
      preset: 'custom',
      fromMs: Math.round(fromUnixSec * 1000),
      toMs: Math.round(toUnixSec * 1000),
    });
  }, [setRange]);
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

  // Bundled panel data — v0.5.81 perf nudge. Instead of every
  // metric / spanmetric panel firing its own /api/{metrics,
  // spans}/metric round trip on mount, the dashboard page
  // fires a single POST /api/dashboards/data carrying ALL the
  // panel queries; the server fans them out to CH in parallel
  // goroutines and returns the results keyed by panel id. Each
  // PanelRenderer reads its slot via the dataOverride prop and
  // skips its own fetch. Browser concurrency cap stops mattering;
  // server-side wall-clock = max(panel queries) instead of sum.
  //
  // Falls back gracefully — when bundlePanelData has no entry
  // for a panel (e.g. a panel added mid-edit) the renderer
  // does its own fetch.
  const [bundlePanelData, setBundlePanelData] = useState<Record<string, PanelDataOverride>>({});
  const bundleablePanels = useMemo(() => {
    const list = doc?.panels;
    if (!list) return [];
    const panels = normalizePanels(list);
    return panels.filter(p => p.type === 'metric' || p.type === 'spanmetric');
  }, [doc?.panels]);
  // GRAN-C (v0.8.248) — quantized #content width for the bundle's
  // width-aware auto step. The bundle builds every panel's query before the
  // panel divs are measurable, so auto-step panels estimate their pixels as
  // content-width × grid-span/4 (estimatePanelPx); the per-panel fallback
  // fetch (PanelRenderer) measures the real div instead. Already bucketed,
  // so it only re-fires the bundle on a 200px bucket crossing. The watch key
  // matters: #content doesn't exist during the early-return spinner, so the
  // hook re-measures once the doc lands and the real layout renders.
  const contentW = useContentWidth(doc != null);
  // Re-fire the bundle whenever the panel set, the time range,
  // or any variable value changes. Each of those re-keys the
  // server-side cache anyway, so we want the bundle aligned.
  useEffect(() => {
    if (bundleablePanels.length === 0) {
      setBundlePanelData({});
      return;
    }
    // Skip when contentW is provably stale: useContentWidth's effect runs
    // just above this one in the same flush (hook declaration order), so a
    // live-DOM mismatch means the corrected bucket is already scheduled and
    // this effect re-fires immediately — skipping avoids a throwaway POST
    // with wrong-width steps on the doc-load commit (where the bundle would
    // otherwise fire against the pre-#content fallback width).
    const live = document.getElementById('content');
    if (live && quantizeWidth(live.clientWidth || 1200) !== contentW) return;
    let cancelled = false;
    const { from, to } = timeRangeToNs(range);
    // Per-panel effective step: operator-pinned cfg.step passes through;
    // auto (step absent — every pre-GRAN-C dashboard) resolves against the
    // panel's estimated width. The backend min-step clamp (v0.8.243) floors
    // fine requests at the metric's export interval.
    const rangeSec = (to - from) / 1e9;
    const stepFor = (cfgStep: number | undefined, p: Panel) =>
      effectivePanelStep(cfgStep, rangeSec, estimatePanelPx(contentW, p.width)) ?? undefined;
    const requests = bundleablePanels.map(p => {
      if (p.type === 'metric') {
        const cfg = applyVarsToMetric(p.config as MetricPanelConfig, varValues);
        return {
          id: p.id, type: 'metric' as const,
          name: cfg.metricName, service: cfg.service,
          agg: cfg.agg,
          groupBy: cfg.groupBy ? cfg.groupBy.split(',').map(s => s.trim()).filter(Boolean) : undefined,
          step: stepFor(cfg.step, p),
          filters: cfg.filters,
        };
      }
      const cfg = applyVarsToSpan(p.config as SpanMetricPanelConfig, varValues);
      return {
        id: p.id, type: 'spanMetric' as const,
        agg: cfg.agg, field: cfg.field,
        groupBy: cfg.groupBy ? cfg.groupBy.split(',').map(s => s.trim()).filter(Boolean) : undefined,
        filters: cfg.filters, dsl: cfg.dsl,
        step: stepFor(cfg.step, p),
      };
    });
    api.dashboardData({ from, to, requests })
      .then(out => { if (!cancelled) setBundlePanelData(out as Record<string, PanelDataOverride>); })
      .catch(() => { if (!cancelled) setBundlePanelData({}); });
    return () => { cancelled = true; };
  }, [bundleablePanels, range, varValues, contentW]);

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
  // Reorder by drop: move srcId to immediately before targetId.
  // No-op when src === target. Used by the drag-and-drop
  // handlers wired below the panel render block.
  const movePanel = (srcId: string, targetId: string) => {
    if (srcId === targetId) return;
    const srcIdx = panels.findIndex(p => p.id === srcId);
    const tgtIdx = panels.findIndex(p => p.id === targetId);
    if (srcIdx < 0 || tgtIdx < 0) return;
    const next = [...panels];
    const [moved] = next.splice(srcIdx, 1);
    // After splicing out src, target index might shift left by 1
    // if src came before it.
    const insertAt = srcIdx < tgtIdx ? tgtIdx - 1 : tgtIdx;
    next.splice(insertAt, 0, moved);
    setDraft({ ...draft, panels: next });
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

  // v0.6.50 — export the dashboard to a JSON file. Read-only, so
  // every role can use it (a viewer exporting a board to share is
  // fine). Builds the portable subset from the already-loaded
  // panels + variables via serializeDashboard; triggers a client-
  // side download with no backend round-trip.
  const exportDashboard = () => {
    if (!draft) return;
    const json = serializeDashboard({ ...draft, panels, variables });
    const blob = new Blob([json], { type: 'application/json' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = suggestedFilename(draft.name);
    a.click();
    URL.revokeObjectURL(url);
  };

  const editingPanelObj = editingPanel ? panels.find(p => p.id === editingPanel) : null;

  return (
    <>
      <Topbar title={draft.name} range={range} onRangeChange={setRange} />
      <div id="content">
        <div className="controls" style={{ marginBottom: 14 }}>
          {editing ? (
            <>
              <input value={draft.name} placeholder="Dashboard name" aria-label="Dashboard name"
                onChange={e => setDraft({ ...draft, name: e.target.value })}
                style={{ width: 220 }} />
              <input value={draft.description} placeholder="Description" aria-label="Dashboard description"
                onChange={e => setDraft({ ...draft, description: e.target.value })}
                style={{ width: 320 }} />
              <AddPanelMenu onAdd={addPanel} />
              <span style={{ marginLeft: 'auto' }} />
              <Button variant="secondary" onClick={cancel}>Cancel</Button>
              <Button onClick={save} loading={busy}>Save</Button>
            </>
          ) : (
            <>
              {draft.description && (
                <span style={{ color: 'var(--text2)', fontSize: 12 }}>{draft.description}</span>
              )}
              <span style={{ marginLeft: 'auto' }} />
              {/* Export is read-only → available to every role so a
                  viewer can grab a board to share / version. */}
              <Button variant="secondary" onClick={exportDashboard}
                title="Download this dashboard as a portable JSON file">↓ Export JSON</Button>
              {isAdmin && (
                <>
                  <Button variant="danger" onClick={removeDashboard}>Delete</Button>
                  <Button onClick={() => setEditing(true)}>Edit</Button>
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
            onMovePanel={movePanel}
            onZoom={handleZoom}
            dashboardId={id}
            bundlePanelData={bundlePanelData} />
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
  const labels: Record<PanelType, string> = {
    row: 'Row (section header)',
    metric: 'Metric (line)',
    spanmetric: 'Span aggregation (line)',
    stat: 'Stat (single value)',
    gauge: 'Gauge',
    markdown: 'Markdown / notes',
  };
  return (
    <div style={{ position: 'relative' }}>
      <Button variant="secondary" onClick={() => setOpen(o => !o)}>+ Add panel</Button>
      {open && (
        <div style={{
          position: 'absolute', top: '100%', left: 0, marginTop: 4,
          background: 'var(--bg1)', border: '1px solid var(--border)',
          borderRadius: 6, padding: 4, zIndex: 50, minWidth: 180,
          boxShadow: 'var(--shadow-pop)',
        }}>
          {(['row', 'metric', 'spanmetric', 'stat', 'markdown'] as PanelType[]).map(t => (
            <button key={t} className="ghost"
              onClick={() => { onAdd(t); setOpen(false); }}
              style={{
                display: 'block', width: '100%', textAlign: 'left',
                borderRadius: 4, fontWeight: 400,
              }}>
              {labels[t]}
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
  panels, range, vars, editing, onEditPanel, onDeletePanel, onMovePanel, onZoom, dashboardId, bundlePanelData,
}: {
  panels: Panel[];
  range: TimeRange;
  vars?: Record<string, string>;
  editing: boolean;
  onEditPanel: (id: string) => void;
  onDeletePanel: (id: string) => void;
  onMovePanel: (srcId: string, targetId: string) => void;
  // onZoom propagates a chart's drag-to-zoom selection up to
  // the dashboard so the rest of the panels re-fetch for the
  // new range. Receives unix-seconds bounds, parent owns the
  // TimeRange state.
  onZoom?: (fromUnixSec: number, toUnixSec: number) => void;
  // Cursor-sync key passed to every chart panel — every chart on
  // the dashboard hovers in lockstep so the operator reads 8
  // panels as one view.
  dashboardId: string;
  // Pre-fetched panel data keyed by panel id (v0.5.81 bundle).
  // Each metric / spanmetric PanelRenderer reads its slot and
  // skips its own fetch. Missing entries fall through to the
  // panel's own fetch path.
  bundlePanelData: Record<string, PanelDataOverride>;
}) {
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());
  // ID of the panel currently being dragged-over so we can render a
  // visual drop indicator. Drag-source id rides on dataTransfer, no
  // need to mirror it into state.
  const [dropTarget, setDropTarget] = useState<string | null>(null);

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
                     if (next.has(g.rowPanel.id)) next.delete(g.rowPanel.id); else next.add(g.rowPanel.id);
                     setCollapsed(next);
                   }}
                   onKeyDown={e => {
                     // Keyboard parity with the click toggle — Enter/Space
                     // collapses/expands the row the same way a click does.
                     if (!g.rowPanel) return;
                     if (e.key === 'Enter' || e.key === ' ') {
                       e.preventDefault();
                       const next = new Set(collapsed);
                       if (next.has(g.rowPanel.id)) next.delete(g.rowPanel.id); else next.add(g.rowPanel.id);
                       setCollapsed(next);
                     }
                   }}
                   role="button"
                   tabIndex={0}
                   aria-expanded={!isCollapsed}>
                <span className="dash-row-toggle">{isCollapsed ? '▶' : '▼'}</span>
                <span className="dash-row-title">{g.rowPanel.title || 'Row'}</span>
                <span className="dash-row-count">
                  {g.panels.length} panel{g.panels.length === 1 ? '' : 's'}
                </span>
                {editing && (
                  <span className="row gap-1" style={{ marginLeft: 8 }} onClick={e => e.stopPropagation()}>
                    <Button variant="secondary" size="sm"
                      onClick={() => g.rowPanel && onEditPanel(g.rowPanel.id)}>Edit</Button>
                    <Button variant="danger" size="sm" title="Delete row"
                      onClick={() => g.rowPanel && onDeletePanel(g.rowPanel.id)}>×</Button>
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
                  <div key={p.id}
                    draggable={editing}
                    onDragStart={e => {
                      if (!editing) return;
                      e.dataTransfer.setData('text/panel-id', p.id);
                      e.dataTransfer.effectAllowed = 'move';
                    }}
                    onDragOver={e => {
                      if (!editing) return;
                      e.preventDefault();
                      e.dataTransfer.dropEffect = 'move';
                      if (dropTarget !== p.id) setDropTarget(p.id);
                    }}
                    onDragLeave={() => {
                      if (dropTarget === p.id) setDropTarget(null);
                    }}
                    onDrop={e => {
                      if (!editing) return;
                      e.preventDefault();
                      const srcId = e.dataTransfer.getData('text/panel-id');
                      setDropTarget(null);
                      if (srcId && srcId !== p.id) onMovePanel(srcId, p.id);
                    }}
                    onDragEnd={() => setDropTarget(null)}
                    style={{
                      gridColumn: `span ${Math.max(1, Math.min(4, p.width))}`,
                      background: 'var(--bg2)',
                      border: dropTarget === p.id
                        ? '1px dashed var(--accent)'
                        : '1px solid var(--border)',
                      borderRadius: 6, padding: 10,
                      position: 'relative',
                      cursor: editing ? 'grab' : 'default',
                      opacity: 1,
                      transition: 'border-color 0.1s',
                    }}>
                    <div className="row-between" style={{
                      marginBottom: 6, fontSize: 12, color: 'var(--text2)',
                    }}>
                      <span className="row gap-2" style={{ minWidth: 0 }}>
                        {editing && (
                          <span title="Drag to reorder"
                            style={{
                              color: 'var(--text3)', fontSize: 14,
                              cursor: 'grab', userSelect: 'none',
                            }}>⋮⋮</span>
                        )}
                        <span style={{
                          fontWeight: 600, color: 'var(--text)',
                          overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                        }}>{p.title}</span>
                        {/* v0.6.20 — range-override indicator. When
                            a panel locks its own window, surface
                            the preset next to the title so the
                            operator doesn't wonder why the chart
                            doesn't move with the Topbar picker.
                            Empty when default (inherit dashboard
                            range) — the page-level Topbar already
                            shows that window. */}
                        {p.rangeOverride?.preset && (
                          <span className="badge b-info mono"
                            title="This panel uses a fixed time range — overrides the dashboard Topbar">
                            ↻ {p.rangeOverride.preset}
                          </span>
                        )}
                      </span>
                      {editing && (
                        <span className="row gap-1">
                          <Button variant="secondary" size="sm"
                            onClick={() => onEditPanel(p.id)}>Edit</Button>
                          <Button variant="danger" size="sm" title="Delete panel"
                            onClick={() => onDeletePanel(p.id)}>×</Button>
                        </span>
                      )}
                    </div>
                    <PanelRenderer panel={p} range={range} vars={vars}
                                   syncKey={`dashboard:${dashboardId}`}
                                   onZoom={onZoom}
                                   dataOverride={bundlePanelData[p.id]} />
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
