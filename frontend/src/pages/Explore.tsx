import { Suspense, useEffect, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { useNavigate, useSearchParams } from 'react-router-dom';
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
import { ColumnManager } from '@/components/ColumnManager';
import { api } from '@/lib/api';
import { useExemplarFetcher, useServiceDeploys, useSLOs } from '@/lib/queries';
import { timeRangeToNs, fmtNum, tsLong, rowClickHandlers } from '@/lib/utils';
import { encodeRange, decodeRange, encodeFilters, decodeFilters, buildQuery } from '@/lib/urlState';
import type { TimeRange, FilterExpr, SpanMetricSeries, SpanAgg, TraceRow, LatencyHeatmap as Heatmap } from '@/lib/types';

type ResultMode = 'metric' | 'traces' | 'repeats';

// BubbleUpMode — chooses the (baseline, selection) predicate
// pair for the BubbleUp investigator.
//   off     — panel hidden, no fetch
//   errors  — selection = status_code='error'
//   slow1s  — selection = duration_ms > 1000
//   slow5s  — selection = duration_ms > 5000
//   custom  — selection = the last filter chip the user added;
//             everything before it is the baseline. Legacy
//             behaviour for power users who already know the
//             "stage 2 chips" trick.
type BubbleUpMode = 'off' | 'errors' | 'slow1s' | 'slow5s' | 'custom';
type TraceSortKey = 'traceId' | 'rootName' | 'serviceName' | 'duration' | 'spans' | 'time' | 'status';

// Each column's natural starting direction when first selected: time
// and duration are most-recent / slowest-first (descending), others
// alphabetical ascending. Matches the convention on /traces and /services.
const TRACE_SORT_NATURAL: Record<TraceSortKey, 'asc' | 'desc'> = {
  traceId: 'asc', rootName: 'asc', serviceName: 'asc',
  duration: 'desc', spans: 'desc', time: 'desc', status: 'desc',
};

const AGG_OPTIONS: { v: SpanAgg; label: string; unit?: string }[] = [
  { v: 'count',      label: 'Count',           unit: '' },
  { v: 'rate',       label: 'Rate (per sec)',  unit: '/s' },
  { v: 'errors',     label: 'Error count',     unit: '' },
  { v: 'error_rate', label: 'Error rate (%)',  unit: '%' },
  { v: 'avg',        label: 'Avg',             unit: 'ms' },
  { v: 'p50',        label: 'P50 (median)',    unit: 'ms' },
  { v: 'p90',        label: 'P90',             unit: 'ms' },
  { v: 'p95',        label: 'P95',             unit: 'ms' },
  { v: 'p99',        label: 'P99',             unit: 'ms' },
  { v: 'p999',       label: 'P99.9',           unit: 'ms' },
  { v: 'min',        label: 'Min',             unit: 'ms' },
  { v: 'max',        label: 'Max',             unit: 'ms' },
  { v: 'sum',        label: 'Sum',             unit: 'ms' },
];

const SUGGESTED_GROUPBY = [
  'service.name', 'name', 'kind', 'status_code',
  'http.method', 'http.route', 'http.status_code',
  'db.system', 'rpc.method', 'peer.service',
  'resource.host.name', 'resource.deployment.environment',
];

// Quick-metric presets — one click swaps the (agg, field, viz)
// triplet to a common-use shape. Saves operators from "wait,
// which option gives me error rate per service" navigation.
// Each preset is the answer to one of the questions operators
// actually ask during triage. Dynatrace's metric picker pre-
// computes these as "key metrics"; we keep them lightweight
// (no separate column) and consistent with the existing
// builder + DSL flow.
type MetricPreset = {
  key: string;
  label: string;
  hint: string;
  agg: SpanAgg;
  field: string;
  viz: Viz;
  // Optional split-by recommendation applied when picked from
  // an empty / single-key split state. Operator overrides freely.
  groupBy?: string[];
};
const METRIC_PRESETS: MetricPreset[] = [
  { key: 'rps',     label: 'Requests / sec',   hint: 'Throughput (rate of all matching spans)',          agg: 'rate',       field: 'duration_ms', viz: 'line', groupBy: ['service.name'] },
  { key: 'errpct',  label: 'Error rate %',     hint: 'Percentage of spans with status_code = error',     agg: 'error_rate', field: 'duration_ms', viz: 'line', groupBy: ['service.name'] },
  { key: 'errcnt',  label: 'Errors / period',  hint: 'Absolute error count per bucket',                  agg: 'errors',     field: 'duration_ms', viz: 'bar',  groupBy: ['service.name'] },
  { key: 'p99',     label: 'P99 latency',      hint: 'Tail latency — slowest 1% per bucket',             agg: 'p99',        field: 'duration_ms', viz: 'line', groupBy: ['service.name'] },
  { key: 'p95',     label: 'P95 latency',      hint: 'Standard tail-latency SLO indicator',              agg: 'p95',        field: 'duration_ms', viz: 'line', groupBy: ['service.name'] },
  { key: 'avglat',  label: 'Avg latency',      hint: 'Mean duration — best for noisy quantile sets',     agg: 'avg',        field: 'duration_ms', viz: 'line', groupBy: ['service.name'] },
  { key: 'count',   label: 'Span count',       hint: 'Raw count per bucket, no normalisation',           agg: 'count',      field: 'duration_ms', viz: 'bar' },
  { key: 'heatmap', label: 'Latency heatmap',  hint: 'Honeycomb-style 2D density (time × log-duration)', agg: 'count',      field: 'duration_ms', viz: 'heatmap' },
  // v0.5.260 — Uptrace-style "group by operation signature".
  // Splits by (service.name, name) together so every distinct
  // service+operation pair gets its own line. Pairs with the
  // RED viz so the operator sees rate / errors / p99 broken
  // down per operation in one click — Uptrace's `group by
  // _group_id` killer view, native to Coremetry.
  { key: 'red-op',  label: 'RED by operation', hint: 'Rate + errors + p99 stacked, broken down by (service, operation)', agg: 'rate', field: 'duration_ms', viz: 'red',  groupBy: ['service.name', 'name'] },
];

// REPEAT_PRESETS — one-click pick of (groupBy, minRepeats) that
// turn the Repeats mode into a question. "SQL N+1" groups by
// db.statement at ≥5 (typical ORM offender). "Chatty RPC"
// groups by name+peer.service at ≥3 (matches the user's
// example: 3 gRPC calls to the same operation in one trace
// surface as a row). "Endpoint fan-out" groups by http.route
// at ≥5 (a service hammering its own endpoint).
type RepeatPreset = {
  key: string;
  label: string;
  hint: string;
  groupBy: string[];
  minRepeats: number;
  // Optional filter pins added to the chip list when the preset
  // fires. "Chatty RPC" sets kind=client so we count caller-side
  // outbound spans only — otherwise each duplication double-
  // counts (3 caller client spans + 3 callee server spans → two
  // rows for the same root issue). Filters are AND-merged with
  // whatever the operator already has.
  filters?: FilterExpr[];
};
const REPEAT_PRESETS: RepeatPreset[] = [
  { key: 'rpc',     label: 'Chatty RPC',
    hint: '≥ 3 client-side calls with the same (name, peer.service) — repeated outbound chatter (e.g. api-gateway calling order-service.getOrder 3× in one trace)',
    groupBy: ['name', 'peer.service'], minRepeats: 3,
    filters: [{ k: 'kind', op: '=', v: ['client'] }] },
  { key: 'sql',     label: 'SQL N+1',
    hint: '≥ 5 spans with the same db.statement inside one trace — classic ORM N+1',
    groupBy: ['db.statement'], minRepeats: 5 },
  { key: 'route',   label: 'Endpoint fan-out',
    hint: '≥ 5 spans on the same http.route inside one trace — endpoint hammering itself',
    groupBy: ['http.route'], minRepeats: 5 },
  { key: 'op',      label: 'Same operation',
    hint: '≥ 3 spans with the same name (operation) inside one trace — repeated work regardless of target',
    groupBy: ['name'], minRepeats: 3 },
];

// Top-N split-by — when split is set, cap the chart to the busiest N
// series by total count. Anything past N is silently dropped client-
// side. Prevents the chart from drowning under 200 services on a
// "split by service.name" with a fresh deploy. Default 10.
const TOPN_OPTIONS = [5, 10, 20, 50];

// v0.5.259 — sub-10s steps. See Metrics.tsx for the rationale.
const STEP_OPTIONS = [
  { v: 0,    label: 'Auto' },
  { v: 1,    label: '1 s' },
  { v: 5,    label: '5 s' },
  { v: 10,   label: '10 s' },
  { v: 30,   label: '30 s' },
  { v: 60,   label: '1 min' },
  { v: 300,  label: '5 min' },
  { v: 1800, label: '30 min' },
];

type Source = 'spans' | 'metrics' | 'logs';
type Viz = 'line' | 'bar' | 'topN' | 'kpi' | 'heatmap' | 'red';

function ExploreInner() {
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();

  // Data source tab — Spans is the rich legacy workspace
  // (filters / aggregation / split-by / traces table). Metrics
  // and Logs are simpler dedicated panels with the same range
  // + viz picker on top so the operator can switch context
  // without retyping. Persisted in the URL as ?source=… so a
  // saved view restores the chosen source.
  const [source, setSource] = useState<Source>(() => {
    const v = searchParams.get('source');
    return v === 'metrics' || v === 'logs' ? v : 'spans';
  });
  // Visualization picker — applies to the current source's
  // result. Spans source ignores it for the traces result mode.
  const [viz, setViz] = useState<Viz>(() => {
    const v = searchParams.get('viz') as Viz;
    return ['line', 'bar', 'topN', 'kpi', 'heatmap', 'red'].includes(v) ? v : 'line';
  });
  const [compare, setCompare] = useState(searchParams.get('compare') === 'true');

  // ── State, hydrated from URL on first render ─────────────────────────────
  const [range, setRange] = useState<TimeRange>(
    () => decodeRange(searchParams.get('range'), { preset: '30m' }));
  const [filters, setFilters] = useState<FilterExpr[]>(
    () => decodeFilters(searchParams.get('filters')));
  const [agg, setAgg] = useState<SpanAgg>(
    () => (searchParams.get('agg') as SpanAgg) || 'count');
  const [field, setField] = useState(searchParams.get('field') ?? 'duration_ms');
  const [groupBy, setGroupBy] = useState<string[]>(
    () => (searchParams.get('groupBy') ?? '').split(',').filter(Boolean));
  const [groupDraft, setGroupDraft] = useState('');
  const [step, setStep] = useState(parseInt(searchParams.get('step') ?? '0', 10) || 0);
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
  const [traces, setTraces] = useState<TraceRow[] | null | undefined>(undefined);
  // Client-side sort for the traces result table — page-size is small
  // (default 50, max 500) so we don't need a server roundtrip per click.
  const [traceSort, setTraceSort] = useState<TraceSortKey>('time');
  const [traceSortDir, setTraceSortDir] = useState<'asc' | 'desc'>('desc');
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
    () => (searchParams.get('mode') === 'advanced' ? 'advanced' : 'builder'));
  const [dsl, setDsl] = useState(searchParams.get('dsl') ?? '');
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

  // ── State → URL (replaceState — keeps history clean) ─────────────────────
  useEffect(() => {
    const qs = buildQuery([
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
      ['range',   encodeRange(range)],
      ['step',    resultMode === 'metric' && step ? step : ''],
      ['topN',    resultMode === 'metric' && groupBy.length > 0 && topN !== 10 ? topN : ''],
      ['limit',   resultMode === 'traces' && traceLimit !== 50 ? traceLimit : ''],
      ['cols',    resultMode === 'traces' ? extraCols.join(',') : ''],
    ]);
    const next = qs ? `?${qs}` : '';
    if (next !== window.location.search) {
      navigate(`/explore${next}`, { preventScrollReset: true, replace: true });
    }
  }, [source, viz, compare, resultMode, agg, field, groupBy, filters, dsl, mode, range, step, traceLimit, extraCols, navigate]);

  // Load service options for filter value suggestions
  useEffect(() => {
    api.services(timeRangeToNs(range))
      .then(s => setServices((s ?? []).map(x => x.name)))
      .catch(() => setServices([]));
  }, [range]);

  // Run query whenever inputs change (debounce skipped — small payload)
  useEffect(() => {
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
  }, [resultMode, viz, range, filters, dsl, mode, agg, field, groupBy, step, traceLimit, extraCols, compare, repeatMin]);

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

  // Quick stats per series for the summary table
  // Sorted view of the trace results — pure client-side because the
  // page is bounded (default 50, hard max 500). Avoids a server
  // round-trip per header click.
  const sortedTraces = useMemo(() => {
    if (!traces) return traces;
    const cmp = (a: TraceRow, b: TraceRow): number => {
      switch (traceSort) {
        case 'traceId':     return a.traceId.localeCompare(b.traceId);
        case 'rootName':    return (a.rootName || '').localeCompare(b.rootName || '');
        case 'serviceName': return a.serviceName.localeCompare(b.serviceName);
        case 'duration':    return a.durationMs - b.durationMs;
        case 'spans':       return a.spanCount - b.spanCount;
        case 'time':        return a.startTime - b.startTime;
        case 'status':      return Number(a.hasError) - Number(b.hasError);
      }
    };
    const arr = [...traces].sort(cmp);
    return traceSortDir === 'desc' ? arr.reverse() : arr;
  }, [traces, traceSort, traceSortDir]);

  const toggleTraceSort = (col: TraceSortKey) => {
    if (traceSort === col) setTraceSortDir(d => d === 'desc' ? 'asc' : 'desc');
    else { setTraceSort(col); setTraceSortDir(TRACE_SORT_NATURAL[col]); }
  };

  const summary = useMemo(() => {
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

  return (
    <>
      <Topbar title="Explore" range={range} onRangeChange={setRange} />
      <div id="content">
        {/* v0.5.275 — Saved views bar. Same component /traces +
            /logs + /problems + /anomalies use. Operator builds a
            useful filter+DSL+viz combo, hits "Save", picks a
            name → URL search-string persists in saved_views;
            recall via the dropdown or 1-9 keyboard shortcut. */}
        <SavedViewsBar page="explore" />

        {/* Source tabs — Spans (rich legacy workspace), Metrics
            (raw OTel metric_points + label split-by), Logs
            (timeseries from CH or external ES). All three share
            the page's range + viz picker. */}
        <div className="controls" style={{ marginBottom: 6 }}>
          <span style={{ color: 'var(--text2)', fontSize: 12 }}>Source:</span>
          <div style={{ display: 'flex', border: '1px solid var(--border)', borderRadius: 6, overflow: 'hidden' }}>
            {(['spans', 'metrics', 'logs'] as Source[]).map(s => (
              <button key={s} onClick={() => setSource(s)}
                className={source === s ? '' : 'sec'}
                style={{
                  borderRadius: 0,
                  borderRight: s !== 'logs' ? '1px solid var(--border)' : 'none',
                  textTransform: 'capitalize',
                }}>
                {s}
              </button>
            ))}
          </div>
          <span style={{ color: 'var(--text2)', fontSize: 12, marginLeft: 8 }}>Viz:</span>
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
            Compare to previous period
          </label>
          <span style={{ flex: 1 }} />
          <ShareButton />
        </div>

        {/* Metrics + Logs source panels render their own
            workspace + viz; Spans keeps its full legacy UI
            below this fork. */}
        {/* Heatmap is a spans-source-only mode (per-span latency
            distribution); the metrics + logs explorers fall back
            to line when "heatmap" is selected at the top. */}
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

        {source === 'spans' && (<>

        <div style={{ marginBottom: 8, display: 'flex', alignItems: 'center', gap: 10 }}>
          <span style={{ fontSize: 12, color: 'var(--text2)', flex: 1 }}>
            {resultMode === 'metric'
              ? 'Build span metrics on the fly — filter spans, pick an aggregation, optionally split by attributes.'
              : 'Search raw traces with the same filter / DSL — click a row to open the waterfall.'}
          </span>
          {/* Facets toggle — surfaces the trace tag explorer
              panel below this row. Hidden by default would
              defeat the discovery purpose; visible by default
              with a one-click hide for operators who already
              know their filter set. Persisted to localStorage. */}
          <button className="sec" onClick={() => setShowFacets(v => !v)}
            style={{ fontSize: 11, padding: '3px 10px' }}
            title="Toggle the trace tag explorer (discover common values per facet)">
            {showFacets ? '× Facets' : '◫ Facets'}
          </button>
        </div>

        {showFacets && (
          <div style={{ marginBottom: 12 }}>
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

        {/* Result mode toggle: Metric chart / Trace list / Repeats
            finder (all driven by the same filter + window).
            Repeats mode answers "which traces have the same span
            shape happening >= N times" — N+1 detector + chatty-
            RPC finder. */}
        <div className="controls" style={{ marginBottom: 6 }}>
          <span style={{ color: 'var(--text2)', fontSize: 12 }}>Show:</span>
          <div style={{ display: 'flex', border: '1px solid var(--border)', borderRadius: 6, overflow: 'hidden' }}>
            <button onClick={() => setResultMode('traces')}
              className={resultMode === 'traces' ? '' : 'sec'}
              style={{ borderRadius: 0, borderRight: '1px solid var(--border)' }}>
              ⋮ Traces
            </button>
            <button onClick={() => setResultMode('metric')}
              className={resultMode === 'metric' ? '' : 'sec'}
              style={{ borderRadius: 0, borderRight: '1px solid var(--border)' }}>
              ∿ Metric
            </button>
            <button onClick={() => setResultMode('repeats')}
              className={resultMode === 'repeats' ? '' : 'sec'}
              title="Find traces where the same span shape repeats N+ times (N+1 / chatty-RPC detector)"
              style={{ borderRadius: 0 }}>
              ⟳ Repeats
            </button>
          </div>
        </div>

        {/* Quick metric presets — Dynatrace's "key metric" picker.
            One click swaps (agg + field + viz) to a question-shaped
            preset: rps, error rate, p99, etc. The active preset
            is highlighted; "Custom" lights up when the current
            triplet doesn't match any preset (operator hand-tuned). */}
        {resultMode === 'metric' && (
          <div className="controls" style={{ marginBottom: 6, flexWrap: 'wrap' }}>
            <span style={{ color: 'var(--text2)', fontSize: 12 }}>Metric:</span>
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
                              border: '1px dashed var(--border)', borderRadius: 6 }}>
                Custom
              </span>
            )}
          </div>
        )}

        {/* Aggregation + field row — only in metric mode */}
        {resultMode === 'metric' && (
          <div className="controls">
            <span style={{ color: 'var(--text2)', fontSize: 12 }}>Aggregation:</span>
            <select value={agg} onChange={e => setAgg(e.target.value as SpanAgg)}>
              {AGG_OPTIONS.map(o => <option key={o.v} value={o.v}>{o.label}</option>)}
            </select>
            {needsField(agg) && (
              <>
                <span style={{ color: 'var(--text2)', fontSize: 12, marginLeft: 4 }}>of:</span>
                <Combobox value={field} onChange={setField}
                  options={['duration_ms', 'duration_s', 'http.status_code', '1']}
                  placeholder="duration_ms" width={170} />
              </>
            )}
            <span style={{ color: 'var(--text2)', fontSize: 12, marginLeft: 4 }}>Step:</span>
            <select value={step} onChange={e => setStep(Number(e.target.value))}>
              {STEP_OPTIONS.map(o => <option key={o.v} value={o.v}>{o.label}</option>)}
            </select>
            {/* Compare-to-previous overlay toggle. When on, a second
                fetch pulls the SAME metric over the equal-width
                window ending at `from`; the chart draws both with
                the previous period in dashed translucent. */}
            <span style={{ color: 'var(--text2)', fontSize: 12, marginLeft: 4 }}>Compare:</span>
            <label style={{ display: 'inline-flex', alignItems: 'center', gap: 4, fontSize: 12 }}>
              <input type="checkbox" checked={compare}
                onChange={e => setCompare(e.target.checked)} />
              prev period
            </label>
          </div>
        )}

        {resultMode === 'traces' && (
          <div className="controls">
            <span style={{ color: 'var(--text2)', fontSize: 12 }}>Limit:</span>
            <select value={traceLimit} onChange={e => setTraceLimit(Number(e.target.value))}>
              {/* v0.5.314 — raised cap to 5000. At billion-span
                  scale a busy 24h window can have 10k+ matching
                  traces; the old 500 ceiling silently dropped
                  the long tail. */}
              {[20, 50, 100, 200, 500, 1000, 2000, 5000].map(n => <option key={n} value={n}>{n} traces</option>)}
            </select>
            {/* v0.5.314 — surface the approx total from the
                response so the operator can SEE there are more
                rows than the current limit shows. Red when
                limit-bound (operator should raise it). */}
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
            <span style={{ color: 'var(--text2)', fontSize: 12, marginLeft: 'auto' }}>
              Sorted by start time desc
            </span>
          </div>
        )}

        {resultMode === 'repeats' && (
          <>
            {/* Preset row — one-click pick of (groupBy,
                minRepeats) that turns the mode into a question
                shape. Sample: "3 calls to the same gRPC
                operation in one trace" = Chatty RPC. */}
            <div className="controls" style={{ marginBottom: 6, flexWrap: 'wrap' }}>
              <span style={{ color: 'var(--text2)', fontSize: 12 }}>Preset:</span>
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
                      // Append preset's filter chips de-duped
                      // against existing operator filters so
                      // clicking the preset twice doesn't pile
                      // up identical chips.
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
                  border: '1px dashed var(--border)', borderRadius: 6,
                }}>Custom</span>
              )}
            </div>
            <div className="controls">
              <span style={{ color: 'var(--text2)', fontSize: 12 }}>Min repeats:</span>
              <select value={repeatMin} onChange={e => setRepeatMin(Number(e.target.value))}>
                {[2, 3, 5, 10, 20, 50, 100].map(n => <option key={n} value={n}>≥ {n}</option>)}
              </select>
              <span style={{ color: 'var(--text2)', fontSize: 11, marginLeft: 'auto' }}>
                Split-by below sets the "same shape" key. Pick a preset above for one-click defaults.
              </span>
            </div>
          </>
        )}

        {/* Natural-language → filters (v0.5.255). The operator types
            "yesterday's slow checkouts" / "5xx from auth-service last
            hour" / "kafka producer errors today" and the Copilot
            converts the description to a strict-JSON FilterExpr + time
            range we apply directly to the page state. Server-side
            validates ops + presets so a hallucinated key can't leak
            through. Silent on hosts where AI Copilot isn't configured
            — the operator just doesn't see the box. */}
        <NLQueryBox
          onApply={(nlFilters, preset) => {
            setFilters(nlFilters as typeof filters);
            setRange({ preset });
          }} />

        {/* Mode toggle: Builder ⇄ Advanced */}
        <div className="controls" style={{ marginBottom: 6 }}>
          <div style={{ display: 'flex', border: '1px solid var(--border)', borderRadius: 6, overflow: 'hidden' }}>
            <button onClick={() => setMode('builder')}
              className={mode === 'builder' ? '' : 'sec'}
              style={{ borderRadius: 0, borderRight: '1px solid var(--border)' }}>
              Builder
            </button>
            <button onClick={() => setMode('advanced')}
              className={mode === 'advanced' ? '' : 'sec'}
              style={{ borderRadius: 0 }}>
              Advanced query
            </button>
          </div>
          {mode === 'advanced' && (
            <span style={{ color: 'var(--text2)', fontSize: 11 }}>
              One condition per line · operators: <code>=</code> <code>!=</code> <code>&gt;</code> <code>&gt;=</code> <code>&lt;</code> <code>&lt;=</code> <code>~</code> <code>!~</code> <code>in [a,b]</code> <code>exists</code>
            </span>
          )}
        </div>

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
              rows={Math.max(6, dsl.split('\n').length + 1)} />
            {queryError && <div className="trp-error" style={{ marginTop: 6 }}>{queryError}</div>}
            <div style={{ marginTop: 4, fontSize: 11, color: 'var(--text3)' }}>
              Conditions are AND-joined · prefix with <code>resource.</code> or <code>span.</code> to scope ·
              <code>duration</code> accepts <code>500ms</code>, <code>1.5s</code>, <code>2m</code>
            </div>
          </div>
        )}

        {/* Group by — only meaningful for metric mode */}
        {resultMode === 'metric' && (
        <div className="controls" style={{ marginBottom: 14 }}>
          <span style={{ color: 'var(--text2)', fontSize: 12 }}>Split by:</span>
          {groupBy.length === 0 && (
            <span style={{ color: 'var(--text3)', fontSize: 12, fontStyle: 'italic' }}>
              (single line — add attributes to break down)
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
            placeholder="+ split key" width={200}
            onEnter={() => addGroupKey(groupDraft)} />
          {groupDraft && (
            <button className="sec" onClick={() => addGroupKey(groupDraft)}>Add</button>
          )}
          {/* Top-N cap when split is set. Avoids drowning the chart
              under 200 services after a fresh deploy; renders the
              busiest N by total value across the window. */}
          {groupBy.length > 0 && (
            <>
              <span style={{ color: 'var(--text2)', fontSize: 12, marginLeft: 12 }}>Top:</span>
              <select value={topN} onChange={e => setTopN(Number(e.target.value))}>
                {TOPN_OPTIONS.map(n => (
                  <option key={n} value={n}>Top {n}</option>
                ))}
                <option value={0}>All series</option>
              </select>
              {series && topN > 0 && series.length > topN && (
                <span style={{ fontSize: 11, color: 'var(--text3)' }}>
                  showing {topN} of {series.length} series
                </span>
              )}
            </>
          )}
        </div>
        )}

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
                <table>
                  <thead>
                    <tr>
                      <th>Series</th>
                      <th style={{ textAlign: 'right' }}>Last</th>
                      <th style={{ textAlign: 'right' }}>Avg</th>
                      <th style={{ textAlign: 'right' }}>Max</th>
                      <th style={{ textAlign: 'right' }}>Buckets</th>
                    </tr>
                  </thead>
                  <tbody>
                    {summary.map((row, i) => (
                      <tr key={i}>
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

        {/* ── Traces mode: matching trace list ────────────────────────────────── */}
        {resultMode === 'traces' && traces === undefined && <Spinner />}
        {resultMode === 'traces' && traces && traces.length === 0 && (
          <Empty icon="⋮" title="No matching traces">
            Loosen your filters or widen the time range.
          </Empty>
        )}
        {resultMode === 'traces' && traces && traces.length > 0 && (
          <>
            <div style={{ fontSize: 11, color: 'var(--text2)', marginBottom: 8 }}>
              Showing <b style={{ color: 'var(--accent2)' }}>{traces.length}</b> of {fmtNum(traceTotal)} traces
              {traces.length < traceTotal && <> · raise the limit to see more</>}
            </div>
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <TraceSortTh col="traceId"     label="Trace ID"  sort={traceSort} dir={traceSortDir} onSort={toggleTraceSort} />
                    <TraceSortTh col="rootName"    label="Root"      sort={traceSort} dir={traceSortDir} onSort={toggleTraceSort} />
                    <TraceSortTh col="serviceName" label="Service"   sort={traceSort} dir={traceSortDir} onSort={toggleTraceSort} />
                    <TraceSortTh col="duration"    label="Duration"  sort={traceSort} dir={traceSortDir} onSort={toggleTraceSort} align="right" />
                    <TraceSortTh col="spans"       label="Spans"     sort={traceSort} dir={traceSortDir} onSort={toggleTraceSort} align="right" />
                    <TraceSortTh col="time"        label="Started"   sort={traceSort} dir={traceSortDir} onSort={toggleTraceSort} />
                    <TraceSortTh col="status"      label="Status"    sort={traceSort} dir={traceSortDir} onSort={toggleTraceSort} />
                    {/* Same column-manager UX as /traces — adds
                        attribute columns to the result table. */}
                    {extraCols.map(k => (
                      <th key={k} style={{ position: 'relative', whiteSpace: 'nowrap' }}>
                        <span style={{ fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace', fontSize: 11 }}>{k}</span>
                        <button type="button" title="Remove column"
                          onClick={() => setExtraCols(extraCols.filter(c => c !== k))}
                          style={{
                            marginLeft: 6, padding: '0 4px', fontSize: 10, lineHeight: 1,
                            background: 'transparent', border: 'none', color: 'var(--text3)',
                            cursor: 'pointer',
                          }}>×</button>
                      </th>
                    ))}
                    <th style={{ width: 1, whiteSpace: 'nowrap' }}>
                      <ColumnManager
                        cols={extraCols}
                        onAdd={k => { if (!extraCols.includes(k) && extraCols.length < 8) setExtraCols([...extraCols, k]); }} />
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {(sortedTraces ?? []).map(t => (
                    <tr key={t.traceId}
                        {...rowClickHandlers(`/trace?id=${t.traceId}`,
                                             () => navigate(`/trace?id=${t.traceId}`))}
                        style={{ cursor: 'pointer', contentVisibility: 'auto', containIntrinsicSize: 'auto 34px' }}>
                      <td className="mono">
                        <Link to={`/trace?id=${t.traceId}`}
                              onClick={e => e.stopPropagation()}
                              style={{ fontSize: 11 }}>
                          {t.traceId.slice(0, 12)}…
                        </Link>
                      </td>
                      <td><b>{t.rootName}</b></td>
                      <td className="mono" style={{ fontSize: 12 }}>{t.serviceName}</td>
                      <td className="mono" style={{ textAlign: 'right' }}>
                        {t.durationMs.toFixed(1)}ms
                      </td>
                      <td className="mono" style={{ textAlign: 'right' }}>{fmtNum(t.spanCount)}</td>
                      <td className="mono" style={{ fontSize: 11 }}>{tsLong(t.startTime)}</td>
                      <td>
                        {t.hasError
                          ? <span className="badge b-err">ERROR</span>
                          : <span className="badge b-ok">OK</span>}
                      </td>
                      {extraCols.map(k => {
                        const v = t.extras?.[k] ?? '';
                        return (
                          <td key={k} className="mono" style={{ fontSize: 11, color: v ? 'var(--text2)' : 'var(--text3)', whiteSpace: 'nowrap', maxWidth: 280, overflow: 'hidden', textOverflow: 'ellipsis' }} title={v || ''}>
                            {v || '—'}
                          </td>
                        );
                      })}
                      <td />
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </>
        )}

        {/* ── Repeats mode: N+1 / fan-out finder ──────────────── */}
        {resultMode === 'repeats' && repeats === undefined && <Spinner />}
        {resultMode === 'repeats' && repeats && repeats.length === 0 && (
          <Empty icon="⟳" title="No repeated span shapes found">
            No trace has the same (group-by) shape repeating ≥ {repeatMin} times in this window.
            Try lowering the threshold or switching the split-by (e.g. <code>name</code> + <code>peer.service</code> for chatty RPC, <code>http.route</code> for endpoint fan-out).
          </Empty>
        )}
        {resultMode === 'repeats' && repeats && repeats.length > 0 && (
          <>
            <div style={{ marginBottom: 6, fontSize: 12, color: 'var(--text2)' }}>
              {repeats.length} trace{repeats.length === 1 ? '' : 's'} with ≥ {repeatMin} repeats of the same span shape — heaviest at the top.
            </div>
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Trace</th>
                    <th>Service · root</th>
                    <th>Repeated shape ({groupBy.length ? groupBy.join(' + ') : 'db.statement'})</th>
                    <th className="num">Repeats</th>
                    <th className="num">Total time</th>
                    <th>Started</th>
                  </tr>
                </thead>
                <tbody>
                  {repeats.map((r, i) => (
                    <tr key={`${r.traceId}|${i}`}
                        onClick={() => navigate(`/trace?id=${r.traceId}`)}
                        style={{ cursor: 'pointer', contentVisibility: 'auto', containIntrinsicSize: 'auto 34px' }}>
                      <td>
                        <Link to={`/trace?id=${r.traceId}`}
                              onClick={e => e.stopPropagation()}
                              style={{ fontFamily: 'monospace', fontSize: 11 }}>
                          {r.traceId.slice(0, 12)}…
                        </Link>
                      </td>
                      <td style={{ fontSize: 12 }}>
                        <span style={{ fontWeight: 600 }}>{r.service || '—'}</span>
                        {r.rootName && (
                          <span style={{ color: 'var(--text3)' }}> · {r.rootName}</span>
                        )}
                      </td>
                      <td style={{
                        fontFamily: 'ui-monospace, SFMono-Regular, monospace',
                        fontSize: 11, color: 'var(--text2)',
                        maxWidth: 520, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                      }} title={(r.groupValues ?? []).join(' · ')}>
                        {(r.groupValues ?? []).filter(Boolean).join(' · ') ||
                          <span style={{ color: 'var(--text3)' }}>(empty)</span>}
                      </td>
                      <td className="num mono" style={{ fontWeight: 700,
                        color: r.count >= 50 ? 'var(--err)' : r.count >= 20 ? 'var(--warn)' : 'var(--text)' }}>
                        {fmtNum(r.count)}
                      </td>
                      <td className="num mono">{r.totalDurationMs.toFixed(1)}ms</td>
                      <td className="mono" style={{ fontSize: 11, color: 'var(--text3)' }}>
                        {tsLong(r.startedAt)}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </>
        )}
        </>)}

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

function needsField(agg: SpanAgg): boolean {
  return !['count', 'rate', 'errors', 'error_rate'].includes(agg);
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
  return (
    <Suspense fallback={<Spinner />}>
      <ExploreInner />
    </Suspense>
  );
}

// NLQueryBox — v0.5.255 natural-language search input.
// Operator types a plain-English description; the Copilot returns
// a strict-JSON filter set + time range the parent applies.
// Hidden when copilot isn't configured (the endpoint returns 503
// → silent failure mode rather than a misleading "AI failed" toast).
function NLQueryBox({
  onApply,
}: {
  onApply: (filters: { k: string; op: string; v: string[] }[], preset: string) => void;
}) {
  const [prompt, setPrompt] = useState('');
  const [busy, setBusy] = useState(false);
  const [state, setState] = useState<
    | { kind: 'idle' }
    | { kind: 'ok'; explain: string; preset: string; filterCount: number }
    | { kind: 'warn'; msg: string; raw?: string }
    | { kind: 'err'; msg: string }
    | { kind: 'unavailable' }
  >({ kind: 'idle' });

  const run = async () => {
    const trimmed = prompt.trim();
    if (!trimmed) return;
    setBusy(true);
    try {
      const r = await api.copilotNLToQuery(trimmed);
      if (r.warning) {
        setState({ kind: 'warn', msg: r.warning, raw: r.raw });
        return;
      }
      if (!r.filters || r.filters.length === 0) {
        setState({ kind: 'warn', msg: 'Model produced no filters — try rephrasing.' });
        return;
      }
      onApply(r.filters, r.range.preset);
      setState({
        kind: 'ok',
        explain: r.explain,
        preset: r.range.preset,
        filterCount: r.filters.length,
      });
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e);
      // 503 = Copilot not wired; hide the box rather than nag.
      if (msg.toLowerCase().includes('not configured')) {
        setState({ kind: 'unavailable' });
      } else {
        setState({ kind: 'err', msg });
      }
    } finally {
      setBusy(false);
    }
  };

  if (state.kind === 'unavailable') return null;

  return (
    <div style={{
      marginBottom: 10, padding: 8,
      background: 'rgba(139,92,246,0.04)',
      border: '1px solid rgba(139,92,246,0.20)',
      borderRadius: 6,
    }}>
      <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
        <span style={{
          fontSize: 12, fontWeight: 600,
          color: 'var(--accent2, #a78bfa)',
          whiteSpace: 'nowrap',
        }}>✦ Natural language</span>
        <input
          value={prompt}
          onChange={e => setPrompt(e.target.value)}
          onKeyDown={e => e.key === 'Enter' && !busy && run()}
          placeholder={`Try: "yesterday's slow checkouts" · "5xx from auth-service last hour" · "kafka producer errors today"`}
          disabled={busy}
          style={{ flex: 1, fontSize: 13 }} />
        <button onClick={run} disabled={busy || !prompt.trim()}
          style={{ fontSize: 12, whiteSpace: 'nowrap' }}>
          {busy ? 'Thinking…' : 'Apply'}
        </button>
      </div>
      {state.kind === 'ok' && (
        <div style={{
          marginTop: 6, fontSize: 11, color: 'var(--text2)',
          display: 'flex', gap: 8, alignItems: 'baseline',
        }}>
          <span style={{ color: 'var(--ok)' }}>✓</span>
          <span>
            Applied <b>{state.filterCount}</b> filter{state.filterCount === 1 ? '' : 's'} · range
            {' '}<code style={{ fontFamily: 'ui-monospace, monospace' }}>{state.preset}</code>
            {state.explain && <> · <span style={{ color: 'var(--text3)' }}>{state.explain}</span></>}
          </span>
        </div>
      )}
      {state.kind === 'warn' && (
        <div style={{
          marginTop: 6, fontSize: 11, color: 'var(--warn)',
        }}>
          ⚠ {state.msg}
          {state.raw && (
            <pre style={{
              marginTop: 4, padding: 6, borderRadius: 4,
              background: 'var(--bg0)', border: '1px solid var(--border)',
              fontSize: 10, maxHeight: 80, overflow: 'auto',
              whiteSpace: 'pre-wrap',
            }}>{state.raw}</pre>
          )}
        </div>
      )}
      {state.kind === 'err' && (
        <div style={{ marginTop: 6, fontSize: 11, color: 'var(--err)' }}>
          ✗ {state.msg}
        </div>
      )}
    </div>
  );
}

// Sortable header for the traces result table. Reuses the same .sortable
// CSS class as the /traces and /services tables for visual consistency.
function TraceSortTh({ col, label, sort, dir, onSort, align }: {
  col: TraceSortKey; label: string;
  sort: TraceSortKey; dir: 'asc' | 'desc';
  onSort: (c: TraceSortKey) => void;
  align?: 'left' | 'right';
}) {
  const active = sort === col;
  return (
    <th className={`sortable${active ? ' sorted' : ''}`}
        onClick={() => onSort(col)}
        style={{ textAlign: align ?? 'left' }}>
      {label}
      <span className="sort-arrow">{active ? (dir === 'desc' ? '▼' : '▲') : '↕'}</span>
    </th>
  );
}
