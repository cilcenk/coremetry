import { Suspense, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useEscLayer } from '@/lib/escLayer';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { useQueryClient } from '@tanstack/react-query';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { Button, IconButton, MenuItem } from '@/components/ui';
import { useAuth } from '@/components/AuthProvider';
import { PanelRenderer, applyVarsToMetric, applyVarsToSpan, type PanelDataOverride } from '@/components/dashboard/PanelRenderer';
import { PanelEditor, defaultConfig } from '@/components/dashboard/PanelEditor';
import { VariableEditor } from '@/components/dashboard/VariableEditor';
import { VariablesBar } from '@/components/dashboard/VariablesBar';
import type { DashboardVariable } from '@/lib/types';
import { api } from '@/lib/api';
import { raceGuard } from '@/lib/raceGuard';
import { usePageZoomRange } from '@/lib/chart/usePageZoomRange';
import { useContentWidth } from '@/lib/useContentWidth';
import { quantizeWidth } from '@/lib/chartStep';
import { effectivePanelStep, estimatePanelPx } from '@/components/dashboard/panelStep';
import type {
  Dashboard, Panel, PanelType, TimeRange,
  MetricPanelConfig, SpanMetricPanelConfig,
} from '@/lib/types';
import { timeRangeToNs } from '@/lib/utils';
import {
  DASHBOARD_RESERVED_PARAMS, isDashboardVariableParam,
  parseRefreshParam, refreshLabel, REFRESH_CHOICES,
} from '@/lib/dashboardUrl';
import { serializeDashboard, suggestedFilename } from '@/lib/dashboardIO';
import { panelToExploreHref } from '@/pages/explore/panelToExplore';
import { PageShell } from '@/components/ui/PageShell';

// Wrapper handles the Suspense requirement of useSearchParams() in App
// Router with static export.
export default function DashboardPage() {
  return <Suspense fallback={<Spinner />}><Inner /></Suspense>;
}

function Inner() {
  const [sp, setSp] = useSearchParams();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const { user } = useAuth();
  const id = sp.get('id') ?? '';
  const startInEdit = sp.get('edit') === '1';

  // v0.9.429 — zoom-yığını deseni paylaşılan usePageZoomRange hook'unda
  // (Dynatrace-style drag-to-zoom: her panel global range'i yazar, tüm
  // paneller yeni pencereyle refetch; çift-tık bir adım geri).
  const { range, setRange, handleZoom, handleZoomReset } = usePageZoomRange('30m');
  const [doc, setDoc] = useState<Dashboard | null | undefined>(undefined);
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState<Dashboard | null>(null);
  const [editingPanel, setEditingPanel] = useState<string | null>(null); // panel id
  // v0.9.780 — etiket girdisinin HAM metni. null = dokunulmadı (değer
  // draft.tags'ten türetilir). Ayrı tutulmasının nedeni edit alanının
  // yanında açıklanıyor: türetilmiş değer virgülü yutuyor.
  const [tagsInput, setTagsInput] = useState<string | null>(null);
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
      if (!isDashboardVariableParam(k)) return;
      init[k] = v;
    });
    return init;
  });

  // ── Auto-refresh + kiosk (v0.9.779) ───────────────────────────────
  // İkisi de ADRESTE yaşıyor: paylaşılan link aynı yenileme aralığını
  // ve aynı TV görünümünü açar. Yazım react-router üzerinden (aşağıdaki
  // değişken aynası ham replaceState kullandığı için router'ın kendi
  // location'ı bayat kalabiliyor; kiosk'u AppShell okuyacağından yazımın
  // router'a GÖRÜNMESİ şart).
  const refreshSec = parseRefreshParam(sp.get('refresh'));
  const kiosk = sp.get('kiosk') === '1';
  const setRefreshSec = useCallback((sec: number) => {
    setSp(prev => {
      const next = new URLSearchParams(prev);
      if (sec > 0) next.set('refresh', String(sec)); else next.delete('refresh');
      return next;
    }, { replace: true });
  }, [setSp]);
  const setKiosk = useCallback((on: boolean) => {
    setSp(prev => {
      const next = new URLSearchParams(prev);
      if (on) next.set('kiosk', '1'); else next.delete('kiosk');
      return next;
    }, { replace: true });
  }, [setSp]);

  // refreshTick — auto-refresh'in TEK mekaniği (Clusters v0.9.43 emsali).
  //
  // Neden sayaç, neden setRange DEĞİL: bir preset range'in ('30m')
  // kimliği ASLA değişmez (useUrlRange ham string üzerinden memo'lar),
  // dolayısıyla tick olmadan hiçbir fetch effect'i yeniden koşmaz —
  // "yeniliyorum" diyen ama hiçbir şey istemeyen bir düğme kalırdı.
  // setRange yazmak ise zoom geri-yığınını siler (usePageZoomRange),
  // adresi kirletir ve her panelin sunucu cache anahtarını çevirir.
  // Sayaç bunların hiçbirini yapmaz: yalnızca effect'leri tekrar koşturur
  // ve timeRangeToNs(range) o an yeniden hesaplandığı için pencere de
  // kendiliğinden ilerler.
  //
  // Düzenleme modunda ve custom (zoom'lanmış) pencerede KAPALI: birincisi
  // kaydedilmemiş taslağın üstüne veri çeker, ikincisi operatörün elle
  // seçtiği sabit pencereyi tazelemenin bir anlamı yok.
  const [refreshTick, setRefreshTick] = useState(0);
  useEffect(() => {
    if (!refreshSec || editing || range.preset === 'custom') return;
    const id = setInterval(() => {
      if (!document.hidden) setRefreshTick(t => t + 1); // gizli sekmede istek yok
    }, refreshSec * 1000);
    return () => clearInterval(id);
  }, [refreshSec, editing, range.preset]);

  // Kiosk'tan ESC ile çıkış (PanelMenu / CorePanel klavye sözleşmesinin
  // aynısı) — sidebar gizliyken görünür ✕ tek çıkış olmasın.
  // v0.9.950 (E2/Ö28) — KATMAN. Kiosk EN ALTTAKİ katman olmalı: kiosk
  // içinde açılan bir panel menüsü/modal ilk Esc'i alır, yoksa operatör
  // menüyü kapatmak isterken tam ekrandan da düşerdi.
  useEscLayer(kiosk, () => setKiosk(false));

  // v0.9.857 (UX denetimi K7) — Trace.tsx ile aynı yarış deseni: cleanup'sız
  // manuel fetch adası. Düşük frekanslı ama aynı sınıf; iki pano arasında
  // hızlı geçişte eski pano yenisinin üstüne yazabilirdi.
  useEffect(() => {
    if (!id) return;
    setDoc(undefined);
    const g = raceGuard();
    api.getDashboard(id, g.signal).then(d => {
      if (!g.ok()) return;
      setDoc(d);
      // panels arrives as JSON-encoded string on the wire (json.RawMessage),
      // normalize it to an array for our local state.
      const panels = normalizePanels(d.panels);
      setDraft({ ...d, panels });
      if (startInEdit && user?.role === 'admin') setEditing(true);
    }).catch(() => { if (g.ok()) setDoc(null); });
    return g.cancel;
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
  //
  // v0.9.779 — rezerve set artık DASHBOARD_RESERVED_PARAMS (testli, tek
  // yer). 'refresh' / 'kiosk' listeye girmeseydi bu effect onları her
  // yazımda silerdi: auto-refresh açılır, bir sonraki render'da sessizce
  // kapanırdı.
  //
  // `sp` de bağımlılıkta: bu ayna ham replaceState yazıyor, yani
  // react-router'ın location'ı onu GÖRMÜYOR. Router üzerinden bir yazım
  // olduğunda (Topbar'ın range seçimi, aşağıdaki refresh/kiosk yazımları)
  // router'ın bayat snapshot'ı değişken paramlarını adresten düşürüyordu;
  // sp değişince ayna tekrar koşup onları geri koyar.
  useEffect(() => {
    const url = new URL(window.location.href);
    for (const key of Array.from(url.searchParams.keys())) {
      if (!DASHBOARD_RESERVED_PARAMS.has(key)) url.searchParams.delete(key);
    }
    for (const [k, v] of Object.entries(varValues)) {
      if (v) url.searchParams.set(k, v);
    }
    window.history.replaceState({}, '', url.toString());
  }, [varValues, sp]);

  // Parsed variable definitions (live from the dashboard doc) drive
  // both the picker bar and what gets substituted into panels. Same
  // Rules-of-Hooks reasoning as the URL mirror — declared before any
  // conditional returns.
  // v0.9.759 — düzenleme modunda değişken tanımları düzenlenebilir;
  // null = dokunulmadı (doc'unki geçerli). Save effVariables'ı yazar.
  const [varDefs, setVarDefs] = useState<DashboardVariable[] | null>(null);
  const variables: DashboardVariable[] = useMemo(() => {
    const raw = doc?.variables;
    if (!raw) return [];
    if (Array.isArray(raw)) return raw as DashboardVariable[];
    try {
      const parsed = JSON.parse(raw as unknown as string);
      return Array.isArray(parsed) ? parsed : [];
    } catch { return []; }
  }, [doc?.variables]);
  const effVariables = varDefs ?? variables;

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
    // v0.9.779 — auto-refresh tetikleyicisi. Sadece bağımlılık; range
    // preset'te sabit kimlikli olduğu için tick olmadan bu effect
    // yeniden koşmaz. timeRangeToNs aşağıda YENİDEN hesaplandığından
    // pencere de her tick'te ilerler.
    void refreshTick;
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
  }, [bundleablePanels, range, varValues, contentW, refreshTick]);

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
  // v0.9.773 — duplicate a panel in place (Grafana's "Copy" on the panel
  // menu). The copy lands immediately AFTER its source so the operator sees
  // it without hunting the bottom of the board.
  //
  // Two details that would be bugs if missed:
  //   • structuredClone, not a spread. Panel.config carries nested objects
  //     (stat/gauge thresholds, the metric/span sub-config); a shallow copy
  //     would leave the two panels sharing one thresholds array, so editing
  //     the copy would silently rewrite the original.
  //   • enter edit mode. `draft` is only persisted by Save, which only
  //     exists in edit mode — duplicating from the view mode would look
  //     like it worked and vanish on reload. Flipping to editing makes the
  //     unsaved state visible and saveable.
  const duplicatePanel = (pid: string) => {
    const idx = panels.findIndex(p => p.id === pid);
    if (idx < 0) return;
    const copy: Panel = {
      ...structuredClone(panels[idx]),
      id: rid(),
      title: `${panels[idx].title} (kopya)`,
    };
    const next = [...panels];
    next.splice(idx + 1, 0, copy);
    if (!editing) setEditing(true);
    setDraft({ ...draft, panels: next });
  };
  // Edit requested from the panel menu — reachable in view mode, so it has
  // to open edit mode first (the editor writes into `draft`).
  const requestEditPanel = (pid: string) => {
    if (!editing) setEditing(true);
    setEditingPanel(pid);
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
        // v0.9.759 — değişken tanımları da kaydedilir (backend merge
        // semantiği: alan yoksa korunur, açık boş liste boşaltır).
        variables: effVariables,
        // v0.9.780 — etiketler. Aynı merge semantiği: burada AÇIKÇA
        // gönderiliyor, yani operatör hepsini silerse ([]) boş kalır.
        tags: draft.tags ?? [],
      });
      setDoc({ ...updated, panels: normalizePanels(updated.panels) });
      setVarDefs(null);
      setTagsInput(null);
      // v0.9.780 — /dashboards listesi ad/açıklama/etiket gösteriyor ve
      // 60s staleTime ile duruyor; geçersiz kılmazsak operatör bir
      // etiketi kaydedip listeye dönünce eski hâlini görürdü. ['dashboards']
      // ön-eki hem 'list' hem PinToDashboardModal'ın anahtarını kapsıyor.
      void qc.invalidateQueries({ queryKey: ['dashboards'] });
      setEditing(false);
    } finally {
      setBusy(false);
    }
  };
  const cancel = () => {
    setDraft({ ...doc, panels: normalizePanels(doc.panels) });
    setVarDefs(null); // v0.9.759 — düzenlenen değişkenler de geri alınır
    setTagsInput(null); // v0.9.780 — etiket girdisi de taslakla birlikte
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
    const json = serializeDashboard({ ...draft, panels, variables: effVariables });
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
      {/* Kiosk çıkışı — sidebar gizliyken tek görünür çıkış. ESC de
          çalışır (yukarıdaki dinleyici), ama görünmeyen bir kısayol tek
          çıkış olamaz: bir TV'de kimse ESC'i bilmiyor. */}
      {kiosk && (
        <button className="sec" type="button" onClick={() => setKiosk(false)}
          title="Kiosk modundan çık (ESC)"
          style={{
            position: 'fixed', top: 10, right: 12, zIndex: 'var(--z-fab)',
            fontSize: 11, padding: '3px 8px', borderRadius: 'var(--radius-sm)',
          }}>
          Kiosk'tan çık ✕
        </button>
      )}
      <PageShell>
        <div className="controls" style={{ marginBottom: 14 }}>
          {editing ? (
            <>
              <input value={draft.name} placeholder="Dashboard name" aria-label="Dashboard name"
                onChange={e => setDraft({ ...draft, name: e.target.value })}
                style={{ width: 220 }} />
              <input value={draft.description} placeholder="Description" aria-label="Dashboard description"
                onChange={e => setDraft({ ...draft, description: e.target.value })}
                style={{ width: 320 }} />
              {/* v0.9.780 — etiketler. Virgülle ayrılmış düz metin:
                  bir rozet-editörü kurmak yerine yazmayı serbest
                  bırakıyoruz; etiket kümesi kapalı değil ve operatör
                  zaten kendi sözlüğünü kuruyor. Ayrıştırma kaydederken
                  değil YAZARKEN yapılıyor ki draft tek doğru olsun. */}
              <input value={tagsInput ?? (draft.tags ?? []).join(', ')}
                placeholder="Etiketler (virgülle)" aria-label="Dashboard tags"
                title="Virgülle ayrılmış etiketler — panolar listesinde görünür ve aranabilir"
                onChange={e => {
                  // HAM metin ayrı tutuluyor: gösterilen değeri
                  // ayrıştırılmış diziden türetseydik, yazılan virgül
                  // (ve ardındaki boşluk) filtreden düşer ve operatör
                  // ikinci etiketi hiç yazamazdı.
                  setTagsInput(e.target.value);
                  setDraft({
                    ...draft,
                    tags: e.target.value.split(',').map(t => t.trim()).filter(Boolean),
                  });
                }}
                style={{ width: 220 }} />
              <AddPanelMenu onAdd={addPanel} />
              <span style={{ marginLeft: 'auto' }} />
              <Button variant="secondary" onClick={cancel}>Cancel</Button>
              <Button variant="primary" onClick={save} loading={busy}>Save</Button>
            </>
          ) : (
            <>
              {draft.description && (
                <span style={{ color: 'var(--text2)', fontSize: 12 }}>{draft.description}</span>
              )}
              <span style={{ marginLeft: 'auto' }} />
              {/* v0.9.779 — auto-refresh + TV modu. Topbar'a DOKUNULMADI
                  (49 dosya import ediyor); ikisi de bu sayfanın kendi
                  şeridinde yaşıyor. */}
              <RefreshControl seconds={refreshSec} onChange={setRefreshSec}
                disabled={range.preset === 'custom'} />
              <Button variant="secondary" size="sm" onClick={() => setKiosk(true)}
                title="TV/kiosk modu — sol menüyü gizler, panoyu tam genişliğe açar. ESC ile çıkılır.">
                ⛶ TV
              </Button>
              {/* Export is read-only → available to every role so a
                  viewer can grab a board to share / version. */}
              <Button variant="secondary" onClick={exportDashboard}
                title="Download this dashboard as a portable JSON file">↓ Export JSON</Button>
              {isAdmin && (
                <>
                  {/* v0.9.1006 (M4/O6 + K5) — iki değişiklik: dolu
                      kırmızı Delete, dolu mavi Edit'in yanında ikinci bir
                      ağırlıktı (`ghost-danger`a indi) ve YIKICI OLAN
                      SOLDAYDI, yani kas hafızasının birincil eylemi
                      beklediği yerde. Sıra da düzeltildi. */}
                  <Button variant="primary" onClick={() => setEditing(true)}>Edit</Button>
                  <Button variant="ghost-danger" onClick={removeDashboard}>Delete</Button>
                </>
              )}
            </>
          )}
        </div>

        {editing && (
          <VariableEditor value={effVariables} onChange={setVarDefs} />
        )}
        {/* Grafana-style variables bar — only renders when the
            dashboard declares variables. Each variable's selection
            persists in the URL as ?<name>=<value> and the renderer
            substitutes ${name} into panel DSLs / service / groupBy. */}
        {!editing && effVariables.length > 0 && (
          <VariablesBar
            variables={effVariables}
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
            canEdit={isAdmin}
            onEditPanel={setEditingPanel}
            onEditRequest={requestEditPanel}
            onDuplicatePanel={duplicatePanel}
            onDeletePanel={deletePanel}
            onMovePanel={movePanel}
            onZoom={handleZoom}
            onZoomReset={handleZoomReset}
            dashboardId={id}
            refreshTick={refreshTick}
            bundlePanelData={bundlePanelData} />
        )}

        {editingPanelObj && (
          <PanelEditor panel={editingPanelObj}
            onChange={updatePanel}
            onClose={() => setEditingPanel(null)}
            onDelete={() => deletePanel(editingPanelObj.id)} />
        )}
      </PageShell>
    </>
  );
}

// RefreshControl (v0.9.779) — auto-refresh anahtarı + aralık seçimi.
//
// Görsel dil Clusters'ın LiveToggle'ı (v0.9.38): ikincil buton + nabız
// noktası. İki parça bilinçli: buton HIZLI aç/kapa (operatör aralığı
// zaten seçmiştir), select aralığı değiştirir.
//
// Etiket dürüstlüğü (v0.9.43 dersi): burada "Live" YAZMIYOR. Panel
// verisinin efektif tazeliği sunucu cache TTL'ine bağlı; 30s'de bir
// sormak 30s'lik veri garantisi vermez. Buton "Auto" der, ayrıntı
// title'da durur.
function RefreshControl({ seconds, onChange, disabled }: {
  seconds: number;
  onChange: (sec: number) => void;
  disabled?: boolean;
}) {
  const on = seconds > 0;
  const why = disabled
    ? 'Elle seçilmiş (zoom/custom) pencerede auto-refresh kapalı — sabit bir aralığı tazelemenin karşılığı yok.'
    : on
      ? `Auto-refresh açık — her ${refreshLabel(seconds)} panel verileri yeniden istenir. `
        + 'Panel verileri sunucu cache tazeliğine tabidir; gizli sekmede döngü durur. Kapatmak için tıkla.'
      : 'Auto-refresh kapalı — açmak için tıkla.';
  return (
    <span className="row gap-1">
      <Button variant="secondary" size="sm" disabled={disabled}
        onClick={() => onChange(on ? 0 : 60)}
        title={why}
        style={{ whiteSpace: 'nowrap' }}
        leftIcon={<span className={on && !disabled ? 'pulse-dot' : ''} style={{
          display: 'inline-block', width: 8, height: 8, borderRadius: '50%',
          background: on && !disabled ? 'var(--ok)' : 'var(--text3)',
        }} />}>
        {on ? 'Auto' : 'Paused'}
      </Button>
      <select value={String(seconds)} disabled={disabled}
        aria-label="Auto-refresh aralığı" title={why}
        onChange={e => onChange(Number(e.target.value))}
        style={{ fontSize: 11, padding: '2px 4px', width: 84 }}>
        {REFRESH_CHOICES.map(sec => (
          <option key={sec} value={String(sec)}>{refreshLabel(sec)}</option>
        ))}
      </select>
    </span>
  );
}

// PanelMenu (v0.9.773) — the ⋯ on a dashboard panel's TITLE row.
//
// Deliberately NOT a second chart menu. Metric / spanmetric / promql
// panels are drawn by CorePanel, which already owns a ⋯
// for chart-level actions (fullscreen / CSV / show query). This one carries
// PANEL-level actions only — things about the tile, not about the plot — so
// the two never overlap and the operator learns one rule: outer menu = the
// panel, inner menu = the picture.
//
// Delete stays OUT. It lives on the edit-mode × where an accidental click is
// already guarded by having entered edit mode on purpose; promoting it into
// a hover menu that a viewer can also open is how boards lose panels.
function PanelMenu({ panel, vars, range, canEdit, onDuplicate, onEdit }: {
  panel: Panel;
  vars?: Record<string, string>;
  range: TimeRange;
  canEdit: boolean;
  onDuplicate: (id: string) => void;
  onEdit: (id: string) => void;
}) {
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLSpanElement | null>(null);

  // A panel's own range override wins — Explore should open the window the
  // operator is actually looking at, not the dashboard's.
  const exploreHref = panelToExploreHref(panel, vars, panel.rangeOverride ?? range);

  // Same keyboard contract CorePanel learned in v0.9.711: promising
  // role="menu" without ESC and outside-click is lying to a screen reader.
  // v0.9.950 (E2/Ö28) — menü kendi katmanı; kiosk'un ÜSTÜNDE açılırsa ilk
  // Esc menüyü kapatır (LIFO), kiosk'tan düşürmez.
  useEscLayer(open, () => setOpen(false));
  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    window.addEventListener('mousedown', onDown);
    return () => window.removeEventListener('mousedown', onDown);
  }, [open]);

  // A menu with nothing in it is worse than no menu: a viewer on a stat panel
  // has neither a convertible query nor edit rights.
  if (!exploreHref && !canEdit) return null;

  const item = (label: string, onClick: () => void) => (
    <MenuItem key={label} onClick={() => { setOpen(false); onClick(); }}>
      {label}
    </MenuItem>
  );

  return (
    <span ref={ref} style={{ position: 'relative', display: 'inline-flex' }}
      // The card is draggable in edit mode; a press on the menu must not
      // start a drag (and must not be read as an outside-click either).
      draggable={false}
      onMouseDown={e => e.stopPropagation()}>
      <IconButton className="dash-panel-menu-btn"
        variant="secondary" size="sm"
        aria-label="Panel menüsü" aria-haspopup="menu" aria-expanded={open}
        title="Panel actions"
        onClick={() => setOpen(o => !o)}
        icon="⋯" />
      {open && (
        <div role="menu" style={{
          position: 'absolute', top: '100%', right: 0, marginTop: 4,
          minWidth: 188, background: 'var(--bg2)',
          border: '1px solid var(--border)', borderRadius: 'var(--radius-sm)',
          boxShadow: 'var(--shadow-pop)', padding: 4, zIndex: 'var(--z-dropdown)',
          display: 'flex', flexDirection: 'column', gap: 2,
        }}>
          {/* Absent for panel shapes the builder can't express (stat, gauge,
              markdown, heatmap, raw PromQL) — panelToExploreHref returns null
              rather than opening a query that isn't this panel's. */}
          {exploreHref && item("↗ Explore'da aç", () => navigate(exploreHref))}
          {canEdit && item('⧉ Çoğalt', () => onDuplicate(panel.id))}
          {canEdit && item('✎ Düzenle', () => onEdit(panel.id))}
        </div>
      )}
    </span>
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
    heatmap: 'Heatmap (latency density)',
    promql: 'PromQL query',
    topn: 'Top-N bar',
    markdown: 'Markdown / notes',
  };
  return (
    <div style={{ position: 'relative' }}>
      <Button variant="secondary" onClick={() => setOpen(o => !o)}>+ Add panel</Button>
      {open && (
        <div style={{
          position: 'absolute', top: '100%', left: 0, marginTop: 4,
          background: 'var(--bg1)', border: '1px solid var(--border)',
          borderRadius: 6, padding: 4, zIndex: 'var(--z-dropdown)', minWidth: 180,
          boxShadow: 'var(--shadow-pop)',
        }}>
          {/* v0.9.781 — this list is a HAND-MAINTAINED array behind an
              `as PanelType[]` cast, so TypeScript does NOT check it against
              the union: gauge and heatmap both shipped renderable but
              unreachable because only the `labels` record above was updated.
              Any new type goes in BOTH places. */}
          {(['row', 'metric', 'spanmetric', 'promql', 'topn', 'stat', 'gauge', 'heatmap', 'markdown'] as PanelType[]).map(t => (
            <MenuItem key={t} onClick={() => { onAdd(t); setOpen(false); }}>
              {labels[t]}
            </MenuItem>
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
  panels, range, vars, editing, canEdit, onEditPanel, onEditRequest, onDuplicatePanel,
  onDeletePanel, onMovePanel, onZoom, onZoomReset, dashboardId, refreshTick,
  bundlePanelData,
}: {
  panels: Panel[];
  range: TimeRange;
  vars?: Record<string, string>;
  editing: boolean;
  // Whether this operator may mutate the board (admin/editor). A viewer
  // still gets the menu — "open in Explore" is a read — but not the
  // mutating half of it.
  canEdit: boolean;
  onEditPanel: (id: string) => void;
  // v0.9.773 — edit asked for from the panel menu, which is reachable in
  // VIEW mode; the parent flips into editing first. onEditPanel stays the
  // already-editing path (the inline Edit button).
  onEditRequest: (id: string) => void;
  onDuplicatePanel: (id: string) => void;
  onDeletePanel: (id: string) => void;
  onMovePanel: (srcId: string, targetId: string) => void;
  // onZoom propagates a chart's drag-to-zoom selection up to
  // the dashboard so the rest of the panels re-fetch for the
  // new range. Receives unix-seconds bounds, parent owns the
  // TimeRange state.
  onZoom?: (fromUnixSec: number, toUnixSec: number) => void;
  // Grafana-parite M1 — çift-tık: dashboard zoom geri-yığınını pop eder.
  onZoomReset?: () => void;
  // Cursor-sync key passed to every chart panel — every chart on
  // the dashboard hovers in lockstep so the operator reads 8
  // panels as one view.
  dashboardId: string;
  // v0.9.779 — auto-refresh sayacı. Bundle'ı tazelemek YETMEZ: stat /
  // gauge / heatmap / promql panelleri (ve builder dışı promql modundaki
  // metric paneli) kendi fetch'lerini yapıyor. Yalnız bundle'ı yenilemek
  // donmuş stat kutuları bırakırdı — v0.9.43'te bire bir yaşanan
  // dürüstlük hatası. Sayaç her panel effect'inin bağımlılığına iner.
  refreshTick?: number;
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
              <div className="grid-4" style={{
                display: 'grid', gap: 12,
                marginTop: g.rowPanel ? 8 : 0,
              }}>
                {g.panels.map(p => (
                  <div key={p.id}
                    className="dash-panel"
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
                        {/* v0.9.773 — operator note. A dashboard is scanned,
                            so the text lives in a tooltip, not in the card
                            body where it would push the chart down. */}
                        {p.description && (
                          <i className="dash-panel-info" title={p.description}
                            aria-label={`Panel description: ${p.description}`}>i</i>
                        )}
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
                      <span className="row gap-1">
                        {editing && (
                          <>
                            <Button variant="secondary" size="sm"
                              onClick={() => onEditPanel(p.id)}>Edit</Button>
                            <Button variant="danger" size="sm" title="Delete panel"
                              onClick={() => onDeletePanel(p.id)}>×</Button>
                          </>
                        )}
                        <PanelMenu panel={p} vars={vars} range={range} canEdit={canEdit}
                          onDuplicate={onDuplicatePanel} onEdit={onEditRequest} />
                      </span>
                    </div>
                    <PanelRenderer panel={p} range={range} vars={vars}
                                   syncKey={`dashboard:${dashboardId}`}
                                   onZoom={onZoom}
                                   onZoomReset={onZoomReset}
                                   refreshTick={refreshTick}
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
