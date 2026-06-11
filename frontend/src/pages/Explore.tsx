import { Suspense, useEffect, useMemo, useRef, useState } from 'react';
import type { CSSProperties } from 'react';
import { useLocation, useNavigate, useSearchParams } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { Combobox } from '@/components/Combobox';
import { FilterBuilder } from '@/components/FilterBuilder';
import { MultiLineChart } from '@/components/MultiLineChart';
import { RedPanel } from '@/components/RedPanel';
import { HeatmapCellExemplars, type HeatmapCellRef } from '@/components/HeatmapCellExemplars';
import { SavedViewsBar } from '@/components/SavedViewsBar';
import { LatencyHeatmap } from '@/components/LatencyHeatmap';
import { BubbleUpPanel } from '@/components/BubbleUpPanel';
import { FacetsPanel } from '@/components/FacetsPanel';
import { ShareButton } from '@/components/ShareButton';
import { LogsExplorer } from '@/components/LogsExplorer';
import { MetricsExplorer } from '@/components/MetricsExplorer';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import { api } from '@/lib/api';
import { useExemplarFetcher, useServiceDeploys, useSLOs } from '@/lib/queries';
import { timeRangeToNs, fmtNum } from '@/lib/utils';
import { encodeRange, decodeRange, encodeFilters, decodeFilters, buildQuery } from '@/lib/urlState';
import { storedRangeString } from '@/lib/useUrlRange';
import { decodeMetricQuery, type MetricQuery, type MetricViz } from '@/lib/metricQuery';
import type { TimeRange, FilterExpr, SpanMetricSeries, SpanAgg, LatencyHeatmap as Heatmap } from '@/lib/types';
import {
  AGG_OPTIONS, SUGGESTED_GROUPBY, METRIC_PRESETS, REPEAT_PRESETS,
  TOPN_OPTIONS, STEP_OPTIONS, SUMMARY_COLS, needsField,
  type ResultMode, type Source, type Viz, type BubbleUpMode,
  type MetricPreset, type SummaryRow,
} from './explore/presets';
import { NLQueryBox } from './explore/NLQueryBox';
import { TracesResult } from './explore/TracesResult';
import { RepeatsResult } from './explore/RepeatsResult';
import { QuestionCards } from './explore/QuestionCards';
import { useQueryHistory } from './explore/useQueryHistory';

// ── "Every metric is a doorway" — ?m= descriptor seeding (Phase B) ──────────
// A MetricQuery descriptor riding ?m= takes precedence over the individual
// params: the panel that drew the chart hands its exact descriptor to the
// explorer, which decodes it back into this very builder. The mapping below is
// the descriptor → Explore-state projection; Explore's spans workspace is the
// only first-class metric surface today, so the spanmetrics/tracemetrics
// sources both land on source='spans' + resultMode='metric'. (A first-class
// `tracemetrics` source — distinct CH pipeline — is a later phase.)
interface ExploreSeed {
  resultMode: ResultMode;       // always 'metric' for a descriptor
  agg: SpanAgg;
  field: string;
  groupBy: string[];
  dsl: string;
  mode: 'builder' | 'advanced'; // 'advanced' so the synthesised DSL applies
  viz: Viz;
  step: number;
  range?: TimeRange;
}

function seedFromMetricQuery(mq: MetricQuery): ExploreSeed {
  // agg names line up 1:1 with Explore's SpanAgg / aggToSQL set
  // (rate/count/sum/avg/p50/p90/p95/p99/error_rate all exist there).
  const agg = mq.agg as SpanAgg;
  // field: latency-shaped aggs (or a duration-named metric) measure duration_ms;
  // count-like aggs (rate/count/sum) carry no field.
  const latencyAgg = agg === 'avg' || agg === 'p50' || agg === 'p90' || agg === 'p95' || agg === 'p99';
  const field = (mq.metric.includes('duration') || latencyAgg) ? 'duration_ms' : '';
  // filters Record → AND-joined DSL (`k = "v"`), mode=advanced so it applies.
  const dsl = Object.entries(mq.filters ?? {})
    .filter(([, v]) => v !== '' && v != null)
    .map(([k, v]) => `${k} = "${v}"`)
    .join(' AND ');
  const vizMap: Record<MetricViz, Viz> = {
    line: 'line', area: 'line', bar: 'bar',
    stat: 'kpi', heatmap: 'heatmap', topN: 'topN',
  };
  return {
    resultMode: 'metric',
    agg,
    field,
    groupBy: mq.groupBy ?? [],
    dsl,
    mode: 'advanced',
    viz: vizMap[mq.viz] ?? 'line',
    step: mq.step ? (parseInt(mq.step, 10) || 0) : 0,
    range: mq.range,
  };
}

function ExploreInner() {
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();

  // Decode ?m= ONCE. When present, its projection (seed) takes precedence over
  // the individual params in every state initializer below; when absent, every
  // initializer falls back to its original param-based path unchanged.
  const seedRef = useRef<ExploreSeed | null>(null);
  if (seedRef.current === null) {
    const mq = decodeMetricQuery(searchParams.get('m'));
    seedRef.current = mq ? seedFromMetricQuery(mq) : ({} as ExploreSeed & { __none?: true });
    if (!mq) (seedRef.current as { __none?: true }).__none = true;
  }
  const seedRaw = seedRef.current as ExploreSeed & { __none?: true };
  const seed: ExploreSeed | null = seedRaw.__none ? null : seedRaw;

  // Data source tab — Spans is the rich legacy workspace
  // (filters / aggregation / split-by / traces table). Metrics
  // and Logs are simpler dedicated panels with the same range
  // + viz picker on top so the operator can switch context
  // without retyping. Persisted in the URL as ?source=… so a
  // saved view restores the chosen source.
  const [source, setSource] = useState<Source>(() => {
    // A descriptor always lands on the spans workspace (the metric surface).
    if (seed) return 'spans';
    const v = searchParams.get('source');
    return v === 'metrics' || v === 'logs' ? v : 'spans';
  });
  // Visualization picker — applies to the current source's
  // result. Spans source ignores it for the traces result mode.
  const [viz, setViz] = useState<Viz>(() => {
    if (seed) return seed.viz;
    const v = searchParams.get('viz') as Viz;
    return ['line', 'bar', 'topN', 'kpi', 'heatmap', 'red'].includes(v) ? v : 'line';
  });
  const [compare, setCompare] = useState(searchParams.get('compare') === 'true');

  // ── State, hydrated from URL on first render ─────────────────────────────
  const [range, setRange] = useState<TimeRange>(
    () => seed?.range ?? decodeRange(searchParams.get('range') ?? storedRangeString(), { preset: '30m' }));
  const [filters, setFilters] = useState<FilterExpr[]>(
    () => decodeFilters(searchParams.get('filters')));
  const [agg, setAgg] = useState<SpanAgg>(
    () => seed ? seed.agg : ((searchParams.get('agg') as SpanAgg) || 'count'));
  const [field, setField] = useState(() => seed ? seed.field : (searchParams.get('field') ?? 'duration_ms'));
  const [groupBy, setGroupBy] = useState<string[]>(
    () => seed?.groupBy ?? (searchParams.get('groupBy') ?? '').split(',').filter(Boolean));
  const [groupDraft, setGroupDraft] = useState('');
  const [step, setStep] = useState(() => seed ? seed.step : (parseInt(searchParams.get('step') ?? '0', 10) || 0));
  // Top-N: cap the number of split-by series rendered. 0 means "all".
  // Persists in the URL so a saved Explore view restores the cap.
  const [topN, setTopN] = useState(
    () => parseInt(searchParams.get('topN') ?? '10', 10) || 10);
  const [series, setSeries] = useState<SpanMetricSeries[] | null | undefined>(undefined);
  // Compare-to-previous — second series set fetched at [from-Δ, to-Δ]
  // and overlaid on the chart in dashed translucent style. The Δ is
  // the current window width so "vs previous period" means apples-to-
  // apples (last 1h vs the 1h before). Empty = no compare.
  const [compareSeries, setCompareSeries] = useState<SpanMetricSeries[] | null | undefined>(undefined);
  const [heatmap, setHeatmap] = useState<Heatmap | null | undefined>(undefined);
  // v0.5.260 — Heatmap cell-click drill: opens HeatmapCellExemplars
  // modal showing the trace cohort that produced the clicked
  // (time bucket × latency band).
  const [cellExemplar, setCellExemplar] = useState<HeatmapCellRef | null>(null);
  // BubbleUp panel — operator-driven attribute investigation.
  // The most-recently-added filter chip becomes the
  // "selection" predicate; everything before it is the
  // baseline. Toggle below the chart so it doesn't fire on
  // every render — runs lazily on first open.
  // BubbleUp mode selector — replaces the old single-boolean
  // showBubbleUp. Lets the operator pick a preset selection
  // (errors / slow > 1s / slow > 5s) without manually staging
  // 2 filter chips. 'custom' falls back to the legacy
  // "last chip is the selection" behaviour.
  const [bubbleMode, setBubbleMode] = useState<BubbleUpMode>('off');
  // Facets panel — Datadog "trace tag explorer". Toggled on
  // by default since it's the quickest discovery surface for
  // operators new to a service. Persisted in localStorage so
  // returning users keep their preference.
  const [showFacets, setShowFacets] = useState(() => {
    if (typeof window === 'undefined') return true;
    return localStorage.getItem('coremetry-explore-facets') !== '0';
  });
  useEffect(() => {
    try { localStorage.setItem('coremetry-explore-facets', showFacets ? '1' : '0'); }
    catch { /* ignore */ }
  }, [showFacets]);
  const [services, setServices] = useState<string[]>([]);

  // Result mode: aggregated metrics chart, OR raw matching trace list.
  // Same filter/DSL drives both — different backend endpoint per mode.
  const [resultMode, setResultMode] = useState<ResultMode>(
    () => {
      if (seed) return seed.resultMode;
      const r = searchParams.get('result');
      if (r === 'traces' || r === 'repeats') return r;
      return 'metric';
    });
  // Repeat-finder state — minimum same-shape occurrences per
  // trace before a row surfaces. Pre-v0.4.96 the only way to
  // find N+1 patterns was per-trace eyeballing; default 5 is
  // the "actually noisy" threshold (2-3× duplicates are common
  // and not worth paging on).
  const [repeatMin, setRepeatMin] = useState(
    () => parseInt(searchParams.get('minRepeats') ?? '5', 10) || 5);
  const [repeats, setRepeats] = useState<import('@/lib/types').RepeatedSpanRow[] | null | undefined>(undefined);
  const [traces, setTraces] = useState<import('@/lib/types').TraceRow[] | null | undefined>(undefined);
  const [traceTotal, setTraceTotal] = useState(0);
  const [traceLimit, setTraceLimit] = useState(
    () => parseInt(searchParams.get('limit') ?? '50', 10) || 50);
  // User-selected attribute columns for the traces result table.
  // Mirrors the /traces page: 1-line header chip + per-row mono cell.
  // Persisted to URL as ?cols=key1,key2 so saved Explore queries
  // restore the same column set; bounded to 8 server-side.
  const [extraCols, setExtraCols] = useState<string[]>(
    () => (searchParams.get('cols') ?? '').split(',').map(s => s.trim()).filter(Boolean));

  // Advanced query mode + DSL textarea
  const [mode, setMode] = useState<'builder' | 'advanced'>(
    () => seed ? seed.mode : (searchParams.get('mode') === 'advanced' ? 'advanced' : 'builder'));
  const [dsl, setDsl] = useState(() => seed ? seed.dsl : (searchParams.get('dsl') ?? ''));
  const [queryError, setQueryError] = useState<string | null>(null);

  // Exemplar lookup — picks a representative trace for the
  // current filter window so the user can drill from "this
  // P99 spike" straight into the trace that landed at it.
  // Only enabled when the filter pins a single service (the
  // backend requires service for performant lookup; without
  // it we'd be querying every row in the window).
  const exemplarFetch = useExemplarFetcher();
  const [exemplarBusy, setExemplarBusy] = useState<'slow' | 'error' | null>(null);
  const [exemplarMsg, setExemplarMsg] = useState<string | null>(null);
  const exemplarCtx = useMemo(() => extractExemplarCtx(filters, mode), [filters, mode]);

  // Deploy markers — fetched only when the filter pins a
  // single service (same gating as exemplars; the lookup is
  // service-scoped). The MultiLineChart paints dashed vertical
  // lines at every deploy time so the operator can spot a
  // regression that coincides with a code ship.
  // Memoize so the relative range doesn't churn the
  // useServiceDeploys query key on every render.
  const exploreRange = useMemo(() => timeRangeToNs(range), [range]);
  const deploysQ = useServiceDeploys(
    exemplarCtx?.service ?? '',
    exploreRange.from,
    exploreRange.to,
  );
  const deployMarkers = useMemo(() => {
    if (!deploysQ.data) return undefined;
    return deploysQ.data.map(d => ({
      timeUnixNs: d.timeUnixNs,
      label: d.version,
      description: `${d.spanCount.toLocaleString()} spans since first seen`,
    }));
  }, [deploysQ.data]);

  // SLO-derived chart thresholds. When the filter pins one
  // service, pull every SLO for that service and convert to a
  // horizontal threshold line whose y-value matches the chart's
  // current `agg`:
  //   • latency aggs (p50/p90/p95/p99/avg/max) ← thresholdMs
  //     from the SLO's latency target
  //   • error_rate                              ← (1 - target)
  //     from the SLO's availability target, expressed as %
  //   • everything else (rate / count / errors) gets no
  //     threshold — there's no canonical SLO surface for them.
  // Operations also match: an SLO scoped to a specific span
  // operation only contributes when the chart's filter is on
  // that operation (or no operation pinned, which is the
  // service-wide view).
  const slosQ = useSLOs();
  const sloThresholds = useMemo(() => {
    if (!slosQ.data || !exemplarCtx) return undefined;
    const out: { value: number; label: string; severity: 'warn' | 'err' }[] = [];
    for (const slo of slosQ.data) {
      if (slo.service !== exemplarCtx.service) continue;
      // Scoped-operation SLO only applies when the chart's
      // operation filter matches (or is empty AND we're
      // looking at the right agg).
      if (slo.operation && slo.operation !== exemplarCtx.op) continue;
      if (slo.sliType === 'latency' && /^(p\d+|avg|max)$/.test(agg)) {
        out.push({
          value: slo.thresholdMs,
          label: `SLO ${(slo.target * 100).toFixed(2)}% < ${slo.thresholdMs}ms`,
          severity: 'err',
        });
      } else if (slo.sliType === 'availability' && agg === 'error_rate') {
        const errBudgetPct = (1 - slo.target) * 100;
        out.push({
          value: errBudgetPct,
          label: `SLO target ${(slo.target * 100).toFixed(2)}% (err ≤ ${errBudgetPct.toFixed(2)}%)`,
          severity: 'err',
        });
      }
    }
    return out.length > 0 ? out : undefined;
  }, [slosQ.data, exemplarCtx, agg]);

  async function openExemplar(kind: 'slow' | 'error') {
    if (!exemplarCtx) return;
    const { from, to } = timeRangeToNs(range);
    setExemplarBusy(kind);
    setExemplarMsg(null);
    try {
      const ex = await exemplarFetch({
        service: exemplarCtx.service,
        op: exemplarCtx.op,
        from, to, kind,
      });
      if (!ex) {
        setExemplarMsg(kind === 'error'
          ? 'No error trace in this window.'
          : 'No matching trace in this window.');
        return;
      }
      navigate(`/trace?id=${encodeURIComponent(ex.traceId)}#span=${encodeURIComponent(ex.spanId)}`);
    } catch {
      setExemplarMsg('Lookup failed — try a wider time range.');
    } finally {
      setExemplarBusy(null);
    }
  }

  // Recent-queries ring for the paramless entry screen (explore-v2
  // Phase-1). Debounced save fires below whenever the URL settles on a
  // non-empty shape; the question-card screen reads `history`.
  const { history, save: saveHistory } = useQueryHistory();

  // hasParams — true once the operator has a real query in the URL.
  // Drives the entry-card screen: a fresh /explore (no query, or only the
  // auto-written default `range`) shows QuestionCards; any meaningful
  // search param skips cards and renders the full workspace exactly as
  // before. `range` is excluded because the State→URL effect ALWAYS
  // writes it (encodeRange never empties), so a bare /explore becomes
  // /explore?range=30m on mount — that alone is not "a query". Tracked
  // as state so a card click (navigate to ?…) flips it on next render.
  const [hasParams, setHasParams] = useState(() => hasMeaningfulParams(searchParams));
  // First canonical URL of this mount — the seed a card/deep-link/saved
  // view produced. History records only divergence from it (see the
  // State→URL effect). Refs reset on the entry↔workspace key remount,
  // so each seeded visit gets its own baseline.
  const seedNextRef = useRef<string | null>(null);

  // ── State → URL (replaceState — keeps history clean) ─────────────────────
  useEffect(() => {
    // Non-range entries first so we can tell "real query" from "just the
    // default range" for the entry-screen gate + history ring.
    const queryEntries: Array<[string, string | number | undefined | null | false]> = [
      ['source',  source !== 'spans' ? source : ''],
      ['viz',     viz !== 'line' ? viz : ''],
      ['compare', compare ? 'true' : ''],
      ['result',  resultMode === 'traces' ? 'traces' : ''],
      ['agg',     resultMode === 'metric' && agg !== 'count' ? agg : ''],
      ['field',   resultMode === 'metric' && field !== 'duration_ms' ? field : ''],
      ['groupBy', resultMode === 'metric' ? groupBy.join(',') : ''],
      ['filters', mode === 'builder' ? encodeFilters(filters) : ''],
      ['dsl',     mode === 'advanced' ? dsl : ''],
      ['mode',    mode === 'advanced' ? 'advanced' : ''],
      ['step',    resultMode === 'metric' && step ? step : ''],
      ['topN',    resultMode === 'metric' && groupBy.length > 0 && topN !== 10 ? topN : ''],
      ['limit',   resultMode === 'traces' && traceLimit !== 50 ? traceLimit : ''],
      ['cols',    resultMode === 'traces' ? extraCols.join(',') : ''],
    ];
    const queryQs = buildQuery(queryEntries);
    // Full URL ALWAYS carries range (encodeRange never empties) — same as
    // before this refactor; only the gate ignores range.
    const qs = buildQuery([...queryEntries, ['range', encodeRange(range)]]);
    const next = qs ? `?${qs}` : '';
    if (next !== window.location.search) {
      navigate(`/explore${next}`, { preventScrollReset: true, replace: true });
    }
    // Entry-screen gate + recent-queries ring. A real query (anything
    // beyond the default range) hides the cards + records it (debounced).
    const meaningful = queryQs.length > 0;
    setHasParams(meaningful);
    // Seed-skip: the first canonical URL this mount produces is the
    // SEED (a question card, a shared deep-link, a saved view) — it's
    // already represented by the card/link the operator clicked, so
    // recording it would duplicate the card into "Son sorgular"
    // (Phase-1 review finding). Only states the operator CHANGED
    // away from the seed are history-worthy.
    if (seedNextRef.current === null) {
      seedNextRef.current = next;
    }
    if (meaningful && next !== seedNextRef.current) {
      saveHistory(historyDesc({ resultMode, viz, agg, field, groupBy, mode, dsl, filters, repeatMin }), next);
    }
  }, [source, viz, compare, resultMode, agg, field, groupBy, filters, dsl, mode, range, step, topN, traceLimit, extraCols, repeatMin, navigate, saveHistory]);

  // Load service options for filter value suggestions.
  // Gated on hasParams — the entry-card screen renders no workspace, so
  // firing its fetches there is pure wasted CH load (Phase-1 review
  // finding). Flips true on card click → effect re-runs.
  useEffect(() => {
    if (!hasParams) return;
    api.services(timeRangeToNs(range))
      .then(s => setServices((s ?? []).map(x => x.name)))
      .catch(() => setServices([]));
  }, [range, hasParams]);

  // Run query whenever inputs change (debounce skipped — small payload)
  useEffect(() => {
    if (!hasParams) return;
    setQueryError(null);
    const { from, to } = timeRangeToNs(range);
    const filterArg = mode === 'builder' && filters.length ? JSON.stringify(filters) : undefined;
    const dslArg    = mode === 'advanced' && dsl.trim() ? dsl : undefined;

    if (resultMode === 'metric') {
      // Heatmap fetch is gated on `viz === 'heatmap'` — only
      // pulls the 2D density payload when the operator
      // actually has the heatmap mode selected, so the line
      // / bar / topN / kpi modes don't pay for the extra
      // CH GROUP BY at billion-span scale.
      if (viz === 'heatmap') {
        setHeatmap(undefined);
        api.spanHeatmap({
          filters: filterArg, dsl: dslArg,
          from, to, buckets: 80,
        })
          .then(h => setHeatmap(h ?? null))
          .catch(err => {
            setHeatmap(null);
            const msg = String(err?.message ?? err);
            setQueryError(msg.includes('DSL') ? msg : null);
          });
      } else if (viz === 'red') {
        // RED panel manages its own fetches internally (three
        // parallel spanMetric calls with shared syncKey). Bypass
        // the single-series state machine here so we don't fire
        // a duplicate fourth query on top of those three.
        // setSeries clears the loading-state so a switch back
        // to line/bar/topN/kpi paints fresh.
        setSeries(null);
      } else {
        setSeries(undefined);
        setCompareSeries(undefined);
        api.spanMetric({
          agg, field,
          groupBy: groupBy.join(',') || undefined,
          filters: filterArg, dsl: dslArg,
          from, to,
          step: step || undefined,
        })
          .then(r => setSeries(r ?? []))
          .catch(err => {
            setSeries(null);
            const msg = String(err?.message ?? err);
            setQueryError(msg.includes('DSL') ? msg : null);
          });
        // Compare-to-previous fan-out: same metric over the
        // window of equal width that ended at `from`. Independent
        // fetch so the main chart paints without waiting on the
        // compare result; failures degrade silently to "no
        // comparison overlay" without blocking the page.
        if (compare) {
          const width = to - from;
          api.spanMetric({
            agg, field,
            groupBy: groupBy.join(',') || undefined,
            filters: filterArg, dsl: dslArg,
            from: from - width, to: from,
            step: step || undefined,
          })
            .then(r => setCompareSeries(r ?? []))
            .catch(() => setCompareSeries(null));
        } else {
          setCompareSeries(null);
        }
      }
    } else if (resultMode === 'traces') {
      // Traces mode — same filters/DSL feed the trace search instead.
      // v0.5.314 — Operator-reported: hits the 50 default limit
      // ceiling and doesn't know there are more rows. Switched
      // count mode from default 'skip' (omits total) to 'approx'
      // so the table footer can show "N of M matched, raise
      // limit to see more". Approx mode is fast (counts over
      // the same paginated scan + an O(1) hasMore probe).
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
      // Repeats mode — find (trace_id, group-by) pairs where the
      // same span shape occurred >= repeatMin times. Defaults to
      // grouping by db.statement, the most common N+1 source;
      // operator can flip to peer.service / name / http.route via
      // the split-by chips.
      setRepeats(undefined);
      api.spanRepeats({
        filters: filterArg, dsl: dslArg,
        from, to,
        groupBy: groupBy.length ? groupBy : ['db.statement'],
        minRepeats: repeatMin,
      })
        .then(r => setRepeats(r ?? []))
        .catch(err => {
          setRepeats(null);
          const msg = String(err?.message ?? err);
          setQueryError(msg.includes('DSL') ? msg : null);
        });
    }
  }, [resultMode, viz, range, filters, dsl, mode, agg, field, groupBy, step, traceLimit, extraCols, compare, repeatMin, hasParams]);

  const aggMeta = AGG_OPTIONS.find(o => o.v === agg)!;
  const unit = aggMeta.unit ?? '';
  const totalSeries = series?.length ?? 0;
  const totalPoints = series?.reduce((n, s) => n + s.points.length, 0) ?? 0;

  const addGroupKey = (k: string) => {
    const t = k.trim();
    if (!t || groupBy.includes(t)) return;
    setGroupBy([...groupBy, t]);
    setGroupDraft('');
  };
  const removeGroupKey = (k: string) =>
    setGroupBy(groupBy.filter(x => x !== k));

  // applyPreset swaps the metric stack to a preset triplet
  // (agg / field / viz) plus the suggested split-by — but only
  // overrides split-by when the operator hasn't already picked
  // their own. Pickier presets respect operator intent.
  const applyPreset = (p: MetricPreset) => {
    setAgg(p.agg);
    setField(p.field);
    setViz(p.viz);
    setResultMode('metric');
    if (p.groupBy && groupBy.length === 0) {
      setGroupBy(p.groupBy);
    }
  };

  // Top-N cap: client-side trim of the heaviest series by total
  // count. Cheaper than re-issuing the query with a server-side
  // LIMIT (which would require teaching span_metric a per-series
  // sort, currently absent). Heatmap mode ignores topN.
  const cappedSeries = useMemo(() => {
    if (!series || topN <= 0 || series.length <= topN) return series;
    const withTotal = series.map(s => ({
      s, total: s.points.reduce((n, p) => n + (p.value ?? 0), 0),
    }));
    withTotal.sort((a, b) => b.total - a.total);
    return withTotal.slice(0, topN).map(x => x.s);
  }, [series, topN]);
  // Same cap applied to the compare series so the overlay matches
  // the visible main set. We don't bother matching the SAME N
  // names — the compare set's busiest N is the apples-to-apples
  // "what did the previous period look like, also top-N".
  const cappedCompare = useMemo(() => {
    if (!compareSeries || topN <= 0 || compareSeries.length <= topN) return compareSeries;
    const withTotal = compareSeries.map(s => ({
      s, total: s.points.reduce((n, p) => n + (p.value ?? 0), 0),
    }));
    withTotal.sort((a, b) => b.total - a.total);
    return withTotal.slice(0, topN).map(x => x.s);
  }, [compareSeries, topN]);
  // Combined series fed to the chart. Compare series carry a
  // `compare: true` mark on their groupKey so the chart can
  // render them in dashed translucent style.
  const renderedSeries = useMemo(() => {
    if (!cappedSeries) return cappedSeries;
    if (!cappedCompare || cappedCompare.length === 0) return cappedSeries;
    // Shift compare timestamps forward by the window width so
    // both sets line up on the chart's x-axis — operator sees
    // "now-shape vs prior-period-shape, same time slot".
    const { from: fNow, to: tNow } = timeRangeToNs(range);
    const widthNs = tNow - fNow;
    const shifted = cappedCompare.map(s => ({
      ...s,
      groupKey: [...(s.groupKey ?? []), '(prev)'],
      points: s.points.map(p => ({ ...p, time: p.time + widthNs })),
    }));
    return [...cappedSeries, ...shifted];
  }, [cappedSeries, cappedCompare, range]);

  // Quick stats per series for the summary table.
  const summary = useMemo<SummaryRow[]>(() => {
    if (!series) return [];
    return series.map(s => {
      const vals = s.points.map(p => p.value).filter(v => v != null && !isNaN(v));
      if (vals.length === 0) return { key: s.groupKey, count: 0, last: 0, max: 0, avg: 0 };
      const sum = vals.reduce((a, b) => a + b, 0);
      return {
        key: s.groupKey,
        count: vals.length,
        last: vals[vals.length - 1],
        max: Math.max(...vals),
        avg: sum / vals.length,
      };
    });
  }, [series]);

  // Per-series summary table on the shared sortable+resizable
  // primitive. Default Max desc surfaces the heaviest series first.
  const summaryDt = useDataTable<SummaryRow>({
    storageKey: 'explore-summary',
    columns: SUMMARY_COLS,
    rows: summary,
    initialSort: { id: 'max', dir: 'desc' },
  });

  // ── Query-console zone styling ───────────────────────────────────────────
  // The spans workspace controls live in ONE bordered card whose
  // internal rows are "zones": a fixed-width uppercase micro-label
  // on the left + the relevant controls, separated by a 1px divider.
  // Purely presentational — no logic lives here.
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

  // ── Entry screen — paramless /explore (explore-v2 Phase-1) ───────────────
  // No search params = "what do you want to find out?" question cards +
  // recent queries, with the saved-views bar below. ANY search param skips
  // this entirely and renders the full workspace (deep-links unchanged).
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
        {/* ── Query console — ONE bordered card; internal rows are
            "zones" (fixed micro-label + controls), divided by 1px
            hairlines. Layout/grouping only; every control keeps its
            original handler + state wiring (v0.8.19). */}
        <div style={{
          background: 'var(--bg2)', border: '1px solid var(--border)',
          borderRadius: 'var(--radius)', marginBottom: 12,
        }}>

          {/* SOURCE zone — source segmented left; saved views +
              share pushed right. (Time range lives in the Topbar.) */}
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
            {/* v0.5.275 — Saved views bar. Same component /traces +
                /logs + /problems + /anomalies use; recall via the
                dropdown or 1-9 keyboard shortcut. */}
            <SavedViewsBar page="explore" />
            <ShareButton />
          </div>

          {source === 'spans' && (<>

          {/* ASK zone — natural-language query box (v0.5.255). */}
          <div style={ZONE}>
            <span style={ZONE_LABEL}>Ask</span>
            <div style={{ flex: 1, minWidth: 240 }}>
              <NLQueryBox
                onApply={(nlFilters, preset) => {
                  setFilters(nlFilters as typeof filters);
                  setRange({ preset });
                }} />
            </div>
          </div>

          {/* FILTER zone — Builder⇄Advanced toggle + the chips
              (or the DSL textarea in advanced mode) inline. */}
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

          {/* SHOW zone — result-mode segmented + viz + compare;
              Facets toggle pushed right. ONE compare checkbox lives
              here (the old duplicate in the aggregation row removed). */}
          <div style={ZONE}>
            <span style={ZONE_LABEL}>Show</span>
            <div className="segmented">
              <button type="button" onClick={() => setResultMode('traces')}
                className={resultMode === 'traces' ? 'active' : ''}>
                ⋮ Traces
              </button>
              <button type="button" onClick={() => setResultMode('metric')}
                className={resultMode === 'metric' ? 'active' : ''}>
                ∿ Metric
              </button>
              <button type="button" onClick={() => setResultMode('repeats')}
                className={resultMode === 'repeats' ? 'active' : ''}
                title="Find traces where the same span shape repeats N+ times (N+1 / chatty-RPC detector)">
                ⟳ Repeats
              </button>
            </div>
            <span style={VDIV} />
            <span style={{ color: 'var(--text2)', fontSize: 12 }}>Viz:</span>
            <select value={viz} onChange={e => setViz(e.target.value as Viz)}>
              <option value="line">Line</option>
              <option value="bar">Bar</option>
              <option value="topN">Top-N</option>
              <option value="kpi">KPI</option>
              <option value="heatmap">Heatmap</option>
              <option value="red">RED panel</option>
            </select>
            <label style={{ display: 'flex', alignItems: 'center', gap: 5,
                            color: 'var(--text2)', cursor: 'pointer', fontSize: 12, marginLeft: 8 }}
              title="Overlay the previous window of the same length as faded twin series">
              <input type="checkbox" checked={compare}
                onChange={e => setCompare(e.target.checked)} />
              Compare to previous
            </label>
            <span style={{ flex: 1 }} />
            {/* Facets toggle — surfaces the trace tag explorer as a
                collapsible left sidebar below the card. */}
            <button className="sec" type="button" onClick={() => setShowFacets(v => !v)}
              style={{ fontSize: 11, padding: '3px 10px' }}
              title="Toggle the trace tag explorer (discover common values per facet)">
              {showFacets ? '× Facets' : '◫ Facets'}
            </button>
          </div>

          {/* METRIC zone — quick preset chips (Dynatrace key-metric
              picker). One click swaps (agg + field + viz). */}
          {resultMode === 'metric' && (
            <div style={ZONE}>
              <span style={ZONE_LABEL}>Metric</span>
              {METRIC_PRESETS.map(p => {
                const isActive = p.agg === agg && p.field === field && p.viz === viz;
                return (
                  <button key={p.key} type="button"
                    onClick={() => applyPreset(p)}
                    title={p.hint}
                    className={isActive ? '' : 'sec'}
                    style={{ fontSize: 11, padding: '4px 10px' }}>
                    {p.label}
                  </button>
                );
              })}
              {!METRIC_PRESETS.some(p => p.agg === agg && p.field === field && p.viz === viz) && (
                <span style={{ fontSize: 11, color: 'var(--text3)', fontStyle: 'italic',
                                padding: '4px 10px',
                                border: '1px dashed var(--border)', borderRadius: 'var(--radius-sm)' }}>
                  Custom
                </span>
              )}
            </div>
          )}

          {/* BUILD zone — aggregation + field + step + split-by + Top-N,
              one wrapping line. (Compare checkbox now lives in SHOW.) */}
          {resultMode === 'metric' && (
            <div style={ZONE}>
              <span style={ZONE_LABEL}>Build</span>
              <span style={{ color: 'var(--text2)', fontSize: 12 }}>Agg:</span>
              <select value={agg} onChange={e => setAgg(e.target.value as SpanAgg)}>
                {AGG_OPTIONS.map(o => <option key={o.v} value={o.v}>{o.label}</option>)}
              </select>
              {needsField(agg) && (
                <>
                  <span style={{ color: 'var(--text2)', fontSize: 12 }}>of</span>
                  <Combobox value={field} onChange={setField}
                    options={['duration_ms', 'duration_s', 'http.status_code', '1']}
                    placeholder="duration_ms" width={170} />
                </>
              )}
              <span style={{ color: 'var(--text2)', fontSize: 12, marginLeft: 4 }}>Step:</span>
              <select value={step} onChange={e => setStep(Number(e.target.value))}>
                {STEP_OPTIONS.map(o => <option key={o.v} value={o.v}>{o.label}</option>)}
              </select>
              <span style={VDIV} />
              <span style={{ color: 'var(--text2)', fontSize: 12 }}>Split:</span>
              {groupBy.length === 0 && (
                <span style={{ color: 'var(--text3)', fontSize: 12, fontStyle: 'italic' }}>
                  (single line)
                </span>
              )}
              {groupBy.map(k => (
                <span key={k} className="fb-chip">
                  <b>{k}</b>
                  <button className="fb-chip-x" type="button"
                    onClick={() => removeGroupKey(k)} aria-label="Remove">✕</button>
                </span>
              ))}
              <Combobox value={groupDraft} onChange={setGroupDraft}
                options={SUGGESTED_GROUPBY.filter(k => !groupBy.includes(k))}
                placeholder="+ split key" width={170}
                onEnter={() => addGroupKey(groupDraft)} />
              {groupDraft && (
                <button className="sec" onClick={() => addGroupKey(groupDraft)}>Add</button>
              )}
              {groupBy.length > 0 && (
                <>
                  <span style={{ color: 'var(--text2)', fontSize: 12, marginLeft: 8 }}>Top:</span>
                  <select value={topN} onChange={e => setTopN(Number(e.target.value))}>
                    {TOPN_OPTIONS.map(n => (
                      <option key={n} value={n}>Top {n}</option>
                    ))}
                    <option value={0}>All series</option>
                  </select>
                  {series && topN > 0 && series.length > topN && (
                    <span style={{ fontSize: 11, color: 'var(--text3)' }}>
                      {topN} of {series.length}
                    </span>
                  )}
                </>
              )}
            </div>
          )}

          {/* RESULT zone — traces mode: limit + "showing N of M". */}
          {resultMode === 'traces' && (
            <div style={ZONE}>
              <span style={ZONE_LABEL}>Result</span>
              <span style={{ color: 'var(--text2)', fontSize: 12 }}>Limit:</span>
              <select value={traceLimit} onChange={e => setTraceLimit(Number(e.target.value))}>
                {/* v0.5.314 — raised cap to 5000 (busy 24h windows
                    can have 10k+ matching traces). */}
                {[20, 50, 100, 200, 500, 1000, 2000, 5000].map(n => <option key={n} value={n}>{n} traces</option>)}
              </select>
              {/* v0.5.314 — surface the approx total; red when limit-bound. */}
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

          {/* REPEATS zone — presets + Min repeats. */}
          {resultMode === 'repeats' && (
            <div style={ZONE}>
              <span style={ZONE_LABEL}>Repeats</span>
              {REPEAT_PRESETS.map(p => {
                const active = p.minRepeats === repeatMin
                  && p.groupBy.length === groupBy.length
                  && p.groupBy.every((k, i) => groupBy[i] === k);
                return (
                  <button key={p.key} type="button"
                    title={p.hint}
                    onClick={() => {
                      setGroupBy(p.groupBy);
                      setRepeatMin(p.minRepeats);
                      // Append preset's filter chips de-duped against
                      // existing operator filters.
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
              {!REPEAT_PRESETS.some(p => p.minRepeats === repeatMin
                && p.groupBy.length === groupBy.length
                && p.groupBy.every((k, i) => groupBy[i] === k)) && (
                <span style={{
                  fontSize: 11, color: 'var(--text3)', fontStyle: 'italic',
                  padding: '4px 10px',
                  border: '1px dashed var(--border)', borderRadius: 'var(--radius-sm)',
                }}>Custom</span>
              )}
              <span style={VDIV} />
              <span style={{ color: 'var(--text2)', fontSize: 12 }}>Min repeats:</span>
              <select value={repeatMin} onChange={e => setRepeatMin(Number(e.target.value))}>
                {[2, 3, 5, 10, 20, 50, 100].map(n => <option key={n} value={n}>≥ {n}</option>)}
              </select>
              <span style={{ color: 'var(--text3)', fontSize: 11, marginLeft: 'auto' }}
                title='Split-by below sets the "same shape" key. Pick a preset for one-click defaults.'>
                split-by sets the shape key
              </span>
            </div>
          )}

          </>)}
        </div>

        {/* Metrics + Logs source panels render their own
            workspace + viz; Spans keeps its full legacy UI below.
            Heatmap is a spans-source-only mode; the metrics + logs
            explorers fall back to line when "heatmap" is selected. */}
        {source === 'metrics' && (
          <MetricsExplorer range={range}
            viz={viz === 'heatmap' || viz === 'red' ? 'line' : viz}
            compare={compare}
            initialService={searchParams.get('service') ?? ''}
            initialMetric={searchParams.get('metric') ?? ''} />
        )}
        {source === 'logs' && (
          <LogsExplorer range={range}
            viz={viz === 'heatmap' || viz === 'red' ? 'line' : viz}
            compare={compare} />
        )}
        {source !== 'spans' && null}

        {source === 'spans' && (
          <div style={{ display: 'flex', gap: 14, alignItems: 'flex-start' }}>
            {/* FacetsPanel — collapsible LEFT sidebar (toggled by the
                SHOW-row Facets button; open/closed persisted to
                localStorage). Was a stacked full-width strip before. */}
            {showFacets && (
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
            {/* Right column — chart + per-series summary / traces /
                repeats, exactly as before. */}
            <div style={{ flex: 1, minWidth: 0 }}>

        {/* ── Metric mode · heatmap viz ───────────────────────────────────────── */}
        {resultMode === 'metric' && viz === 'heatmap' && (
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
                  Latency density · {heatmap.times.length} time buckets ×
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

        {/* ── Metric mode · RED panel (Uptrace-style stacked trio) ─────────────── */}
        {resultMode === 'metric' && viz === 'red' && (() => {
          // v0.5.297 — use the memoised exploreRange (line ~295)
          // instead of a bare timeRangeToNs(range). Without this
          // the IIFE re-evaluates now() each render, fresh deps
          // each pass into RedPanel → infinite refetch loop.
          const { from, to } = exploreRange;
          return (
            <RedPanel
              filters={mode === 'builder' ? filters : []}
              dsl={mode === 'advanced' && dsl.trim() ? dsl : undefined}
              from={from} to={to}
              groupBy={groupBy} step={step} field={field} />
          );
        })()}

        {/* ── Metric mode · line/bar/topN/kpi viz ─────────────────────────────── */}
        {resultMode === 'metric' && viz !== 'heatmap' && viz !== 'red' && series === undefined && <Spinner />}
        {resultMode === 'metric' && viz !== 'heatmap' && viz !== 'red' && series && series.length === 0 && (
          <Empty icon="◎" title="No data for this query">
            Try a wider time range, fewer filters, or remove split keys.
          </Empty>
        )}
        {resultMode === 'metric' && viz !== 'heatmap' && viz !== 'red' && series && series.length > 0 && (
          <>
            <div style={{
              background: 'var(--bg1)', border: '1px solid var(--border)',
              borderRadius: 8, padding: 14,
            }}>
              <div style={{
                display: 'flex', alignItems: 'center', gap: 8,
                fontSize: 11, color: 'var(--text2)', marginBottom: 8,
              }}>
                <span>
                  <b style={{ color: 'var(--accent2)' }}>{aggMeta.label}</b>
                  {needsField(agg) && <> of <b style={{ color: 'var(--accent2)' }}>{field}</b></>}
                  {groupBy.length > 0 && <> · split by <b style={{ color: 'var(--accent2)' }}>{groupBy.join(' / ')}</b></>}
                  {' · '}{totalSeries} series · {totalPoints} points
                </span>
                <span style={{ flex: 1 }} />
                {/* Exemplar drill — only enabled when the filter
                    pins one service (the lookup is service-scoped
                    on CH for performance). The buttons jump to a
                    representative slow / error trace for the
                    current window — Datadog / Honeycomb / Grafana
                    pattern; saves the operator from manually
                    wading through /traces. */}
                {exemplarCtx ? (
                  <span style={{ display: 'inline-flex', gap: 6, alignItems: 'center' }}>
                    {exemplarMsg && (
                      <span style={{ color: 'var(--text3)', marginRight: 4 }}>{exemplarMsg}</span>
                    )}
                    <button className="sec"
                      onClick={() => openExemplar('slow')}
                      disabled={exemplarBusy !== null}
                      title={`Open the slowest trace in this window for ${exemplarCtx.service}${exemplarCtx.op ? ' · ' + exemplarCtx.op : ''}`}
                      style={{ fontSize: 11, padding: '2px 8px' }}>
                      {exemplarBusy === 'slow' ? 'Loading…' : 'Slowest trace →'}
                    </button>
                    <button className="sec"
                      onClick={() => openExemplar('error')}
                      disabled={exemplarBusy !== null}
                      title="Open a trace with status=error in this window"
                      style={{ fontSize: 11, padding: '2px 8px' }}>
                      {exemplarBusy === 'error' ? 'Loading…' : 'Error trace →'}
                    </button>
                  </span>
                ) : (
                  <span style={{ color: 'var(--text3)', fontStyle: 'italic' }}
                        title="Add a service.name = ... filter to enable trace drill-down">
                    add service filter to enable trace drill-down
                  </span>
                )}
              </div>
              <MultiLineChart series={renderedSeries ?? series} unit={unit}
                              deploys={deployMarkers}
                              thresholds={sloThresholds}
                              onZoom={(fromSec, toSec) => {
                                // Drag-zoom on the chart → update the
                                // page's TimeRange to the selected
                                // window. Datadog / Grafana pattern;
                                // saves a trip to the topbar picker.
                                setRange({
                                  preset: 'custom',
                                  fromMs: Math.floor(fromSec * 1000),
                                  toMs:   Math.ceil(toSec  * 1000),
                                });
                              }} />
            </div>

            {/* Per-series summary */}
            {groupBy.length > 0 && summary.length > 1 && (
              <div className="table-wrap" style={{ marginTop: 14 }}>
                <table style={{ tableLayout: 'fixed', width: '100%' }}>
                  <DataTableColgroup dt={summaryDt} />
                  <DataTableHead dt={summaryDt} />
                  <tbody>
                    {summaryDt.sortedRows.map((row, i) => (
                      <tr key={`${row.key.join('')}|${i}`}>
                        <td><b>{row.key.join(' / ') || '(all)'}</b></td>
                        <td className="mono" style={{ textAlign: 'right' }}>{row.last.toFixed(2)}{unit}</td>
                        <td className="mono" style={{ textAlign: 'right' }}>{row.avg.toFixed(2)}{unit}</td>
                        <td className="mono" style={{ textAlign: 'right' }}>{row.max.toFixed(2)}{unit}</td>
                        <td className="mono" style={{ textAlign: 'right' }}>{fmtNum(row.count)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}

            {/* BubbleUp — what's special about THESE spans?
                Splits the active filters into baseline (all
                but the most recent) vs selection (the most
                recent chip). Lazy: only fetches when the
                operator opens the panel, so adding chips
                doesn't fire the divergence query on every
                tweak. */}
            {/* BubbleUp — Honeycomb-style "what's special about
                THESE spans?" investigator. Three entry-points:
                   • Errors  — selection = status_code='error'
                   • Slow    — selection = duration > <threshold>
                   • Custom  — last filter chip is the selection
                The two presets are the day-1 questions an SRE
                asks; they used to be gated behind a manual
                "add two filter chips" trick that few discovered.
                Now they're one click and run immediately. */}
            <div style={{ marginTop: 14 }}>
              <div style={{
                display: 'inline-flex', gap: 4, alignItems: 'center',
              }}>
                <span style={{
                  fontSize: 10, color: 'var(--text3)',
                  textTransform: 'uppercase', letterSpacing: 0.4,
                  marginRight: 4,
                }}>🫧 Investigate</span>
                {(['off', 'errors', 'slow1s', 'slow5s', 'custom'] as BubbleUpMode[]).map(m => {
                  const label = m === 'off'    ? 'off'
                              : m === 'errors' ? 'errors'
                              : m === 'slow1s' ? 'slow (>1s)'
                              : m === 'slow5s' ? 'slow (>5s)'
                              :                  'custom (last chip)';
                  const disabled = m === 'custom' && filters.length < 2;
                  return (
                    <button key={m} type="button"
                      disabled={disabled}
                      onClick={() => setBubbleMode(m)}
                      title={disabled
                        ? 'Add 2+ filter chips and pick this to compare the last chip as selection'
                        : (m === 'off' ? 'Hide BubbleUp panel' : `Run BubbleUp with ${label} as selection`)}
                      style={{
                        all: 'unset', cursor: disabled ? 'not-allowed' : 'pointer',
                        fontSize: 11, padding: '3px 9px', borderRadius: 3,
                        fontFamily: 'ui-monospace, SFMono-Regular, monospace',
                        background: bubbleMode === m ? 'var(--accent2)' : 'var(--bg2)',
                        color: bubbleMode === m ? 'var(--bg)'
                             : disabled ? 'var(--text3)' : 'var(--text2)',
                        border: `1px solid ${bubbleMode === m ? 'var(--accent2)' : 'var(--border)'}`,
                        fontWeight: bubbleMode === m ? 600 : 400,
                        opacity: disabled ? 0.5 : 1,
                      }}>{label}</button>
                  );
                })}
              </div>
              {bubbleMode !== 'off' && (() => {
                // v0.5.297 — same render-trap fix as RedPanel above.
                const { from, to } = exploreRange;
                // Construct (baseline, selection) per mode.
                // Errors / slow presets: baseline = everything
                // in the current filter chips; selection adds
                // the predicate on top.
                let baseline = filters;
                let selection: typeof filters = [];
                if (bubbleMode === 'errors') {
                  selection = [{ k: 'status_code', op: '=', v: ['error'] }];
                } else if (bubbleMode === 'slow1s') {
                  selection = [{ k: 'duration_ms', op: '>', v: ['1000'] }];
                } else if (bubbleMode === 'slow5s') {
                  selection = [{ k: 'duration_ms', op: '>', v: ['5000'] }];
                } else if (bubbleMode === 'custom') {
                  // legacy behaviour — last chip is the
                  // selection, the rest is the baseline.
                  baseline = filters.slice(0, -1);
                  selection = filters.slice(-1);
                }
                return (
                  <BubbleUpPanel
                    baseline={baseline}
                    selection={selection}
                    from={from}
                    to={to}
                    onApplyFilter={(f) => setFilters([...filters, f])} />
                );
              })()}
            </div>
          </>
        )}

        {/* ── Traces mode: matching trace list (extracted v0.8 explore-v2) ─────── */}
        {resultMode === 'traces' && (
          <TracesResult
            traces={traces}
            traceTotal={traceTotal}
            extraCols={extraCols}
            setExtraCols={setExtraCols} />
        )}

        {/* ── Repeats mode: N+1 / fan-out finder (extracted v0.8 explore-v2) ───── */}
        {resultMode === 'repeats' && (
          <RepeatsResult
            repeats={repeats}
            repeatMin={repeatMin}
            groupBy={groupBy} />
        )}
            </div>
          </div>
        )}

        {/* v0.5.260 — Heatmap cell-click exemplars modal. Renders
            globally at the end of the page so its z-index sits
            on top of every other surface; closes via Esc,
            backdrop click, or "Open trace" navigation. */}
        {cellExemplar && (() => {
          // Bucket width — estimate from the rendered heatmap's
          // time array (one bucket = times[1] - times[0]); fall
          // back to a 60s default if we somehow don't have two
          // buckets.
          const bucketWidthNs = (heatmap && heatmap.times.length >= 2)
            ? heatmap.times[1] - heatmap.times[0]
            : 60 * 1e9;
          return (
            <HeatmapCellExemplars
              cell={cellExemplar}
              bucketWidthNs={bucketWidthNs}
              filters={mode === 'builder' ? filters : []}
              dsl={mode === 'advanced' && dsl.trim() ? dsl : undefined}
              onClose={() => setCellExemplar(null)} />
          );
        })()}
      </div>
    </>
  );
}

// hasMeaningfulParams — true when the URL carries a real query, i.e. any
// search param other than `range`. The State→URL effect always writes
// `range` (encodeRange never empties), so a fresh /explore becomes
// /explore?range=30m on mount; that alone must still show the entry
// cards. Any deep-link shape (agg/field/groupBy/filters/dsl/result/m/
// service/metric/source/…) carries a non-range key and counts as a query.
function hasMeaningfulParams(sp: URLSearchParams): boolean {
  for (const k of sp.keys()) {
    if (k !== 'range') return true;
  }
  return false;
}

// historyDesc — one-line human summary of the current query, used as
// the recent-queries ("Son sorgular") label AND the dedupe key. Phase-1
// explore-v2: a stable, readable digest of (resultMode, viz, agg/field,
// split, filters/dsl) — re-running the same query produces the same
// string so it bumps in the ring instead of duplicating.
function historyDesc(s: {
  resultMode: ResultMode; viz: Viz; agg: SpanAgg; field: string;
  groupBy: string[]; mode: 'builder' | 'advanced'; dsl: string;
  filters: FilterExpr[]; repeatMin: number;
}): string {
  const where = s.mode === 'advanced'
    ? (s.dsl.trim() ? s.dsl.trim().replace(/\s+/g, ' ').slice(0, 60) : 'all spans')
    : (s.filters.length
        ? s.filters.map(f => `${f.k}${f.op}${(f.v ?? []).join('|')}`).join(' · ').slice(0, 60)
        : 'all spans');
  if (s.resultMode === 'traces') return `Traces · ${where}`;
  if (s.resultMode === 'repeats') {
    const shape = s.groupBy.length ? s.groupBy.join(' + ') : 'db.statement';
    return `Repeats ≥${s.repeatMin} · ${shape} · ${where}`;
  }
  // metric
  const agg = AGG_OPTIONS.find(o => o.v === s.agg)?.label ?? s.agg;
  const split = s.groupBy.length ? ` by ${s.groupBy.join(' / ')}` : '';
  const fieldPart = needsField(s.agg) ? ` of ${s.field}` : '';
  return `${agg}${fieldPart}${split} · ${s.viz} · ${where}`;
}

// extractExemplarCtx — pull (service, op) from the filter chips so
// we can light up the "Open exemplar trace" buttons. Returns null
// when no single service is pinned by an `=` filter; the backend
// requires service for the lookup to fan out cheaply across the
// (service_name, time) primary key, and a wide-open lookup would
// regress at billion-span scale. Operation is optional — when set
// it narrows the exemplar to a specific endpoint, otherwise the
// slowest span anywhere in the service wins.
function extractExemplarCtx(filters: FilterExpr[], mode: 'builder' | 'advanced'): {
  service: string;
  op?: string;
} | null {
  if (mode !== 'builder') return null;
  let service = '';
  let op: string | undefined;
  for (const f of filters) {
    // FilterExpr.v is an array; only pick single-value '=' filters
    // for unambiguous extraction. IN with one value also counts.
    if ((f.op !== '=' && f.op !== 'IN') || f.v.length !== 1) continue;
    const val = f.v[0];
    if (f.k === 'service.name' || f.k === 'resource.service.name') service = val;
    else if (f.k === 'name' || f.k === 'span.name') op = val;
  }
  if (!service) return null;
  return { service, op };
}

export default function ExplorePage() {
  // Key the inner workspace on entry-vs-deep-link so an entry ↔ deep-link
  // transition remounts ExploreInner and its useState initializers re-seed
  // from the new params. Explore reads the URL into state ONLY at mount
  // (useState(() => …)); React Router does NOT remount on a query-string
  // change, so a question-card click (navigate to /explore?…) would
  // otherwise leave state at defaults and the State→URL effect would
  // immediately wipe the card's params. The key flips only on the
  // meaningful-params boundary (range alone = entry), so the workspace
  // stays mounted across its own URL writes — transient state (zoom,
  // bubbleMode, sort) survives.
  const { search } = useLocation();
  const meaningful = hasMeaningfulParams(new URLSearchParams(search));
  return (
    <Suspense fallback={<Spinner />}>
      <ExploreInner key={meaningful ? 'workspace' : 'entry'} />
    </Suspense>
  );
}
