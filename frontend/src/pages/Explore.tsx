import { Suspense, useEffect, useMemo, useRef, useState } from 'react';
import type { CSSProperties } from 'react';
import { useLocation, useNavigate, useSearchParams } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { FilterBuilder } from '@/components/FilterBuilder';
import { HeatmapCellExemplars, type HeatmapCellRef } from '@/components/HeatmapCellExemplars';
import { SavedViewsBar } from '@/components/SavedViewsBar';
import { LatencyHeatmap } from '@/components/LatencyHeatmap';
import { FacetsPanel } from '@/components/FacetsPanel';
import { ShareButton } from '@/components/ShareButton';
import { LogsExplorer } from '@/components/LogsExplorer';
import { MetricsExplorer } from '@/components/MetricsExplorer';
import type { ExploreVizKind } from '@/components/ExploreViz';
import { api } from '@/lib/api';
import { timeRangeToNs, fmtNum } from '@/lib/utils';
import { encodeRange, decodeRange, encodeFilters, decodeFilters, buildQuery } from '@/lib/urlState';
import { storedRangeString } from '@/lib/useUrlRange';
import type { TimeRange, FilterExpr, LatencyHeatmap as Heatmap } from '@/lib/types';
import {
  REPEAT_PRESETS, STEP_OPTIONS,
  type ResultMode, type Source,
} from './explore/presets';
import { NLQueryBox } from './explore/NLQueryBox';
import { TracesResult } from './explore/TracesResult';
import { RepeatsResult } from './explore/RepeatsResult';
import { QuestionCards } from './explore/QuestionCards';
import { useQueryHistory } from './explore/useQueryHistory';
import {
  type BuilderState, defaultBuilderState, blankQuery, nextLetter,
  produces, effectiveFilters, builderDesc, MAX_QUERIES,
} from './explore/model';
import { encodeBuilder, seedFromLegacyParams } from './explore/urlCodec';
import { useExploreQueries, useExploreExemplars, useExploreOverlays } from './explore/useExploreQueries';
import { PanelStack, buildPanels } from './explore/PanelStack';
import { GroupTable } from './explore/GroupTable';
import { SummaryViz } from './explore/SummaryViz';
import { QueryRow } from './explore/QueryRow';
import { FormulaRow } from './explore/FormulaRow';
import { VizRail } from './explore/VizRail';
import { SplitByPicker } from './explore/SplitByPicker';

// Explore (explore-v2 Phase 2) — the metric result mode is now the
// multi-query builder: up to four queries A–D (span signals or catalogue
// metrics) + one formula, rendered as a stack of cursor-synced
// TimeSeriesPanels with a combined group table. Builder state rides ?q=
// (compact JSON; urlCodec.ts); every legacy param shape stays decodable
// forever via seedFromLegacyParams (SavedViews + inbound links).
//
// Traces + repeats result modes keep their pre-v2 console (filter zone,
// presets) and URL shapes — those params remain the canonical form there.
// source=metrics / source=logs keep their dedicated panels until the
// Phase-5 /metrics collapse.

// The pristine builder's ?q= encoding — used to suppress the param entirely
// so a paramless /explore keeps showing the entry cards.
const DEFAULT_Q = encodeBuilder(defaultBuilderState());

function ExploreInner() {
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();

  // ── Decode the URL ONCE per mount (the page-level key remounts us on the
  // entry↔workspace boundary; state owns the truth afterwards). ───────────
  const builderSeed = useRef<BuilderState | null | undefined>(undefined);
  if (builderSeed.current === undefined) {
    builderSeed.current = seedFromLegacyParams(searchParams);
  }

  const [source, setSource] = useState<Source>(() => {
    const v = searchParams.get('source');
    return v === 'metrics' || v === 'logs' ? v : 'spans';
  });
  const [resultMode, setResultMode] = useState<ResultMode>(() => {
    const r = searchParams.get('result');
    if (r === 'traces' || r === 'repeats') return r;
    return 'metric';
  });
  const [range, setRange] = useState<TimeRange>(
    () => decodeRange(searchParams.get('range') ?? storedRangeString(), { preset: '30m' }));

  // Legacy viz passthrough for the metrics/logs source panels only — the
  // spans builder has its own ExploreViz inside BuilderState.
  const [legacyViz] = useState<ExploreVizKind>(() => {
    const v = searchParams.get('viz') as ExploreVizKind;
    return ['line', 'bar', 'topN', 'kpi'].includes(v) ? v : 'line';
  });

  // ── Builder state (metric result mode) ───────────────────────────────────
  const [builder, setBuilder] = useState<BuilderState>(
    () => builderSeed.current ?? defaultBuilderState());
  // Debounced copy drives BOTH the fetch fan-out and the ?q= write, so
  // typing in a filter doesn't fire a query per keystroke (plan: 300ms).
  const [debounced, setDebounced] = useState(builder);
  useEffect(() => {
    const t = window.setTimeout(() => setDebounced(builder), 300);
    return () => clearTimeout(t);
  }, [builder]);

  // Ephemeral interaction state — NOT in the URL (plan state model).
  const [zoomWindow, setZoomWindow] = useState<{ from: number; to: number } | null>(null);
  const [hiddenKeys, setHiddenKeys] = useState<Set<string>>(() => new Set());
  const [focusKey, setFocusKey] = useState<string | null>(null);

  // ── Traces / repeats console state (pre-v2, unchanged shapes) ────────────
  const [filters, setFilters] = useState<FilterExpr[]>(
    () => decodeFilters(searchParams.get('filters')));
  const [mode, setMode] = useState<'builder' | 'advanced'>(
    () => (searchParams.get('mode') === 'advanced' ? 'advanced' : 'builder'));
  const [dsl, setDsl] = useState(() => searchParams.get('dsl') ?? '');
  const [queryError, setQueryError] = useState<string | null>(null);
  const [repeatGroupBy, setRepeatGroupBy] = useState<string[]>(
    () => (searchParams.get('groupBy') ?? '').split(',').filter(Boolean));
  const [repeatMin, setRepeatMin] = useState(
    () => parseInt(searchParams.get('minRepeats') ?? '5', 10) || 5);
  const [repeats, setRepeats] = useState<import('@/lib/types').RepeatedSpanRow[] | null | undefined>(undefined);
  const [traces, setTraces] = useState<import('@/lib/types').TraceRow[] | null | undefined>(undefined);
  const [traceTotal, setTraceTotal] = useState(0);
  const [traceLimit, setTraceLimit] = useState(
    () => parseInt(searchParams.get('limit') ?? '50', 10) || 50);
  const [extraCols, setExtraCols] = useState<string[]>(
    () => (searchParams.get('cols') ?? '').split(',').map(s => s.trim()).filter(Boolean));

  // Facets panel — traces mode only (D5). Persisted preference.
  const [showFacets, setShowFacets] = useState(() => {
    if (typeof window === 'undefined') return true;
    return localStorage.getItem('coremetry-explore-facets') !== '0';
  });
  useEffect(() => {
    try { localStorage.setItem('coremetry-explore-facets', showFacets ? '1' : '0'); }
    catch { /* ignore */ }
  }, [showFacets]);

  const [services, setServices] = useState<string[]>([]);
  const [heatmap, setHeatmap] = useState<Heatmap | null | undefined>(undefined);
  const [cellExemplar, setCellExemplar] = useState<HeatmapCellRef | null>(null);

  const exploreRange = useMemo(() => timeRangeToNs(range), [range]);

  // Recent-queries ring + entry-screen gate (Phase-1 behaviour preserved).
  const { history, save: saveHistory } = useQueryHistory();
  const [hasParams, setHasParams] = useState(() => hasMeaningfulParams(searchParams));
  const seedNextRef = useRef<string | null>(null);

  // ── State → URL ───────────────────────────────────────────────────────────
  // Metric mode writes the canonical ?q=; traces/repeats and the
  // metrics/logs panels keep writing their legacy shapes (those params ARE
  // the canonical form for those surfaces). replace keeps history clean.
  useEffect(() => {
    let queryEntries: Array<[string, string | number | undefined | null | false]>;
    if (source !== 'spans') {
      queryEntries = [
        ['source', source],
        ['viz', legacyViz !== 'line' ? legacyViz : ''],
        // service/metric passthrough for ServiceInfra deep links — the
        // panels read them at mount; keep them while the panel is up.
        ['service', searchParams.get('service') ?? ''],
        ['metric', searchParams.get('metric') ?? ''],
      ];
    } else if (resultMode === 'metric') {
      // A pristine default builder writes NO params so the paramless
      // /explore entry screen (question cards) survives — the exact
      // old-workspace semantics where all-defaults produced an empty qs.
      const enc = encodeBuilder(debounced);
      queryEntries = [['q', enc !== DEFAULT_Q ? enc : '']];
    } else {
      queryEntries = [
        ['result',  resultMode],
        ['filters', mode === 'builder' ? encodeFilters(filters) : ''],
        ['dsl',     mode === 'advanced' ? dsl : ''],
        ['mode',    mode === 'advanced' ? 'advanced' : ''],
        ['limit',   resultMode === 'traces' && traceLimit !== 50 ? traceLimit : ''],
        ['cols',    resultMode === 'traces' ? extraCols.join(',') : ''],
        ['groupBy', resultMode === 'repeats' ? repeatGroupBy.join(',') : ''],
        ['minRepeats', resultMode === 'repeats' && repeatMin !== 5 ? repeatMin : ''],
      ];
    }
    const queryQs = buildQuery(queryEntries);
    const qs = buildQuery([...queryEntries, ['range', encodeRange(range)]]);
    const next = qs ? `?${qs}` : '';
    if (next !== window.location.search) {
      navigate(`/explore${next}`, { preventScrollReset: true, replace: true });
    }
    const meaningful = queryQs.length > 0;
    setHasParams(meaningful);
    // Seed-skip (Phase-1): the first canonical URL of a mount is the seed a
    // card / deep link / saved view produced — only divergence is recorded.
    if (seedNextRef.current === null) {
      seedNextRef.current = next;
    }
    if (meaningful && next !== seedNextRef.current) {
      const desc = source !== 'spans'
        ? `${source} explorer`
        : resultMode === 'metric'
          ? builderDesc(debounced)
          : legacyHistoryDesc({ resultMode, mode, dsl, filters, repeatMin, repeatGroupBy });
      saveHistory(desc, next);
    }
    // searchParams intentionally omitted: it's only read for the
    // metrics/logs passthrough whose values never change while mounted.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [source, resultMode, debounced, filters, dsl, mode, range, traceLimit, extraCols, repeatMin, repeatGroupBy, legacyViz, navigate, saveHistory]);

  // Service options for the traces/repeats filter suggestions. Gated on
  // hasParams (entry screen fires no workspace fetches — Phase-1 finding).
  useEffect(() => {
    if (!hasParams || source !== 'spans' || resultMode === 'metric') return;
    api.services(timeRangeToNs(range))
      .then(s => setServices((s ?? []).map(x => x.name)))
      .catch(() => setServices([]));
  }, [range, hasParams, source, resultMode]);

  // ── Builder fan-out (react-query; inactive modes pass from=0 → disabled) ──
  const builderActive = hasParams && source === 'spans' && resultMode === 'metric';
  const builderFrom = builderActive && debounced.viz !== 'heatmap' ? exploreRange.from : 0;
  const { byLetter, anyLoading, error: builderError } = useExploreQueries(
    debounced,
    builderFrom,
    exploreRange.to,
  );
  // Phase 3.2 — per-bucket exemplar trace_ids for eligible queries (◆ glyphs).
  const exemplarsByLetter = useExploreExemplars(debounced, builderFrom, exploreRange.to);
  // Phase 3.3 — deploy markers + SLO thresholds for pinned-service queries.
  const overlaysByLetter = useExploreOverlays(debounced, builderFrom, exploreRange.to);
  const panels = useMemo(
    () => buildPanels(debounced, byLetter, exemplarsByLetter, overlaysByLetter),
    [debounced, byLetter, exemplarsByLetter, overlaysByLetter],
  );
  const anyProduces = debounced.queries.some(produces);

  // Heatmap viz — the LatencyHeatmap path, driven by query A (panel header
  // states it). Gated exactly like the pre-v2 heatmap fetch.
  useEffect(() => {
    if (!builderActive || debounced.viz !== 'heatmap') return;
    const a = debounced.queries.find(produces);
    if (!a) { setHeatmap(null); return; }
    setHeatmap(undefined);
    const fs = effectiveFilters(a);
    const { from, to } = exploreRange;
    api.spanHeatmap({
      filters: fs.length ? JSON.stringify(fs) : undefined,
      dsl: a.dsl.trim() || undefined,
      from, to, buckets: 80,
    })
      .then(h => setHeatmap(h ?? null))
      .catch(() => setHeatmap(null));
  }, [builderActive, debounced, exploreRange]);

  // ── Traces / repeats fetches (pre-v2 behaviour, scoped to their modes) ───
  useEffect(() => {
    if (!hasParams || source !== 'spans' || resultMode === 'metric') return;
    setQueryError(null);
    const { from, to } = timeRangeToNs(range);
    const filterArg = mode === 'builder' && filters.length ? JSON.stringify(filters) : undefined;
    const dslArg    = mode === 'advanced' && dsl.trim() ? dsl : undefined;

    if (resultMode === 'traces') {
      setTraces(undefined);
      api.traces({
        filters: filterArg, dsl: dslArg,
        from, to,
        sort: 'time', order: 'desc',
        limit: traceLimit,
        count: 'approx',
        extraAttrs: extraCols.length ? extraCols.join(',') : undefined,
      })
        .then(r => { setTraces(r.traces ?? []); setTraceTotal(r.total ?? 0); })
        .catch(err => {
          setTraces(null);
          const msg = String(err?.message ?? err);
          setQueryError(msg.includes('DSL') ? msg : null);
        });
    } else {
      setRepeats(undefined);
      api.spanRepeats({
        filters: filterArg, dsl: dslArg,
        from, to,
        groupBy: repeatGroupBy.length ? repeatGroupBy : ['db.statement'],
        minRepeats: repeatMin,
      })
        .then(r => setRepeats(r ?? []))
        .catch(err => {
          setRepeats(null);
          const msg = String(err?.message ?? err);
          setQueryError(msg.includes('DSL') ? msg : null);
        });
    }
  }, [resultMode, range, filters, dsl, mode, traceLimit, extraCols, repeatMin, repeatGroupBy, hasParams, source]);

  // ── Builder mutators ──────────────────────────────────────────────────────
  const setQuery = (i: number, q: BuilderState['queries'][number]) =>
    setBuilder(b => ({ ...b, queries: b.queries.map((x, j) => (j === i ? q : x)) }));
  const addQuery = () => setBuilder(b => {
    const l = nextLetter(b.queries);
    return l ? { ...b, queries: [...b.queries, blankQuery(l)] } : b;
  });
  const removeQuery = (i: number) =>
    setBuilder(b => ({ ...b, queries: b.queries.filter((_, j) => j !== i) }));

  // Result-mode switch — entering traces/repeats from the builder carries
  // query A's narrowing along when the legacy console is still empty.
  const switchResultMode = (m: ResultMode) => {
    if (m !== 'metric' && resultMode === 'metric' && filters.length === 0 && !dsl.trim()) {
      const a = builder.queries.find(produces);
      if (a) {
        const fs = effectiveFilters(a);
        if (fs.length) setFilters(fs);
        if (a.dsl.trim()) { setDsl(a.dsl); setMode('advanced'); }
      }
    }
    setResultMode(m);
  };

  const toggleHidden = (rowKey: string) => setHiddenKeys(prev => {
    const next = new Set(prev);
    if (next.has(rowKey)) next.delete(rowKey); else next.add(rowKey);
    return next;
  });

  // "Fetch this window" — promote the visual zoom into the page range so the
  // backend re-buckets at the finer step (plan chart-wrapper addition #1).
  const fetchZoomWindow = () => {
    if (!zoomWindow) return;
    setRange({
      preset: 'custom',
      fromMs: Math.floor(zoomWindow.from * 1000),
      toMs: Math.ceil(zoomWindow.to * 1000),
    });
    setZoomWindow(null);
  };

  // ── Query-console zone styling (unchanged visual language) ───────────────
  const ZONE: CSSProperties = {
    display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap',
    padding: '9px 12px', borderTop: '1px solid var(--border)',
  };
  const ZONE_FIRST: CSSProperties = { ...ZONE, borderTop: 'none' };
  const ZONE_LABEL: CSSProperties = {
    width: 64, flexShrink: 0, fontSize: 10.5, fontWeight: 700,
    letterSpacing: '.5px', color: 'var(--text3)', textTransform: 'uppercase',
  };
  const VDIV: CSSProperties = {
    width: 1, alignSelf: 'stretch', background: 'var(--border)', margin: '0 2px',
  };

  // ── Entry screen — paramless /explore (Phase-1) ───────────────────────────
  if (!hasParams) {
    return (
      <>
        <Topbar title="Explore" range={range} onRangeChange={setRange} />
        <div id="content">
          <QuestionCards history={history} />
          <div style={{ marginTop: 20, paddingTop: 16, borderTop: '1px solid var(--border)' }}>
            <SavedViewsBar page="explore" />
          </div>
        </div>
      </>
    );
  }

  return (
    <>
      <Topbar title="Explore" range={range} onRangeChange={setRange} />
      <div id="content">
        <div style={{
          background: 'var(--bg2)', border: '1px solid var(--border)',
          borderRadius: 'var(--radius)', marginBottom: 12,
        }}>

          {/* SOURCE zone */}
          <div style={ZONE_FIRST}>
            <span style={ZONE_LABEL}>Source</span>
            <div className="segmented">
              {(['spans', 'metrics', 'logs'] as Source[]).map(s => (
                <button key={s} type="button" onClick={() => setSource(s)}
                  className={source === s ? 'active' : ''}
                  style={{ textTransform: 'capitalize' }}>
                  {s}
                </button>
              ))}
            </div>
            <span style={{ flex: 1 }} />
            <SavedViewsBar page="explore" />
            <ShareButton />
          </div>

          {source === 'spans' && (<>

          {/* SHOW zone — result mode + (metric mode) viz rail + step */}
          <div style={ZONE}>
            <span style={ZONE_LABEL}>Show</span>
            <div className="segmented">
              <button type="button" onClick={() => switchResultMode('traces')}
                className={resultMode === 'traces' ? 'active' : ''}>
                ⋮ Traces
              </button>
              <button type="button" onClick={() => switchResultMode('metric')}
                className={resultMode === 'metric' ? 'active' : ''}>
                ∿ Metric
              </button>
              <button type="button" onClick={() => switchResultMode('repeats')}
                className={resultMode === 'repeats' ? 'active' : ''}
                title="Find traces where the same span shape repeats N+ times (N+1 / chatty-RPC detector)">
                ⟳ Repeats
              </button>
            </div>
            {resultMode === 'metric' && (
              <>
                <span style={VDIV} />
                <VizRail value={builder.viz} onChange={v => setBuilder(b => ({ ...b, viz: v }))} />
                <span style={{ color: 'var(--text2)', fontSize: 12, marginLeft: 4 }}>Step:</span>
                <select value={builder.step}
                  onChange={e => setBuilder(b => ({ ...b, step: Number(e.target.value) }))}
                  title="Bucket genişliği — formül hizası için tüm sorgularda ortak">
                  {STEP_OPTIONS.map(o => <option key={o.v} value={o.v}>{o.label}</option>)}
                </select>
              </>
            )}
            <span style={{ flex: 1 }} />
            {resultMode === 'traces' && (
              <button className="sec" type="button" onClick={() => setShowFacets(v => !v)}
                style={{ fontSize: 11, padding: '3px 10px' }}
                title="Toggle the trace tag explorer (discover common values per facet)">
                {showFacets ? '× Facets' : '◫ Facets'}
              </button>
            )}
          </div>

          {/* ASK zone — NL query box stays in the builder (D5). Applies its
              filter set to query A + the page range. */}
          {resultMode === 'metric' && (
            <div style={ZONE}>
              <span style={ZONE_LABEL}>Ask</span>
              <div style={{ flex: 1, minWidth: 240 }}>
                <NLQueryBox
                  onApply={(nlFilters, preset) => {
                    setBuilder(b => ({
                      ...b,
                      queries: b.queries.map((q, i) =>
                        i === 0 ? { ...q, filters: nlFilters as FilterExpr[] } : q),
                    }));
                    setRange({ preset });
                  }} />
              </div>
            </div>
          )}

          {/* QUERY rows — the A–D builder (metric mode) */}
          {resultMode === 'metric' && (
            <>
              {builder.queries.map((q, i) => (
                <QueryRow key={q.letter} q={q}
                  canRemove={builder.queries.length > 1}
                  onChange={nq => setQuery(i, nq)}
                  onRemove={() => removeQuery(i)} />
              ))}
              <div style={ZONE}>
                <span style={ZONE_LABEL}>Formula</span>
                <FormulaRow value={builder.formula}
                  onChange={f => setBuilder(b => ({ ...b, formula: f }))}
                  letters={builder.queries.filter(produces).map(q => q.letter)} />
                <span style={VDIV} />
                <button className="sec" type="button" onClick={addQuery}
                  disabled={builder.queries.length >= MAX_QUERIES}
                  title={builder.queries.length >= MAX_QUERIES ? 'En fazla 4 sorgu (A–D)' : 'Yeni sorgu ekle'}>
                  + Sorgu
                </button>
              </div>
            </>
          )}

          {/* FILTER zone — traces/repeats console (pre-v2 shape) */}
          {resultMode !== 'metric' && (
            <div style={{ ...ZONE, alignItems: 'flex-start' }}>
              <span style={{ ...ZONE_LABEL, marginTop: 5 }}>Filter</span>
              <div className="segmented" style={{ marginTop: 1 }}>
                <button type="button" onClick={() => setMode('builder')}
                  className={mode === 'builder' ? 'active' : ''}>
                  Builder
                </button>
                <button type="button" onClick={() => setMode('advanced')}
                  className={mode === 'advanced' ? 'active' : ''}>
                  Advanced
                </button>
              </div>
              <div style={{ flex: 1, minWidth: 240 }}>
                {mode === 'builder' && (
                  <FilterBuilder value={filters} onChange={setFilters}
                    suggestedValues={{
                      'service.name': services,
                      'resource.service.name': services,
                      'kind': ['internal', 'server', 'client', 'producer', 'consumer'],
                      'status_code': ['ok', 'error', 'unset'],
                      'http.method': ['GET', 'POST', 'PUT', 'DELETE', 'PATCH'],
                      'db.system': ['postgresql', 'mysql', 'redis', 'mongodb', 'elasticsearch'],
                    }} />
                )}
                {mode === 'advanced' && (
                  <div className="adv-query">
                    <textarea value={dsl}
                      onChange={e => setDsl(e.target.value)}
                      spellCheck={false}
                      placeholder={`# Examples — one condition per line
duration > 500ms
service.name = "frontend"
http.status_code >= 500
status_code = error
peer.service = "payment-service"
db.system in [postgresql, redis]
exception.type exists
name ~ checkout`}
                      rows={Math.max(4, dsl.split('\n').length + 1)} />
                    {queryError && <div className="trp-error" style={{ marginTop: 6 }}>{queryError}</div>}
                    <div style={{ marginTop: 4, fontSize: 11, color: 'var(--text3)' }}
                      title="One condition per line · operators: = != > >= < <= ~ !~ in [a,b] exists · prefix resource./span. to scope · duration accepts 500ms, 1.5s, 2m">
                      Conditions are AND-joined · prefix with <code>resource.</code> or <code>span.</code> to scope ·
                      <code>duration</code> accepts <code>500ms</code>, <code>1.5s</code>, <code>2m</code>
                    </div>
                  </div>
                )}
              </div>
            </div>
          )}

          {/* RESULT zone — traces mode: limit + "showing N of M". */}
          {resultMode === 'traces' && (
            <div style={ZONE}>
              <span style={ZONE_LABEL}>Result</span>
              <span style={{ color: 'var(--text2)', fontSize: 12 }}>Limit:</span>
              <select value={traceLimit} onChange={e => setTraceLimit(Number(e.target.value))}>
                {[20, 50, 100, 200, 500, 1000, 2000, 5000].map(n => <option key={n} value={n}>{n} traces</option>)}
              </select>
              {traces && traceTotal > 0 && (
                <span style={{
                  color: traces.length >= traceLimit && traceTotal > traces.length
                    ? 'var(--err)' : 'var(--text2)',
                  fontSize: 12, fontWeight: 600,
                }}>
                  Showing {fmtNum(traces.length)} of ~{fmtNum(traceTotal)}
                  {traces.length >= traceLimit && traceTotal > traces.length && (
                    <> — raise limit to see more</>
                  )}
                </span>
              )}
              <span style={{ color: 'var(--text3)', fontSize: 11, marginLeft: 'auto' }}>
                Sorted by start time desc
              </span>
            </div>
          )}

          {/* REPEATS zone — presets + shape key + Min repeats. */}
          {resultMode === 'repeats' && (
            <div style={ZONE}>
              <span style={ZONE_LABEL}>Repeats</span>
              {REPEAT_PRESETS.map(p => {
                const active = p.minRepeats === repeatMin
                  && p.groupBy.length === repeatGroupBy.length
                  && p.groupBy.every((k, i) => repeatGroupBy[i] === k);
                return (
                  <button key={p.key} type="button"
                    title={p.hint}
                    onClick={() => {
                      setRepeatGroupBy(p.groupBy);
                      setRepeatMin(p.minRepeats);
                      if (p.filters && p.filters.length > 0) {
                        const extra = p.filters.filter(pf =>
                          !filters.some(x => x.k === pf.k && x.op === pf.op &&
                                              (x.v?.[0] ?? '') === (pf.v?.[0] ?? '')));
                        if (extra.length > 0) setFilters([...filters, ...extra]);
                      }
                    }}
                    className={active ? '' : 'sec'}
                    style={{ fontSize: 11, padding: '4px 10px' }}>
                    {p.label}
                  </button>
                );
              })}
              <span style={VDIV} />
              <span style={{ color: 'var(--text2)', fontSize: 12 }}>Shape:</span>
              <SplitByPicker value={repeatGroupBy} onChange={setRepeatGroupBy} />
              <span style={{ color: 'var(--text2)', fontSize: 12 }}>Min repeats:</span>
              <select value={repeatMin} onChange={e => setRepeatMin(Number(e.target.value))}>
                {[2, 3, 5, 10, 20, 50, 100].map(n => <option key={n} value={n}>≥ {n}</option>)}
              </select>
            </div>
          )}

          </>)}
        </div>

        {/* Metrics + Logs source panels (until the Phase-5 collapse). */}
        {source === 'metrics' && (
          <MetricsExplorer range={range}
            viz={legacyViz}
            compare={false}
            initialService={searchParams.get('service') ?? ''}
            initialMetric={searchParams.get('metric') ?? ''} />
        )}
        {source === 'logs' && (
          <LogsExplorer range={range} viz={legacyViz} compare={false} />
        )}

        {source === 'spans' && (
          <div style={{ display: 'flex', gap: 14, alignItems: 'flex-start' }}>
            {/* FacetsPanel — traces mode only (D5). */}
            {resultMode === 'traces' && showFacets && (
              <div style={{ width: 260, flexShrink: 0 }}>
                <FacetsPanel range={range}
                  dsl={mode === 'advanced' ? dsl : undefined}
                  filters={filters.length > 0 ? encodeFilters(filters) : undefined}
                  onPickValue={(f) => {
                    if (filters.some(x => x.k === f.k && x.op === f.op &&
                                          (x.v?.[0] ?? '') === (f.v?.[0] ?? ''))) {
                      return;
                    }
                    setFilters([...filters, f]);
                  }} />
              </div>
            )}
            <div style={{ flex: 1, minWidth: 0 }}>

        {/* ── Metric mode · panel stack ─────────────────────────────────────── */}
        {resultMode === 'metric' && debounced.viz !== 'heatmap' && (
          <>
            {builderError && (
              <div className="trp-error" style={{ marginBottom: 10 }}>
                Sorgu {builderError.letter} hata verdi: {builderError.message}
              </div>
            )}
            {!anyProduces && (
              <Empty icon="◎" title="Aktif sorgu yok">
                Bir sorguyu aç (harf rozetine tıkla) ya da metric-source sorgusuna bir metrik seç.
              </Empty>
            )}
            {anyProduces && (
              <>
                {zoomWindow && (
                  <div style={{
                    display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8,
                    fontSize: 12, color: 'var(--text2)',
                  }}>
                    <span>🔍 Zoom aktif — tüm paneller senkron</span>
                    <button className="sec" type="button" style={{ fontSize: 11, padding: '2px 8px' }}
                      onClick={() => setZoomWindow(null)}>Sıfırla</button>
                    <button className="sec" type="button" style={{ fontSize: 11, padding: '2px 8px' }}
                      onClick={fetchZoomWindow}
                      title="Zoom penceresini sayfa aralığı yap — backend daha ince bucket'larla yeniden sorgular">
                      Bu pencereyi getir →
                    </button>
                  </div>
                )}
                {panels.length === 0 && anyLoading && <Spinner />}
                {(debounced.viz === 'line' || debounced.viz === 'area' || debounced.viz === 'bars') && (
                  <PanelStack panels={panels}
                    viz={debounced.viz}
                    hiddenKeys={hiddenKeys}
                    focusKey={focusKey}
                    zoomWindow={zoomWindow}
                    onZoom={(f, t) => setZoomWindow({ from: f, to: t })}
                    onExemplarClick={(id) => navigate(`/trace?id=${id}`)} />
                )}
                {(debounced.viz === 'stat' || debounced.viz === 'toplist') && (
                  <SummaryViz panels={panels} mode={debounced.viz} />
                )}
                {/* 'table' viz: no primary panel — the GroupTable below IS the view. */}
                <GroupTable panels={panels}
                  hiddenKeys={hiddenKeys}
                  onToggleHidden={toggleHidden}
                  onFocus={setFocusKey} />
              </>
            )}
          </>
        )}

        {/* ── Metric mode · heatmap viz (query A drives it) ────────────────── */}
        {resultMode === 'metric' && debounced.viz === 'heatmap' && (
          <>
            {heatmap === undefined && <Spinner />}
            {heatmap === null && (
              <Empty icon="◎" title="No data for this query">
                Try a wider time range or fewer filters.
              </Empty>
            )}
            {heatmap && heatmap.maxCount === 0 && (
              <Empty icon="◎" title="No spans matched in this window" />
            )}
            {heatmap && heatmap.maxCount > 0 && (
              <div style={{
                background: 'var(--bg1)', border: '1px solid var(--border)',
                borderRadius: 8, padding: 14,
              }}>
                <div style={{ fontSize: 11, color: 'var(--text2)', marginBottom: 8 }}>
                  Latency density · sorgu A filtreleri · {heatmap.times.length} time buckets ×
                  {' '}{heatmap.durationBins.length} log-scale latency bins
                  · peak cell {heatmap.maxCount.toLocaleString()} spans
                </div>
                <LatencyHeatmap data={heatmap}
                  onCellClick={(cell) => setCellExemplar({
                    timeNs: cell.timeNs,
                    lowDurMs: cell.lowDurMs,
                    highDurMs: cell.highDurMs,
                    count: cell.count,
                  })} />
              </div>
            )}
          </>
        )}

        {/* ── Traces mode ──────────────────────────────────────────────────── */}
        {resultMode === 'traces' && (
          <TracesResult
            traces={traces}
            traceTotal={traceTotal}
            extraCols={extraCols}
            setExtraCols={setExtraCols} />
        )}

        {/* ── Repeats mode ─────────────────────────────────────────────────── */}
        {resultMode === 'repeats' && (
          <RepeatsResult
            repeats={repeats}
            repeatMin={repeatMin}
            groupBy={repeatGroupBy} />
        )}
            </div>
          </div>
        )}

        {/* Heatmap cell-click exemplars modal (query A's filter context). */}
        {cellExemplar && (() => {
          const bucketWidthNs = (heatmap && heatmap.times.length >= 2)
            ? heatmap.times[1] - heatmap.times[0]
            : 60 * 1e9;
          const a = debounced.queries.find(produces);
          return (
            <HeatmapCellExemplars
              cell={cellExemplar}
              bucketWidthNs={bucketWidthNs}
              filters={a ? effectiveFilters(a) : []}
              dsl={a && a.dsl.trim() ? a.dsl : undefined}
              onClose={() => setCellExemplar(null)} />
          );
        })()}
      </div>
    </>
  );
}

// hasMeaningfulParams — true when the URL carries a real query (any param
// other than `range`). Unchanged from Phase-1.
function hasMeaningfulParams(sp: URLSearchParams): boolean {
  for (const k of sp.keys()) {
    if (k !== 'range') return true;
  }
  return false;
}

// legacyHistoryDesc — recent-queries label for the traces/repeats console.
function legacyHistoryDesc(s: {
  resultMode: ResultMode; mode: 'builder' | 'advanced'; dsl: string;
  filters: FilterExpr[]; repeatMin: number; repeatGroupBy: string[];
}): string {
  const where = s.mode === 'advanced'
    ? (s.dsl.trim() ? s.dsl.trim().replace(/\s+/g, ' ').slice(0, 60) : 'all spans')
    : (s.filters.length
        ? s.filters.map(f => `${f.k}${f.op}${(f.v ?? []).join('|')}`).join(' · ').slice(0, 60)
        : 'all spans');
  if (s.resultMode === 'repeats') {
    const shape = s.repeatGroupBy.length ? s.repeatGroupBy.join(' + ') : 'db.statement';
    return `Repeats ≥${s.repeatMin} · ${shape} · ${where}`;
  }
  return `Traces · ${where}`;
}

export default function ExplorePage() {
  // Key the inner workspace on entry-vs-deep-link so an entry ↔ deep-link
  // transition remounts ExploreInner and its useState initializers re-seed
  // from the new params (Phase-1 mechanism, unchanged).
  const { search } = useLocation();
  const meaningful = hasMeaningfulParams(new URLSearchParams(search));
  return (
    <Suspense fallback={<Spinner />}>
      <ExploreInner key={meaningful ? 'workspace' : 'entry'} />
    </Suspense>
  );
}
