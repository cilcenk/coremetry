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
import { LogTable } from '@/components/LogTable';
import { TracePeekDrawer } from '@/components/TracePeekDrawer';
import { LogContextModal } from '@/components/LogContextModal';
import { LogsHistogram } from '@/components/LogsHistogram';
import { Button } from '@/components/ui/Button';
import { buildKibanaURL } from '@/lib/kibanaLink';
import type { KibanaSettings } from '@/lib/types';
import { useLogs } from '@/lib/queries';
import { useUrlRange } from '@/lib/useUrlRange';
import { useTableNav } from '@/lib/useTableNav';
import { api } from '@/lib/api';
import { tsShort, timeRangeToNs, sevName, sevClass } from '@/lib/utils';
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
    <button type="button" className="sec" onClick={copy}
      title="Copy a shareable link to this filtered logs view (filters are encoded in the URL; recipients sign in to Coremetry to open it)"
      style={{ fontSize: 11, padding: '3px 8px', color: copied ? 'var(--ok)' : undefined }}>
      {copied ? '✓ Copied' : '⧉ Copy link'}
    </button>
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
  const [searchParams] = useSearchParams();

  const [range, setRange] = useUrlRange('30m');
  // Cursor paging (v0.7.22 — replaced the offset pager). `cursor`
  // is the opaque `after` token for the page currently displayed
  // ('' = first page). `cursorStack` holds the cursors of the
  // pages we've walked past, so Back can pop one and refetch.
  // Reset both whenever the filter / range / query changes (see
  // resetPaging below + the apply/reset/URL-sync handlers).
  const [cursor, setCursor] = useState('');
  const [cursorStack, setCursorStack] = useState<string[]>([]);
  // Reset to the first page. Called whenever the query inputs
  // change (filter / range / search) — a cursor from the old
  // result set is meaningless against a new one.
  const resetPaging = () => { setCursor(''); setCursorStack([]); };
  const [filter, setFilter] = useState({
    service: '', cluster: '', search: '', severity: 0, traceId: '', spanId: '',
  });
  const [draft, setDraft] = useState(filter);
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
  // v0.5.399 — trace peek drawer state. Clicking the "👁" button
  // next to a trace_id in the log row sets this; TracePeekDrawer
  // fetches the trace summary + sibling logs and renders inline
  // without disturbing the page's existing filter/search state.
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

  // Sync filter state from URL params on every URL change. This
  // covers two cases that useState's lazy init does not handle
  // reliably: (a) static-prerender → CSR hydration, where useState
  // initializes against empty searchParams during SSG and never
  // re-runs even though the client sees real params; (b) in-app
  // navigations that update the URL without remounting the page.
  // Anomaly + service drill-down links rely on this — they pass
  // ?service=<svc>&q=<token> and expect the page to land already
  // scoped instead of showing the global all-logs view.
  useEffect(() => {
    const next = {
      service:  searchParams.get('service') ?? '',
      cluster:  searchParams.get('cluster') ?? '',
      search:   searchParams.get('q') ?? searchParams.get('search') ?? '',
      severity: 0,
      traceId:  searchParams.get('traceId') ?? '',
      spanId:   searchParams.get('spanId')  ?? '',
    };
    setFilter(next);
    setDraft(next);
    resetPaging();
  }, [searchParams]);

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
  const staticQ = useLogs({
    limit: 100, after: cursor || undefined, from, to,
    service: filter.service || undefined,
    cluster: filter.cluster || undefined,
    search: filter.search || undefined,
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
    queryKey: ['logs', 'sev-volume', from, to, filter.service, filter.search, filter.traceId, volumeBucket],
    queryFn: () => api.logsTimeseries({
      from, to,
      service: filter.service || undefined,
      search:  filter.search  || undefined,
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
      if (filter.search) p.set('search', filter.search);
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
  }, [live, filter.service, filter.cluster, filter.search, filter.severity]);

  // When live, the table renders the SSE buffer; otherwise the static
  // windowed query. Live has no loading/error gate — rows fill in as they
  // arrive (an empty buffer shows the "waiting" empty state).
  const data: LogsResponse | undefined | null = live
    ? { total: liveBuffer.length, logs: liveBuffer, nextCursor: '' }
    : staticQ.isLoading ? undefined
    : staticQ.isError ? null
    : staticQ.data;

  // Reset expansion state when the filter / range / page
  // changes — opening row #5 in one window doesn't translate
  // to the next. `cursor` stands in for the page identity now.
  useEffect(() => { setExpanded(new Set()); }, [range, filter, cursor]);

  // Field-mapping hint (v0.5.136 / v0.5.137). On the Elastic
  // backend, surface what fields are queryable so the operator
  // doesn't have to guess what their index mapping exposes. CH
  // backend returns an empty list (its shape is fixed and
  // already documented in the placeholder).
  const [fields, setFields] = useState<string[]>([]);
  const [showFieldsHint, setShowFieldsHint] = useState(false);

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
  // Insert "field:" into the search box at cursor / end. Auto-
  // focuses so the operator can type the value immediately.
  const insertField = (f: string) => {
    const cur = draft.search;
    const sep = cur && !cur.endsWith(' ') ? ' AND ' : '';
    setDraft({ ...draft, search: `${cur}${sep}${f}:` });
    // Move keyboard focus back to the search box. v0.7.46 — KqlSearchInput is
    // now a <textarea> (wraps long KQL queries), so select the textarea.
    requestAnimationFrame(() => {
      const el = document.querySelector<HTMLTextAreaElement>('textarea[placeholder^="Search…"]');
      el?.focus();
      el?.setSelectionRange(el.value.length, el.value.length);
    });
  };

  const apply = () => { resetPaging(); setFilter(draft); };
  const reset = () => {
    const empty = { service: '', cluster: '', search: '', severity: 0, traceId: '', spanId: '' };
    setDraft(empty); setFilter(empty); resetPaging();
  };

  // Append/remove a `key:value` clause from the search box.
  // `negate=true` produces a `NOT key:value` clause. Used by both
  // the facet sidebar and the expanded-row KvRow click-to-filter
  // buttons. Auto-applies (commits filter + resets page).
  const toggleSearchClause = (key: string, value: string, negate = false) => {
    const escapeRe = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    // Always wrap values in double quotes — Lucene treats many
    // characters as operators (`-`, `/`, `:`, `*`, etc.) and a
    // bare hostname like "my-host-7f-abc" is parsed as a boolean
    // expression rather than a literal. Inside quotes only `\`
    // and `"` are special, which we escape. v0.5.230 caught a
    // `host.hostname:my-host-abc` filter never matching.
    const phraseQuote = (s: string) =>
      `"${s.replace(/\\/g, '\\\\').replace(/"/g, '\\"')}"`;
    const k = escapeRe(key);
    const v = escapeRe(value);
    // Match the existing clause (with or without NOT, with or
    // without surrounding quotes). One regex finds both
    // positive + negative forms so an exact ⊕ → ⊖ toggle works
    // without duplicate clauses piling up.
    const re = new RegExp(`(?:^|\\s+AND\\s+|\\s+)(?:NOT\\s+)?${k}:"?${v}"?`);
    let nextSearch: string;
    if (re.test(filter.search)) {
      const alreadyNegated = new RegExp(`(?:^|\\s+AND\\s+|\\s+)NOT\\s+${k}:"?${v}"?`).test(filter.search);
      nextSearch = filter.search.replace(re, ' ')
        .replace(/\s+AND\s+AND\s+/g, ' AND ').trim()
        .replace(/^AND\s+/, '').replace(/\s+AND$/, '');
      if (alreadyNegated !== negate) {
        const sep = nextSearch ? ' AND ' : '';
        nextSearch = `${nextSearch}${sep}${negate ? 'NOT ' : ''}${key}:${phraseQuote(value)}`;
      }
    } else {
      const sep = filter.search ? ' AND ' : '';
      nextSearch = `${filter.search}${sep}${negate ? 'NOT ' : ''}${key}:${phraseQuote(value)}`;
    }
    const next = { ...filter, search: nextSearch };
    setDraft(d => ({ ...d, search: nextSearch }));
    setFilter(next);
    resetPaging();
  };

  // Expanded-row KvRow click-to-filter handlers (v0.5.229).
  // Operator clicks ⊕ on any attribute / resource attribute →
  // adds key:value to search. ⊖ → adds NOT key:value.
  const addFromRow      = (key: string, value: string) => toggleSearchClause(key, value, false);
  const excludeFromRow  = (key: string, value: string) => toggleSearchClause(key, value, true);
  const clearTraceLock = () => {
    const next = { ...filter, traceId: '', spanId: '' };
    setFilter(next); setDraft(d => ({ ...d, traceId: '', spanId: '' }));
  };
  const toggle = (id: number) => {
    const next = new Set(expanded);
    next.has(id) ? next.delete(id) : next.add(id);
    setExpanded(next);
  };

  const logs = data?.logs ?? [];
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
          {fields.length > 0 && (
            <button type="button" className="sec"
              onClick={() => setShowFieldsHint(v => !v)}
              title={`${fields.length} indexable field${fields.length === 1 ? '' : 's'} discovered from the Elasticsearch mapping. Click to browse + auto-insert into the search.`}
              style={{ fontSize: 11, padding: '3px 8px' }}>
              {showFieldsHint ? '× Fields' : 'ƒ Fields'}
            </button>
          )}
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
            const kql = buildKQLFromFilter(filter);
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

        {/* Field-mapping chips (v0.5.137). Toggled via the ƒ
            Fields button next to the search input. Discovered
            from the Elasticsearch _mapping; clicking a chip
            inserts "field:" at the end of the current search
            with the right AND glue + auto-focuses the input. */}
        {showFieldsHint && fields.length > 0 && (
          <div style={{
            background: 'var(--bg2)', border: '1px solid var(--border)',
            borderRadius: 6, padding: 8, marginBottom: 10,
            fontSize: 11,
          }}>
            <div style={{
              fontWeight: 600, color: 'var(--text2)', marginBottom: 6,
              display: 'flex', alignItems: 'baseline', gap: 8,
            }}>
              <span>Discovered fields</span>
              <span style={{ color: 'var(--text3)', fontWeight: 400 }}>
                {fields.length} field{fields.length === 1 ? '' : 's'} · click to insert "field:" into the search
              </span>
            </div>
            <div style={{ display: 'flex', flexWrap: 'wrap', gap: 4 }}>
              {fields.map(f => (
                <button key={f} type="button"
                  onClick={() => insertField(f)}
                  className="sec"
                  style={{
                    fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
                    fontSize: 11, padding: '2px 6px',
                  }}>
                  {f}
                </button>
              ))}
            </div>
          </div>
        )}

        {/* Severity-stacked histogram (v0.5.235) — spike of errors
            stands out against the background INFO traffic without
            reading the count column. Hidden when neither a time
            range nor a trace pin is set; renders nothing on
            empty data. */}
        <LogsHistogram range={{ from, to }} filter={filter} />

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
              expandedIds={expanded}
              onToggleExpand={toggle}
              onFilterAdd={addFromRow}
              onFilterExclude={excludeFromRow}
              onTracePeek={tid => setPeekTraceId(tid)}
              onContextOpen={l => setContextPivot(l)} />
            {/* Cursor pager (v0.7.22 — replaced the offset Pager).
                Back pops the cursor stack; Next pushes the current
                cursor and advances to the response's nextCursor.
                Next is hidden during live tail (the live query owns
                its own moving window — no cursor). Total still comes
                from the backend so the "N total" label is unchanged. */}
            {!live && (
              <div className="row" style={{
                display: 'flex', alignItems: 'center', gap: 10,
                marginTop: 10, fontSize: 12, color: 'var(--text3)',
              }}>
                <Button variant="secondary" size="sm"
                  disabled={cursorStack.length === 0}
                  onClick={() => {
                    setCursorStack(stack => {
                      const next = stack.slice(0, -1);
                      setCursor(stack[stack.length - 1] ?? '');
                      return next;
                    });
                  }}
                  title="Previous page">
                  ← Back
                </Button>
                <Button variant="secondary" size="sm"
                  disabled={!data.nextCursor}
                  onClick={() => {
                    if (!data.nextCursor) return;
                    setCursorStack(stack => [...stack, cursor]);
                    setCursor(data.nextCursor);
                  }}
                  title={data.nextCursor ? 'Next page' : 'No more pages'}>
                  Next →
                </Button>
                <span>{total.toLocaleString()} total</span>
              </div>
            )}
          </>
        )}
      </div>
      <TracePeekDrawer traceId={peekTraceId} onClose={() => setPeekTraceId(null)} />
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
