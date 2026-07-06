import { Suspense, useEffect, useMemo, useRef, useState } from 'react';
import type { CSSProperties, ReactNode } from 'react';
import { Link } from 'react-router-dom';
import { useSearchParams } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { Topbar } from '@/components/Topbar';
import { KqlSearchInput } from '@/components/KqlSearchInput';
import { SavedViewsBar } from '@/components/SavedViewsBar';
import { Spinner, Empty } from '@/components/Spinner';
import { TableSkeleton } from '@/components/Skeleton';
import { Combobox } from '@/components/Combobox';
import { ServicePicker } from '@/components/ServicePicker';
import { CopyButton } from '@/components/CopyButton';
import { LogTable, DEFAULT_LOG_COLUMNS } from '@/components/LogTable';
import { CorrelationContextDrawer } from '@/components/CorrelationContextDrawer';
import { LogContextModal } from '@/components/LogContextModal';
import { LogsHistogram } from '@/components/LogsHistogram';
import { LogFieldsPanel } from '@/components/LogFieldsPanel';
import { Button } from '@/components/ui/Button';
import { buildKibanaURL } from '@/lib/kibanaLink';
import type { KibanaSettings } from '@/lib/types';
import { useLogs } from '@/lib/queries';
import { useUrlRange } from '@/lib/useUrlRange';
import { getRaw, setRaw } from '@/lib/storage';
import { useTableNav } from '@/lib/useTableNav';
import { api } from '@/lib/api';
import { tsShort, timeRangeToNs, sevName, sevClass } from '@/lib/utils';
import {
  compileSearch, toggleFilter, encodeFiltersParam, parseFiltersParam,
  extractHighlightTerms,
} from '@/lib/logFilters';
import type { LogFilter } from '@/lib/logFilters';
import type { LogsResponse, LogRow, TimeRange } from '@/lib/types';

// Share affordance — copies a link to the CURRENT filtered logs
// view. Logs filters live entirely in the URL querystring (the same
// mechanism SavedViewsBar persists), so the copied link reproduces
// the exact slice — service, cluster, KQL, trace-id, time range —
// for any signed-in operator who opens it. v0.8.102: open to every
// role, viewers included — the operator's parallel to the trace
// "Copy current URL" share, granted alongside viewer public-trace
// minting. NOT a public/unauth link: logs aren't externalised, so
// the recipient still authenticates to Coremetry.
function LogShareButton() {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    if (typeof window === 'undefined') return;
    try {
      await navigator.clipboard.writeText(window.location.href);
    } catch {
      // Non-secure-context fallback (mirrors CopyButton).
      const ta = document.createElement('textarea');
      ta.value = window.location.href;
      ta.style.position = 'fixed'; ta.style.opacity = '0';
      document.body.appendChild(ta); ta.select();
      try { document.execCommand('copy'); } catch { /* swallow */ }
      ta.remove();
    }
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  };
  return (
    <Button variant="secondary" size="sm" onClick={copy}
      title="Copy a shareable link to this filtered logs view (filters are encoded in the URL; recipients sign in to Coremetry to open it)"
      style={{ color: copied ? 'var(--ok)' : undefined }}>
      {copied ? '✓ Copied' : '⧉ Copy link'}
    </Button>
  );
}

// Level facet chips (prototype LogsView .facet/.lvl) — each chip
// drives the EXISTING min-severity filter (filter.severity). The
// `min` is the OTel severity-number floor that the severity <select>
// used: All=0, DEBUG=5, INFO=9, WARN=13, ERROR=17. Clicking a chip
// sets that floor; clicking the active chip again returns to All.
// `bucket` is the canonical severity-band name (matches the names
// the /api/logs/timeseries?groupBy=severity backend returns, see
// LogsHistogram) that we sum counts into for this chip's badge.
const LVL_FACETS: Array<{ key: string; label: string; min: number }> = [
  { key: 'error', label: 'ERROR', min: 17 },
  { key: 'warn',  label: 'WARN',  min: 13 },
  { key: 'info',  label: 'INFO',  min: 9  },
  { key: 'debug', label: 'DEBUG', min: 5  },
];

// Map a backend severity-band name (ERROR / FATAL / WARN / INFO /
// DEBUG / TRACE / OTHER, any casing) to one of the four chip
// buckets. FATAL folds into ERROR; TRACE + OTHER fold into DEBUG —
// so the four chips always sum to the grand total.
function bandToFacet(name: string): 'error' | 'warn' | 'info' | 'debug' {
  const u = name.toUpperCase();
  if (u.startsWith('FATAL') || u.startsWith('ERROR')) return 'error';
  if (u.startsWith('WARN')) return 'warn';
  if (u.startsWith('INFO')) return 'info';
  return 'debug'; // DEBUG / TRACE / OTHER / unknown
}

type SevSeries = { name: string; points: { t: number; v: number }[] };

// pickVolumeBucket — same window→bucket heuristic LogsHistogram
// uses, so the Logs-local stacked-bar volume row and the shared
// histogram below it agree on resolution. Returns seconds.
function pickVolumeBucket(from?: number, to?: number): number {
  if (!from || !to) return 30;
  const spanSec = (to - from) / 1_000_000_000;
  if (spanSec < 60 * 15)      return 5;      // <15min → 5s
  if (spanSec < 60 * 60)      return 30;     // <1h    → 30s
  if (spanSec < 60 * 60 * 6)  return 60;     // <6h    → 1m
  if (spanSec < 60 * 60 * 24) return 5 * 60; // <24h   → 5m
  return 15 * 60;                            // ≥24h   → 15m
}

// Compose a KQL clause from the current filter state so the
// Kibana deep-link lands on the same slice. service / trace_id
// become per-field clauses; the free-text search string passes
// through verbatim (it's already KQL on this page). Returned
// string may be empty — Kibana handles "no query" cleanly.
function buildKQLFromFilter(f: {
  service: string; search: string; severity: number; traceId: string; spanId: string;
}): string {
  const parts: string[] = [];
  if (f.service) parts.push(`service.name:"${f.service.replace(/"/g, '\\"')}"`);
  if (f.traceId) parts.push(`trace.id:"${f.traceId}"`);
  if (f.spanId)  parts.push(`span.id:"${f.spanId}"`);
  if (f.severity > 0) {
    // Map OTel severity number to a level name range; Kibana's
    // log.level field typically holds the canonical text.
    const min = f.severity;
    if (min >= 21) parts.push('log.level:"FATAL"');
    else if (min >= 17) parts.push('log.level:("FATAL" OR "ERROR")');
    else if (min >= 13) parts.push('log.level:("FATAL" OR "ERROR" OR "WARN")');
  }
  if (f.search.trim()) parts.push(f.search.trim());
  return parts.join(' AND ');
}

function LogsInner() {
  const [searchParams, setSearchParams] = useSearchParams();

  const [range, setRange] = useUrlRange('30m');
  // Cursor accumulation (v0.8.260 — "Load more" replaced the
  // Back/Next pager; v0.7.22 keyset cursor mechanics unchanged).
  // `cursor` is the opaque `after` token of the page most recently
  // FETCHED ('' = first). Fetched pages append into `accRows`; a
  // filter / range / query change resets both (a cursor from the
  // old result set is meaningless against a new one — v0.7.81).
  const [cursor, setCursor] = useState('');
  const [accRows, setAccRows] = useState<LogRow[]>([]);
  const resetPaging = () => { setCursor(''); setAccRows([]); };
  const [filter, setFilter] = useState({
    service: '', cluster: '', search: '', severity: 0, traceId: '', spanId: '',
  });
  const [draft, setDraft] = useState(filter);
  // Structured field filters (Kibana Discover pill model) — separate
  // from the free-text `search`. Compiled together right before any
  // query goes out (compiledSearch below), so the backend contract
  // is unchanged. Pills live in the ?filters= URL param so Copy link
  // and SavedViewsBar reproduce them.
  const [filters, setFilters] = useState<LogFilter[]>([]);
  // Dynamic table columns (Discover revamp step 3). localStorage is
  // the operator's standing preference; the ?cols= URL param (only
  // written when non-default) wins on deep links so a shared view
  // reproduces its columns WITHOUT clobbering the recipient's
  // stored preference.
  const COLS_STORE_KEY = 'dt.logs.columns';
  const [logCols, setLogCols] = useState<string[]>(() => {
    const raw = getRaw(COLS_STORE_KEY);
    if (raw) {
      try {
        const a: unknown = JSON.parse(raw);
        if (Array.isArray(a)) return a.filter((x): x is string => typeof x === 'string');
      } catch { /* fall through to defaults */ }
    }
    return DEFAULT_LOG_COLUMNS;
  });
  // '' when the current set matches the defaults → param omitted.
  const colsParam = (cols: string[]) =>
    cols.join(',') === DEFAULT_LOG_COLUMNS.join(',') ? '' : cols.join(',');
  const [expanded, setExpanded] = useState<Set<number>>(new Set());
  // v0.5.471 — cluster list for the inline selector. /api/clusters
  // returns the distinct k8s/openshift cluster names seen in the
  // last 24h; small list, fetched once on mount.
  const [clusters, setClusters] = useState<string[]>([]);
  useEffect(() => {
    const toNs = Date.now() * 1_000_000;
    const fromNs = toNs - 24 * 3600 * 1_000_000_000;
    api.clusters(fromNs, toNs)
      .then(r => setClusters(r?.clusters ?? []))
      .catch(() => setClusters([]));
  }, []);
  // v0.5.399 — trace peek state. Clicking the "👁" button next to a
  // trace_id in the log row sets this. Task #6: the peek now opens the
  // CorrelationContextDrawer anchored on that trace_id — the SAME trace + sibling
  // logs the old TracePeekDrawer showed, plus the service's RED metrics lens, all
  // joined on the exact trace_id. No page change, page filter/search untouched.
  const [peekTraceId, setPeekTraceId] = useState<string | null>(null);
  // v0.5.402 — surrounding-context modal state. Clicking "≡ View
  // ±50 context" on an expanded log row stores the pivot row here;
  // LogContextModal fetches the before/after halves and renders
  // the chronological strip.
  const [contextPivot, setContextPivot] = useState<import('@/lib/types').LogRow | null>(null);
  // Live tail (HyperDX-style): poll, prepend new rows. Cadence is 10s — the
  // ≥10s polling budget, and at the operator's ES scale the ingest pipeline
  // (collector batch + ES exporter flush + index refresh) lags ~10s, so a
  // faster poll just burns ES queries without surfacing data any sooner.
  // (v0.7.17 — the v0.7.15 SSE push tailer was reverted: its LIMIT-per-tick
  // fetch dropped logs on a busy service at 10B logs/day.)
  const [live, setLive] = useState(false);

  // Sync filter state from URL params. Covers (a) static-prerender →
  // CSR hydration, where useState initializes against empty
  // searchParams; (b) in-app navigations that update the URL without
  // remounting the page. Anomaly + service drill-down links rely on
  // this — they pass ?service=<svc>&q=<token> and expect the page to
  // land already scoped.
  //
  // Sig-guarded (Discover revamp step 1): the page now WRITES filter
  // params back to the URL (apply / pill actions / clearTraceLock),
  // and useUrlRange writes ?range=. Without the guard, every URL
  // write re-ran this import and wiped locally-applied state (a
  // range change used to clear an applied-but-not-in-URL filter).
  // The sig hashes only the filter-bearing params, so range-only
  // changes no-op and self-writes (which pre-store their own sig)
  // don't double-apply.
  const urlSig = (f: { service: string; cluster: string; search: string; traceId: string; spanId: string },
    filtersRaw: string, colsRaw: string) =>
    JSON.stringify([f.service, f.cluster, f.search, f.traceId, f.spanId, filtersRaw, colsRaw]);
  const lastUrlSigRef = useRef<string | null>(null);
  useEffect(() => {
    const filtersRaw = searchParams.get('filters') ?? '';
    const colsRaw = searchParams.get('cols') ?? '';
    const next = {
      service:  searchParams.get('service') ?? '',
      cluster:  searchParams.get('cluster') ?? '',
      search:   searchParams.get('q') ?? searchParams.get('search') ?? '',
      severity: 0,
      traceId:  searchParams.get('traceId') ?? '',
      spanId:   searchParams.get('spanId')  ?? '',
    };
    const sig = urlSig(next, filtersRaw, colsRaw);
    if (sig === lastUrlSigRef.current) return;
    lastUrlSigRef.current = sig;
    setFilter(next);
    setDraft(next);
    setFilters(parseFiltersParam(filtersRaw));
    // Deep-linked columns override the view; absence means "keep
    // whatever the operator prefers" (localStorage init), so a plain
    // /logs link never resets their column setup.
    if (colsRaw) setLogCols(colsRaw.split(',').filter(Boolean));
    resetPaging();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [searchParams]);

  // State → URL. Writes every filter-bearing param (replace:true — a
  // filter tweak refines the view, no history entry per click) and
  // pre-stores the sig so the import effect above treats the
  // resulting searchParams change as a no-op.
  const writeUrl = (f: typeof filter, pills: LogFilter[], cols?: string[]) => {
    const filtersRaw = encodeFiltersParam(pills);
    const colsRaw = colsParam(cols ?? logCols);
    lastUrlSigRef.current = urlSig(f, filtersRaw, colsRaw);
    setSearchParams(prev => {
      const p = new URLSearchParams(prev);
      const setOrDel = (k: string, v: string) => { if (v) p.set(k, v); else p.delete(k); };
      setOrDel('service', f.service);
      setOrDel('cluster', f.cluster);
      setOrDel('q', f.search);
      p.delete('search'); // legacy alias of q — never write both
      setOrDel('traceId', f.traceId);
      setOrDel('spanId', f.spanId);
      setOrDel('filters', filtersRaw);
      setOrDel('cols', colsRaw);
      return p;
    }, { replace: true });
  };

  // Column mutations: state + standing preference + URL in one step.
  const changeCols = (next: string[]) => {
    setLogCols(next);
    setRaw(COLS_STORE_KEY, JSON.stringify(next));
    writeUrl(filter, filters, next);
  };
  const removeColumn = (id: string) => changeCols(logCols.filter(c => c !== id));
  const toggleColumn = (id: string) =>
    changeCols(logCols.includes(id) ? logCols.filter(c => c !== id) : [...logCols, id]);

  // Build the params for the static-window query. When live
  // tail is on, we don't run this query (the live useQuery
  // below takes over instead) — `enabled: !live` gates it.
  //
  // CRITICAL: `from` / `to` are computed via timeRangeToNs which
  // reads Date.now() for non-custom presets. Without memoising,
  // every render produces a NEW from/to (Date.now() advanced by
  // a few ms), the React Query key hashes differently, RQ
  // starts a fresh query and discards the previous — isLoading
  // stays true forever and the page is stuck on the skeleton.
  // Memoise on the range / traceId-filter so the values only
  // refresh when the operator actually changes the inputs.
  const useTimeRange = !filter.traceId;
  const { from, to } = useMemo(
    () => useTimeRange ? timeRangeToNs(range) : { from: undefined, to: undefined },
    [useTimeRange, range],
  );
  // Pills + free text → the single query string every consumer
  // (table, facets, histogram, live tail, Kibana link) sends to the
  // backend. Disabled pills drop out here — no backend round-trip
  // knows pills exist.
  const compiledSearch = useMemo(
    () => compileSearch(filters, filter.search),
    [filters, filter.search],
  );
  // <mark> terms for the message cell — the APPLIED free-text
  // query's bare terms + quoted phrases only (field clauses and
  // pills excluded; a level:error clause must not light unrelated
  // "error" text). Client-side matching only.
  const highlightTerms = useMemo(
    () => extractHighlightTerms(filter.search),
    [filter.search],
  );
  // Slice for the fields-panel accordion fetches — stable identity
  // so the panel's useQuery keys only change when the slice does.
  const fieldStatsScope = useMemo(() => ({
    from, to,
    service: filter.service || undefined,
    cluster: filter.cluster || undefined,
    search: compiledSearch || undefined,
    severity: filter.severity > 0 ? filter.severity : undefined,
    traceId: filter.traceId || undefined,
    spanId: filter.spanId || undefined,
  }), [from, to, filter.service, filter.cluster, compiledSearch, filter.severity, filter.traceId, filter.spanId]);
  const staticQ = useLogs({
    limit: 100, after: cursor || undefined, from, to,
    service: filter.service || undefined,
    cluster: filter.cluster || undefined,
    search: compiledSearch || undefined,
    severity: filter.severity > 0 ? filter.severity : undefined,
    traceId: filter.traceId || undefined,
    spanId:  filter.spanId  || undefined,
  });

  // Level-facet counts. A per-severity timeseries query feeds the
  // toolbar facet chip badges (the duplicate Logs-local stacked-bar
  // volume row was removed in v0.8.115 — the Elastic-style
  // LogsHistogram below is the one volume viz). Scoped
  // to the current applied filter (service / search / trace) and
  // the active window, but deliberately WITHOUT severity — the
  // chips must show counts for every level, otherwise selecting
  // ERROR would zero out the other chips (Kibana/Datadog facet
  // behaviour). `from`/`to` come from the already-memoised window
  // (no bare timeRangeToNs — v0.5.184). Needs a bounded window OR a
  // trace pin; the query key carries from/to so a preset's Date.now
  // drift doesn't thrash it (the parent memo already froze them).
  const volumeEnabled = (from !== undefined && to !== undefined) || !!filter.traceId;
  const volumeBucket = useMemo(() => pickVolumeBucket(from, to), [from, to]);
  const volumeQ = useQuery({
    queryKey: ['logs', 'sev-volume', from, to, filter.service, compiledSearch, filter.traceId, volumeBucket],
    queryFn: () => api.logsTimeseries({
      from, to,
      service: filter.service || undefined,
      search:  compiledSearch || undefined,
      traceId: filter.traceId || undefined,
      groupBy: 'severity',
      bucketSec: volumeBucket,
    }),
    enabled: volumeEnabled,
    staleTime: 30_000,
  });
  const sevSeries: SevSeries[] = volumeQ.data ?? [];
  // Per-chip counts (summed across all buckets), keyed by facet.
  const facetCounts = useMemo(() => {
    const c: Record<string, number> = { all: 0, error: 0, warn: 0, info: 0, debug: 0 };
    for (const s of sevSeries) {
      const facet = bandToFacet(s.name);
      const sum = s.points.reduce((a, p) => a + p.v, 0);
      c[facet] += sum;
      c.all += sum;
    }
    return c;
  }, [sevSeries]);

  // Live-tail (v0.8.x) — server-pushed SSE replaces the old 10s poll. The
  // table renders a bounded, newest-first client buffer fed by
  // /api/logs/stream: each `event: log` prepends a row (dedup by id, capped
  // at LIVE_CAP); an `event: gap` flags that a busy service outran the
  // per-tick read. The stream closes on document.hidden and reopens on show,
  // catching up via `since` = the newest row we hold (mirrors the SSE
  // reconnect catch-up shipped in eventStream.ts). A filter change starts a
  // fresh stream.
  const LIVE_CAP = 1000;
  const [liveBuffer, setLiveBuffer] = useState<LogRow[]>([]);
  const [liveGap, setLiveGap] = useState(false);
  const newestNsRef = useRef(0);

  useEffect(() => {
    if (!live || typeof EventSource === 'undefined') return;
    setLiveBuffer([]); setLiveGap(false); newestNsRef.current = 0;
    let es: EventSource | null = null;
    const open = () => {
      const p = new URLSearchParams();
      if (filter.service) p.set('service', filter.service);
      if (filter.cluster) p.set('cluster', filter.cluster);
      if (compiledSearch) p.set('search', compiledSearch);
      if (filter.severity > 0) p.set('severity', String(filter.severity));
      if (newestNsRef.current) p.set('since', String(newestNsRef.current)); // reconnect catch-up
      es = new EventSource('/api/logs/stream?' + p.toString(), { withCredentials: true });
      es.addEventListener('log', (e) => {
        let row: LogRow;
        try { row = JSON.parse((e as MessageEvent).data) as LogRow; } catch { return; }
        if (row.timestamp > newestNsRef.current) newestNsRef.current = row.timestamp;
        setLiveBuffer(buf => {
          if (buf.some(r => r.id === row.id)) return buf; // dedup (boundary / reconnect overlap)
          const next = [row, ...buf];
          if (next.length > LIVE_CAP) next.length = LIVE_CAP;
          return next;
        });
      });
      es.addEventListener('gap', () => setLiveGap(true));
      // EventSource auto-reconnects on transport 'error'; the next 'open'
      // catches up forward via newestNsRef — nothing to do here.
    };
    open();
    const onVis = () => {
      if (document.hidden) { es?.close(); es = null; }
      else if (!es) { open(); }
    };
    document.addEventListener('visibilitychange', onVis);
    return () => {
      document.removeEventListener('visibilitychange', onVis);
      es?.close();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [live, filter.service, filter.cluster, compiledSearch, filter.severity]);

  // When live, the table renders the SSE buffer; otherwise the static
  // windowed query. Live has no loading/error gate — rows fill in as they
  // arrive (an empty buffer shows the "waiting" empty state).
  const data: LogsResponse | undefined | null = live
    ? { total: liveBuffer.length, logs: liveBuffer, nextCursor: '' }
    : staticQ.isLoading ? undefined
    : staticQ.isError ? null
    : staticQ.data;

  // Accumulate fetched pages into accRows (v0.8.260 Load more).
  // First page replaces (covers fresh views AND a staleTime
  // refetch of page 1); later cursors append with id-dedup so the
  // keyset boundary can't duplicate a row. isPlaceholderData
  // guards against ingesting the keep-previous stand-in while the
  // next page is still in flight.
  useEffect(() => {
    if (live || !staticQ.data || staticQ.isPlaceholderData) return;
    const page = staticQ.data.logs ?? [];
    setAccRows(prev => {
      if (!cursor) return page;
      const seen = new Set(prev.map(r => r.id));
      return [...prev, ...page.filter(r => !seen.has(r.id))];
    });
  }, [staticQ.data, staticQ.isPlaceholderData, cursor, live]);

  // Reset expansion state when the filter / range changes —
  // opening row #5 in one window doesn't translate to the next.
  // NOT on cursor: Load more appends below, existing expansions
  // must survive the append.
  useEffect(() => { setExpanded(new Set()); }, [range, filter, filters]);

  // Backend mapping fields (v0.5.136 → fields panel v0.8.255).
  // Feeds the left LogFieldsPanel's "Available fields" group. CH
  // backend returns an empty list (its shape is fixed).
  const [fields, setFields] = useState<string[]>([]);

  // Kibana deep-link config (v0.5.236). Loaded once on mount;
  // when disabled or unconfigured, buildKibanaURL returns null
  // and the button doesn't render.
  const [kibana, setKibana] = useState<KibanaSettings | null>(null);
  useEffect(() => {
    api.getKibanaSettings()
      .then(s => setKibana(s ?? null))
      .catch(() => setKibana(null));
  }, []);
  useEffect(() => {
    api.logsFields()
      .then(d => { setFields(d.fields ?? []); })
      .catch(() => { setFields([]); });
  }, []);
  const apply = () => { resetPaging(); setFilter(draft); writeUrl(draft, filters); };
  const reset = () => {
    const empty = { service: '', cluster: '', search: '', severity: 0, traceId: '', spanId: '' };
    setDraft(empty); setFilter(empty); setFilters([]); resetPaging();
    writeUrl(empty, []);
  };

  // Commit a new pill set: state + paging + URL in one step. Every
  // pill mutation (add/negate/disable/remove) is an auto-apply —
  // same as the old toggleSearchClause behaviour.
  const applyPills = (next: LogFilter[]) => {
    setFilters(next);
    resetPaging();
    writeUrl(filter, next);
  };
  const negatePill  = (i: number) => applyPills(filters.map((f, j) => (j === i ? { ...f, negated: !f.negated } : f)));
  const disablePill = (i: number) => applyPills(filters.map((f, j) => (j === i ? { ...f, disabled: !f.disabled } : f)));
  const removePill  = (i: number) => applyPills(filters.filter((_, j) => j !== i));

  // Expanded-row KvRow click-to-filter handlers (v0.5.229 →
  // Discover pills). Operator clicks ⊕ on any attribute → adds a
  // pill; ⊖ → adds a negated pill. Toggle semantics (same polarity
  // removes, opposite flips) live in lib/logFilters.ts.
  const addFromRow      = (key: string, value: string) => applyPills(toggleFilter(filters, key, value, false));
  const excludeFromRow  = (key: string, value: string) => applyPills(toggleFilter(filters, key, value, true));
  const clearTraceLock = () => {
    const next = { ...filter, traceId: '', spanId: '' };
    setFilter(next); setDraft(d => ({ ...d, traceId: '', spanId: '' }));
    writeUrl(next, filters);
  };
  const toggle = (id: number) => {
    const next = new Set(expanded);
    if (next.has(id)) next.delete(id); else next.add(id);
    setExpanded(next);
  };

  // Static view renders the ACCUMULATED rows; the raw page in
  // `data` only feeds total/nextCursor/empty-state checks. Falls
  // back to the page rows in the one render before the
  // accumulator effect fills (or while a placeholder shows the
  // previous slice during a filter change).
  const logs = live ? (data?.logs ?? []) : (accRows.length > 0 ? accRows : (data?.logs ?? []));
  const total = data?.total ?? 0;

  // j/k row navigation — same pattern as /services and /traces.
  // Enter / o on the selected row toggles the expansion
  // (matches the existing click behaviour), Esc clears the
  // selection. The hook scrolls the active row into view via
  // [data-row-idx], which we set on the LogRowR below.
  const tableNav = useTableNav<LogRow>(logs, {
    onOpen: (l) => toggle(l.id),
    pageId: 'logs',
  });

  return (
    <>
      {/* Changing the time range MUST reset the keyset cursor: a token
          from the old window encodes time < staleCursorTime, so paging
          into a new (wider/shifted) window with a stale cursor silently
          drops every row newer than it from page 1. resetPaging mirrors
          the apply/reset/search/URL-sync handlers. (v0.7.81 fix) */}
      <Topbar title="Logs" range={range} onRangeChange={(r) => { setRange(r); resetPaging(); }} />
      <div id="content">
        <SavedViewsBar page="logs" />
        {filter.traceId && (
          <div className="trace-lock">
            <span>Filtered to trace</span>
            <code>{filter.traceId}</code>
            {filter.spanId && (<>
              <span>· span</span>
              <code>{filter.spanId}</code>
            </>)}
            <button className="sec" onClick={clearTraceLock}>✕ Clear</button>
          </div>
        )}
        <div className="controls">
          <ServicePicker value={draft.service} onChange={v => setDraft({ ...draft, service: v })}
            placeholder="Service…" width={170} onEnter={apply} />
          {/* v0.5.471 — cluster selector. Populated from
              /api/clusters (existing endpoint); typically 1-5
              entries per install so a plain <select> is the
              right shape — no server-debounced picker needed.
              Empty option = all clusters. */}
          {clusters.length > 0 && (
            <select value={draft.cluster}
              onChange={e => { setDraft(d => ({ ...d, cluster: e.target.value })); }}
              title="Filter logs to a single k8s/openshift cluster"
              style={{ width: 160 }}>
              <option value="">All clusters</option>
              {clusters.map(c => <option key={c} value={c}>{c}</option>)}
            </select>
          )}
          <KqlSearchInput
            value={draft.search}
            onChange={v => setDraft({ ...draft, search: v })}
            onSubmit={apply}
            placeholder='Search… (KQL: level:error AND service.name:"checkout")'
            title={'Free-text on body OR KQL/Lucene syntax (Elasticsearch backend).\n\n' +
              'Examples:\n' +
              '  level:error\n' +
              '  service.name:"checkout-svc" AND NOT message:health\n' +
              '  trace.id:c9ea*\n' +
              '  message:"connection refused" AND k8s.namespace:prod\n\n' +
              'Plain words match the body. Use double quotes for exact phrases.\n\n' +
              'Field-aware autocomplete (v0.5.464): type `field:` and pick from the dropdown.'} />
          {/* Trace ID filter — dedicated input next to search so
              operators can paste a trace ID from a problem /
              incident and see only its log lines. Mirrors the
              ?traceId= URL param the deep-link routes already
              use. The backend filter is an exact term match
              against trace.id (ES) / trace_id (CH). Trimmed +
              lowercased so a paste of `0xABC…` or whitespace
              padding still works. */}
          <input
            placeholder="Trace ID"
            aria-label="Filter logs by trace ID"
            value={draft.traceId}
            onChange={e => setDraft({ ...draft, traceId: e.target.value.trim().toLowerCase().replace(/^0x/, '') })}
            onKeyDown={e => e.key === 'Enter' && apply()}
            title="Filter logs to a single trace. Time range is ignored when this is set — searches across full retention."
            className="mono"
            style={{ width: 180, fontSize: 12 }} />
          <button onClick={apply}>Search</button>
          <button className="sec" onClick={reset}>Reset</button>
          <LogShareButton />
          <button className={live ? 'live-on' : 'sec'}
            onClick={() => setLive(v => !v)}
            style={{ marginLeft: 'auto' }}
            title="Stream the latest logs live (server-pushed; pauses when the tab is hidden)">
            {live ? '⏸ Pause Live' : '▶ Live tail'}
          </button>
          {live && liveGap && (
            <span className="badge b-warn" title="A busy service produced more lines than one tick could read — narrow the filter to see them all.">
              ⚠ high volume — some lines skipped
            </span>
          )}
          {/* External Kibana deep-link (v0.5.236). Hidden unless
              the admin has filled in Settings → Kibana link.
              Carries the current filter context — service /
              trace-id / search clauses become KQL; window
              becomes the time range — so the operator lands on
              the same slice they're looking at here. */}
          {(() => {
            const kql = buildKQLFromFilter({ ...filter, search: compiledSearch });
            const href = buildKibanaURL(kibana, {
              fromNs: from ?? undefined,
              toNs: to ?? undefined,
              kql,
            });
            if (!href) return null;
            return (
              <a href={href} target="_blank" rel="noopener"
                className="sec"
                title="Open the current filter slice in Kibana Discover"
                style={{
                  fontSize: 12, padding: '5px 12px',
                  textDecoration: 'none', color: 'var(--accent2)',
                }}>
                ↗ Discover in Kibana
              </a>
            );
          })()}
        </div>

        {/* Filter pill bar (Discover revamp step 1). One pill per
            structured field filter; free text stays in the search
            box. ≠ toggles NOT (red tone), ◐ disables without
            removing (opacity + line-through, drops out of the
            compiled query), × removes. All actions auto-apply. */}
        {filters.length > 0 && (
          <div role="group" aria-label="Active field filters"
            style={{ display: 'flex', alignItems: 'center', flexWrap: 'wrap', gap: 6, marginBottom: 10 }}>
            {filters.map((f, i) => {
              const tone = f.negated ? 'var(--err)' : 'var(--accent2)';
              return (
                <span key={`${f.key}\u0000${f.value}`} style={{
                  display: 'inline-flex', alignItems: 'center', gap: 5,
                  padding: '3px 6px 3px 9px', borderRadius: 4, fontSize: 11.5,
                  border: `1px solid ${f.negated ? 'var(--err)' : 'var(--border)'}`,
                  background: f.negated ? 'transparent' : 'var(--accent-soft)',
                  opacity: f.disabled ? 0.5 : 1,
                }}>
                  <span style={{
                    fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
                    color: tone,
                    textDecoration: f.disabled ? 'line-through' : 'none',
                  }}>
                    {f.negated && <b>NOT </b>}{f.key}: {f.value}
                  </span>
                  <Button variant="ghost" size="sm" style={{ color: f.negated ? 'var(--err)' : undefined }}
                    onClick={() => negatePill(i)}
                    title={f.negated ? 'Include (drop the NOT)' : 'Negate — exclude matching logs'}>≠</Button>
                  <Button variant="ghost" size="sm"
                    onClick={() => disablePill(i)}
                    title={f.disabled ? 'Re-enable this filter' : 'Temporarily disable (keeps the pill)'}>◐</Button>
                  <Button variant="ghost" size="sm"
                    onClick={() => removePill(i)}
                    title="Remove this filter">×</Button>
                </span>
              );
            })}
            <Button variant="secondary" size="sm"
              onClick={() => applyPills([])}
              title="Remove all field filters (free-text search stays)">
              Clear all
            </Button>
          </div>
        )}

        {/* Level facet chips (prototype LogsView .logbar/.facet/.lvl).
            Each chip drives the EXISTING min-severity filter
            (filter.severity) and carries a live count from the
            per-severity timeseries query. Clicking a chip commits
            its severity floor immediately + resets paging (facet =
            a filter action, Kibana-style); clicking the active chip
            (or All) returns to All severities. Active chip = accent
            ring. The level label renders as the canonical severity
            badge so the operator's colour memory carries over from
            the table + histogram. (Replaces the old severity
            <select> in the toolbar.) */}
        {(() => {
          // The active chip = the highest-severity chip whose floor
          // is ≤ the current severity floor, so e.g. severity=17
          // lights ERROR, severity=9 lights INFO. 0 = All.
          const activeKey = filter.severity <= 0 ? 'all'
            : (LVL_FACETS.find(f => filter.severity >= f.min)?.key ?? 'all');
          const setSeverity = (min: number) => {
            const next = min === filter.severity ? 0 : min; // toggle off → All
            setFilter(f => ({ ...f, severity: next }));
            setDraft(d => ({ ...d, severity: next }));
            resetPaging();
          };
          const chipBase: CSSProperties = {
            display: 'inline-flex', alignItems: 'center', gap: 6,
            padding: '5px 10px', borderRadius: 20, fontSize: 11.5,
            fontWeight: 600, cursor: 'pointer', userSelect: 'none',
            border: '1px solid var(--border)', background: 'var(--bg1)',
            color: 'var(--text2)', lineHeight: 1.4,
          };
          const onStyle: CSSProperties = {
            borderColor: 'var(--accent)', color: 'var(--accent2)',
            background: 'var(--accent-soft)',
          };
          const Chip = ({
            keyName, children, count, title,
          }: { keyName: string; children: ReactNode; count: number; title: string }) => {
            const on = activeKey === keyName;
            return (
              <span role="button" tabIndex={0}
                aria-pressed={on}
                title={title}
                onClick={() => setSeverity(keyName === 'all' ? 0 : LVL_FACETS.find(f => f.key === keyName)!.min)}
                onKeyDown={e => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); setSeverity(keyName === 'all' ? 0 : LVL_FACETS.find(f => f.key === keyName)!.min); } }}
                style={{ ...chipBase, ...(on ? onStyle : {}) }}>
                {children}
                <span style={{
                  fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
                  fontSize: 10.5, color: on ? 'var(--accent2)' : 'var(--text3)',
                }}>
                  {volumeQ.isLoading ? '·' : count.toLocaleString()}
                </span>
              </span>
            );
          };
          return (
            <div role="group" aria-label="Filter by log level"
              style={{
                display: 'flex', alignItems: 'center', gap: 8,
                flexWrap: 'wrap', marginBottom: 12,
              }}>
              <Chip keyName="all" count={facetCounts.all}
                title="Show all severities">
                <span style={{ color: activeKey === 'all' ? 'var(--accent2)' : 'var(--text2)' }}>All</span>
              </Chip>
              {LVL_FACETS.map(f => (
                <Chip key={f.key} keyName={f.key} count={facetCounts[f.key] ?? 0}
                  title={`Show ${f.label} and above (min severity ${f.min})`}>
                  <span className={`badge ${
                    f.key === 'error' ? 'b-err'
                    : f.key === 'warn' ? 'b-warn'
                    : f.key === 'info' ? 'b-info'
                    : 'b-gray'}`}>{f.label}</span>
                </Chip>
              ))}
              {!volumeEnabled && (
                <span style={{ fontSize: 11, color: 'var(--text3)' }}>
                  counts need a bounded time range
                </span>
              )}
              {volumeQ.isError && (
                <span style={{ fontSize: 11, color: 'var(--err)' }}>
                  level counts unavailable
                </span>
              )}
            </div>
          );
        })()}

        {/* Fields panel (Discover revamp step 2) to the left of the
            histogram + table. Replaces the old "ƒ Fields" chip row —
            same discovery source (/api/logs/fields), but with
            Selected/Available grouping, per-field top-5 accordion
            (lazy fieldstats fetch on expand only), pill actions and
            add/remove-column. The right side keeps everything that
            was here before, unchanged. */}
        <div style={{ display: 'flex', gap: 12, alignItems: 'flex-start' }}>
          <LogFieldsPanel
            fields={fields}
            columns={logCols}
            scope={fieldStatsScope}
            onToggleColumn={toggleColumn}
            onPillAdd={addFromRow}
            onPillExclude={excludeFromRow} />
          <div style={{ flex: 1, minWidth: 0 }}>

        {/* Severity-stacked histogram (v0.5.235) — spike of errors
            stands out against the background INFO traffic without
            reading the count column. Hidden when neither a time
            range nor a trace pin is set; renders nothing on
            empty data. */}
        <LogsHistogram range={{ from, to }} filter={{ ...filter, search: compiledSearch }}
          onRangeSelect={(fromNs, toNs) => {
            // Brush → narrow the window to the selection. Custom
            // ranges ride ?range= (useUrlRange) so the zoom is
            // shareable; paging resets per the v0.7.81 cursor rule.
            setRange({ preset: 'custom', fromMs: Math.round(fromNs / 1e6), toMs: Math.round(toNs / 1e6) });
            resetPaging();
          }} />

        {data === undefined && <TableSkeleton rows={12} cols={5} />}
        {data && logs.length === 0 && (
          filter.traceId ? (
            <Empty icon="≡" title="No logs match this trace">
              The trace exists in Coremetry, but the logs backend has no
              record of it. Two common reasons:
              <ul style={{ marginTop: 8, paddingLeft: 18, lineHeight: 1.6 }}>
                <li>The application emitted no log lines while this trace was active.</li>
                <li>The log shipper (Filebeat / OTel Collector ES exporter / etc.) hadn't started yet when the trace ran, so the log was never indexed.</li>
              </ul>
              {filter.spanId && <>You also filtered by span — try <a href="#" onClick={e => { e.preventDefault(); const next = { ...filter, spanId: '' }; setFilter(next); setDraft(d => ({ ...d, spanId: '' })); }}>removing the span filter</a> to see all logs for the trace.</>}
            </Empty>
          ) : (
            <Empty icon="≡" title="No logs found">
              <div style={{ marginTop: 6, color: 'var(--text2)' }}>
                Widen the time range, drop the service/cluster filter, or
                relax the severity floor. If unfiltered queries are also
                empty, the logs backend (<code>COREMETRY_LOGS_BACKEND</code>)
                may be misconfigured — check <a href="/status" style={{ color: 'var(--accent2)' }}>/status</a>.
              </div>
            </Empty>
          )
        )}
        {data && logs.length > 0 && (
          <>
            <LogTable logs={logs} nav={tableNav}
              columns={logCols}
              onRemoveColumn={removeColumn}
              highlightTerms={highlightTerms}
              expandedIds={expanded}
              onToggleExpand={toggle}
              onFilterAdd={addFromRow}
              onFilterExclude={excludeFromRow}
              onTracePeek={tid => setPeekTraceId(tid)}
              onContextOpen={l => setContextPivot(l)} />
            {/* Load more (v0.8.260 — replaced the Back/Next pager;
                keyset cursor mechanics unchanged underneath). Rows
                accumulate in accRows; the button advances the cursor
                to the response's nextCursor and the new page appends.
                Hidden during live tail (the live buffer owns its own
                moving window). Button-first per spec — an
                IntersectionObserver auto-load can layer on once the
                behaviour settles (and it would multiply backend
                queries, which the ES-usage constraint caps). */}
            {!live && (
              <div className="row" style={{
                display: 'flex', alignItems: 'center', gap: 10,
                marginTop: 10, fontSize: 12, color: 'var(--text3)',
              }}>
                {data.nextCursor && (
                  <Button variant="secondary" size="sm"
                    disabled={staticQ.isFetching}
                    onClick={() => { if (data.nextCursor) setCursor(data.nextCursor); }}
                    title="Fetch the next page and append it below">
                    {staticQ.isFetching ? 'Loading…' : '↓ Load more'}
                  </Button>
                )}
                <span>
                  showing {logs.length.toLocaleString()} of {total.toLocaleString()}
                </span>
              </div>
            )}
          </>
        )}
          </div>
        </div>
      </div>
      <CorrelationContextDrawer
        anchor={peekTraceId ? { kind: 'trace', traceId: peekTraceId } : null}
        onClose={() => setPeekTraceId(null)} />
      <LogContextModal pivot={contextPivot}
        onClose={() => setContextPivot(null)}
        onTracePeek={tid => { setContextPivot(null); setPeekTraceId(tid); }} />
    </>
  );
}

// LogRowR moved to components/LogTable.tsx (shared between
// /logs and the trace detail Logs tab).

export default function LogsPage() {
  return (
    <Suspense fallback={<Spinner />}>
      <LogsInner />
    </Suspense>
  );
}
