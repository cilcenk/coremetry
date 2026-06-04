import { Suspense, useEffect, useMemo, useState } from 'react';
import type { CSSProperties, ReactNode } from 'react';
import { Link } from 'react-router-dom';
import { useSearchParams } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { Topbar } from '@/components/Topbar';
import { KqlSearchInput } from '@/components/KqlSearchInput';
import { EQLPanel } from '@/components/EQLPanel';
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
import { LogPatternStrip } from '@/components/LogPatternStrip';
import { LivePatternsPanel } from '@/components/LivePatternsPanel';
import { LogTemplatesPanel } from '@/components/LogTemplatesPanel';
import { Button } from '@/components/ui/Button';
import { buildKibanaURL } from '@/lib/kibanaLink';
import type { KibanaSettings } from '@/lib/types';
import { useLogs } from '@/lib/queries';
import { useUrlRange } from '@/lib/useUrlRange';
import { useTableNav } from '@/lib/useTableNav';
import { api } from '@/lib/api';
import { tsShort, timeRangeToNs, sevName, sevClass } from '@/lib/utils';
import type { LogsResponse, LogRow, TimeRange } from '@/lib/types';

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

// fmtBucket — render a bucket width in seconds as a human label
// for the volume card subtitle ("per 30s", "per 5m", "per 15m").
function fmtBucket(sec: number): string {
  if (sec % 3600 === 0) return `${sec / 3600}h`;
  if (sec % 60 === 0)   return `${sec / 60}m`;
  return `${sec}s`;
}

// foldVolume — collapse the per-severity timeseries into one
// stacked bar per time bucket: { info, warn, error } counts (info
// = INFO+DEBUG+TRACE+OTHER baseline, warn = WARN, error =
// ERROR+FATAL). Mirrors the prototype's 3-band stack, but the
// total is faithful — every band counts toward exactly one slot.
function foldVolume(series: SevSeries[]): { info: number; warn: number; error: number }[] {
  const byTime = new Map<number, { info: number; warn: number; error: number }>();
  for (const s of series) {
    const facet = bandToFacet(s.name);
    const slot = facet === 'error' ? 'error' : facet === 'warn' ? 'warn' : 'info';
    for (const p of s.points) {
      const cur = byTime.get(p.t) ?? { info: 0, warn: 0, error: 0 };
      cur[slot] += p.v;
      byTime.set(p.t, cur);
    }
  }
  return Array.from(byTime.entries())
    .sort((a, b) => a[0] - b[0])
    .map(([, v]) => v);
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

  // Level-facet counts + stacked volume bars (prototype LogsView).
  // ONE per-severity timeseries query feeds BOTH the toolbar facet
  // chip badges and the Logs-local stacked-bar volume row. Scoped
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
  const volBars = useMemo(() => foldVolume(sevSeries), [sevSeries]);
  const volMax = useMemo(
    () => Math.max(1, ...volBars.map(b => b.info + b.warn + b.error)),
    [volBars],
  );

  // Live-tail query — separate hook with refetchInterval so
  // RQ owns the polling loop. The query key includes the
  // service/search/severity filters but NOT a moving `from`/
  // `to` (the polling fetches the latest 5 min each tick).
  // Disabled when live=false; the static query (above) takes
  // over in that case.
  const liveQ = useQuery({
    queryKey: ['logs', 'live', filter.service, filter.cluster, filter.search, filter.severity],
    queryFn: () => {
      const now = Date.now() * 1_000_000;
      const fromNs = Math.floor((Date.now() - 5 * 60_000) * 1_000_000);
      return api.logs({
        limit: 200, from: fromNs, to: now,
        service: filter.service || undefined,
        cluster: filter.cluster || undefined,
        search: filter.search || undefined,
        severity: filter.severity > 0 ? filter.severity : undefined,
      });
    },
    enabled: live,
    // 10s, not 2s — ≥10s budget + ~10s ES pipeline lag (see the `live` decl).
    // refetchIntervalInBackground defaults false, so RQ already pauses the
    // poll when the tab is hidden (document.hidden rule).
    refetchInterval: live ? 10_000 : false,
    staleTime: 0,
  });

  // Merge: when live is on, prefer the live data; otherwise
  // the static window result. Mirrors the previous setData
  // behaviour where live overwrote static.
  const dataSource = live ? liveQ : staticQ;
  const data = dataSource.isLoading ? undefined
    : dataSource.isError ? null
    : dataSource.data;

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
  // v0.5.468 — EQL backend gate. We hide the EQL panel on CH
  // because ES is the only backend that implements sequence
  // detection. `logsFields` already returns the backend name;
  // reuse it instead of an extra /api/health call.
  const [logsBackend, setLogsBackend] = useState<string>('');
  const [showFieldsHint, setShowFieldsHint] = useState(false);
  // Save-as-alert modal state (v0.5.242). null = closed; an
  // object = open with this draft.
  const [alertDraft, setAlertDraft] = useState<{
    name: string;
    threshold: number;
    windowSec: number;
    severity: string;
  } | null>(null);
  const [alertSaving, setAlertSaving] = useState(false);
  const [alertMsg, setAlertMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

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
      .then(d => { setFields(d.fields ?? []); setLogsBackend(d.backend ?? ''); })
      .catch(() => { setFields([]); setLogsBackend(''); });
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
          {/* Create watcher (v0.5.252 — was: 🔔 Save as alert).
              Refactored to mirror Elasticsearch Watcher's mental
              model: Trigger / Condition / Action sections, so the
              operator reads the rule as a sentence ("Every 1m,
              count(<query>) over last 60s > 10 → page warning")
              instead of three flat inputs. Backend payload is
              unchanged — same saved-search alert rule introduced
              in v0.5.242. Emoji dropped at operator request:
              status-bar buttons should look professional, not
              messaging-app-ish. */}
          {draft.search.trim() !== '' && (
            <button className="sec"
              onClick={() => setAlertDraft({
                name: '', threshold: 10, windowSec: 60, severity: 'warning',
              })}
              title="Create a watcher — periodic poll of this query with a threshold + notification"
              style={{ fontSize: 12 }}>
              Create watcher
            </button>
          )}
          <button className={live ? 'live-on' : 'sec'}
            onClick={() => setLive(v => !v)}
            style={{ marginLeft: 'auto' }}
            title="Auto-refresh every 2 seconds with the latest logs">
            {live ? '⏸ Pause Live' : '▶ Live tail'}
          </button>
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

        {/* Log-volume stacked bars (prototype LogsView .volbars) —
            one thin bar per time bucket, stacked error (top, --err)
            / warn (--warn) / info (--accent). Reuses the SAME
            per-severity timeseries query as the facet counts above
            (no extra fetch). Sits above the shared severity-area
            LogsHistogram, which we keep. Hidden until there's a
            bounded window; renders skeleton/empty inline. */}
        {volumeEnabled && (
          <div className="card" style={{ marginBottom: 10, padding: 0 }}>
            <div className="ov-card-h">
              <h3>Log volume</h3>
              <span className="ov-sub">per {fmtBucket(volumeBucket)} · by level</span>
              <div className="ov-right" style={{ fontSize: 11 }}>
                {(['error', 'warn', 'info'] as const).map(k => (
                  <span key={k} style={{ display: 'inline-flex', alignItems: 'center', gap: 5, color: 'var(--text2)' }}>
                    <span style={{
                      width: 9, height: 9, borderRadius: 2, display: 'inline-block',
                      background: k === 'error' ? 'var(--err)' : k === 'warn' ? 'var(--warn)' : 'var(--accent)',
                    }} />
                    {k}
                  </span>
                ))}
              </div>
            </div>
            <div className="ov-card-b">
              {volumeQ.isLoading ? (
                <div style={{ height: 40 }} />
              ) : volBars.length === 0 ? (
                <div style={{ height: 40, display: 'flex', alignItems: 'center', color: 'var(--text3)', fontSize: 12 }}>
                  No log volume in this window.
                </div>
              ) : (
                <div style={{ display: 'flex', alignItems: 'flex-end', gap: 2, height: 40 }}>
                  {volBars.map((b, i) => {
                    const tot = b.info + b.warn + b.error;
                    return (
                      <div key={i}
                        title={`${tot.toLocaleString()} logs — ${b.error.toLocaleString()} error · ${b.warn.toLocaleString()} warn · ${b.info.toLocaleString()} info`}
                        style={{
                          flex: 1, minWidth: 0, display: 'flex',
                          flexDirection: 'column-reverse',
                        }}>
                        <span style={{ display: 'block', height: (b.error / volMax) * 40, background: 'var(--err)' }} />
                        <span style={{ display: 'block', height: (b.warn / volMax) * 40, background: 'var(--warn)' }} />
                        <span style={{ display: 'block', height: (b.info / volMax) * 40, background: 'var(--accent)' }} />
                      </div>
                    );
                  })}
                </div>
              )}
            </div>
          </div>
        )}

        {/* EQL Sequence Detection (v0.5.468) — only on ES
            backend, collapsed by default so it doesn't crowd
            the page for operators who don't reach for it. */}
        {logsBackend === 'elasticsearch' && (
          <EQLPanel fromMs={from ? Math.floor(from / 1_000_000) : undefined}
                    toMs={to ? Math.floor(to / 1_000_000) : undefined} />
        )}

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

        {/* Log anomaly strip (v0.5.239) — curated patterns
            (OOMKilled / panic / NPE / deadlock / TLS / etc.)
            that are NEW or 2×+ over baseline. Click a chip →
            narrow the table to that pattern + its firing
            service. Renders nothing when there's no signal. */}
        <LogPatternStrip onSelect={({ search: s, service: sv }) => {
          const next = {
            ...filter,
            search: s,
            service: sv || filter.service,
          };
          setDraft(d => ({ ...d, search: s, service: sv || d.service }));
          setFilter(next);
          resetPaging();
        }} />

        {/* Live patterns panel (v0.5.243) — ES significant_text
            unsupervised over (last 15min vs last 24h). Surfaces
            tokens that just got over-represented vs baseline:
            new exception types, rare error codes, unusual
            phrases. Click a chip → narrow search to that token.
            Hidden on CH backend (no native equivalent at
            billion-row scale). */}
        <LivePatternsPanel
          onSelect={token => {
            // Use the shorthand "body" key; the backend's
            // expandShorthand rewrites it to an OR across all
            // body-field candidates (message / Body / log.message)
            // so the filter works regardless of mapping.
            toggleSearchClause('body', token, false);
          }}
          onTracePeek={tid => setPeekTraceId(tid)} />

        {/* Drain-extracted templates (v0.5.244) — persistent
            log-shape ledger. Default sort: first_seen desc so
            new shapes land first. Click a template → search
            box gets the two most distinctive tokens from that
            shape (Java class names + logger paths rank
            highest in the scorer). */}
        <LogTemplatesPanel onSelectTemplate={substring => {
          // Substring search is body-field bound; the backend
          // shorthand expander rewrites "body:" to the right
          // field for the configured ES mapping.
          const next = { ...filter, search: substring };
          setDraft(d => ({ ...d, search: substring }));
          setFilter(next);
          resetPaging();
        }} />

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
              onContextOpen={l => setContextPivot(l)}
              extraExpanded={l => <SimilarTracesPanel body={l.body} />} />
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
      {alertDraft && (
        <SaveAsAlertModal
          query={filter.search}
          draft={alertDraft}
          onChange={setAlertDraft}
          saving={alertSaving}
          msg={alertMsg}
          onClose={() => { setAlertDraft(null); setAlertMsg(null); }}
          onSave={async () => {
            if (!alertDraft) return;
            setAlertSaving(true); setAlertMsg(null);
            try {
              await api.createAlertRule({
                name: alertDraft.name || `Log alert: ${filter.search.slice(0, 40)}`,
                service: '',
                metric: 'log_query',
                comparator: '>',
                threshold: alertDraft.threshold,
                windowSec: alertDraft.windowSec,
                severity: alertDraft.severity,
                enabled: true,
                logQuery: filter.search,
              });
              setAlertMsg({ kind: 'ok', text: 'Saved — rule is now evaluating on every tick.' });
              setTimeout(() => { setAlertDraft(null); setAlertMsg(null); }, 1200);
            } catch (err) {
              setAlertMsg({ kind: 'err',
                text: err instanceof Error ? err.message : 'Save failed' });
            } finally {
              setAlertSaving(false);
            }
          }} />
      )}
    </>
  );
}

// SaveAsAlertModal — Watcher-style overlay (v0.5.252, was the flat
// "Save as alert" of v0.5.242). Mirrors Elasticsearch Watcher's
// three-pane shape: Trigger (cadence) / Input + Condition (the
// query + threshold + window) / Action (severity). The operator
// reads it as a sentence — "Every tick, when count(query) > N in
// last Ws, page <severity>". Cadence is currently fixed to the
// evaluator tick (1m default); surfacing it as a labelled section
// makes the model explicit + opens the door to per-watcher
// frequency overrides later without another UI refactor.
function SaveAsAlertModal({
  query, draft, onChange, saving, msg, onClose, onSave,
}: {
  query: string;
  draft: { name: string; threshold: number; windowSec: number; severity: string };
  onChange: (next: typeof draft) => void;
  saving: boolean;
  msg: { kind: 'ok' | 'err'; text: string } | null;
  onClose: () => void;
  onSave: () => void;
}) {
  const sevColor = draft.severity === 'critical' ? 'var(--err)'
    : draft.severity === 'warning' ? 'var(--warn, #f0b352)'
    : 'var(--text2)';
  return (
    <div onClick={onClose} style={{
      position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.55)',
      display: 'grid', placeItems: 'center', zIndex: 200,
    }}>
      <div onClick={e => e.stopPropagation()} style={{
        width: 540, padding: 20, borderRadius: 8,
        background: 'var(--bg2)', border: '1px solid var(--border)',
      }}>
        <div style={{ fontSize: 15, fontWeight: 700, marginBottom: 2 }}>
          New watcher
        </div>
        <div style={{ fontSize: 12, color: 'var(--text3)', marginBottom: 14 }}>
          Polls the saved query on every evaluator tick, fires + fans out to
          notification channels when the threshold is breached. Same
          mechanism the alert evaluator uses for metric / span rules; logs
          ride the same fire-once-per-window de-dupe and severity routing.
        </div>

        {/* Sentence preview — the operator's mental model in one
            line, derived live from the inputs below. Datadog +
            Elastic Watcher both surface this; it removes the
            "wait what does this rule actually do" beat. */}
        <div style={{
          padding: '8px 10px', marginBottom: 14,
          background: 'var(--bg0)', border: '1px solid var(--border)',
          borderRadius: 4, fontSize: 12, lineHeight: 1.6,
          fontFamily: 'ui-monospace, SFMono-Regular, monospace',
        }}>
          <span style={{ color: 'var(--text3)' }}>Every</span>{' '}
          <b>1m</b>{' '}
          <span style={{ color: 'var(--text3)' }}>when count of matches in last</span>{' '}
          <b>{draft.windowSec}s</b>{' '}
          <span style={{ color: 'var(--text3)' }}>is</span>{' '}
          <b>&gt; {draft.threshold}</b>{' '}
          <span style={{ color: 'var(--text3)' }}>→ open a</span>{' '}
          <b style={{ color: sevColor }}>{draft.severity}</b>{' '}
          <span style={{ color: 'var(--text3)' }}>incident</span>
        </div>

        <label style={{ display: 'block', marginBottom: 10 }}>
          <div style={{ fontSize: 11, fontWeight: 700, color: 'var(--text2)',
            textTransform: 'uppercase', letterSpacing: 0.5, marginBottom: 4 }}>
            Name
          </div>
          <input value={draft.name}
            onChange={e => onChange({ ...draft, name: e.target.value })}
            placeholder={`watcher: ${query.slice(0, 32)}…`}
            style={{ width: '100%' }} />
        </label>

        {/* Trigger — runs on every evaluator tick today. Surfaced
            as a labelled, disabled control to make Watcher's
            "schedule" pane explicit, even though we only expose
            one cadence. */}
        <div style={{ marginBottom: 12 }}>
          <div style={{ fontSize: 11, fontWeight: 700, color: 'var(--text2)',
            textTransform: 'uppercase', letterSpacing: 0.5, marginBottom: 4 }}>
            Trigger
          </div>
          <div style={{
            display: 'flex', alignItems: 'center', gap: 8,
            padding: '6px 8px', borderRadius: 4,
            background: 'var(--bg0)', border: '1px dashed var(--border)',
            fontSize: 12, color: 'var(--text2)',
          }}>
            <span>Every</span>
            <code style={{ color: 'var(--text)', fontFamily: 'ui-monospace, monospace' }}>1m</code>
            <span style={{ color: 'var(--text3)' }}>
              (evaluator tick — per-rule schedule coming in a later release)
            </span>
          </div>
        </div>

        {/* Input — the operator's query. Read-only; Cancel +
            re-search to tweak. Watcher's "input" pane usually
            shows a real ES query; ours surfaces the KQL the
            operator already wrote on the page. */}
        <div style={{ marginBottom: 12 }}>
          <div style={{ fontSize: 11, fontWeight: 700, color: 'var(--text2)',
            textTransform: 'uppercase', letterSpacing: 0.5, marginBottom: 4 }}>
            Input — Query
          </div>
          <pre style={{
            margin: 0, padding: 8, borderRadius: 4, fontSize: 12,
            background: 'var(--bg0)', border: '1px solid var(--border)',
            fontFamily: 'ui-monospace, SFMono-Regular, monospace',
            whiteSpace: 'pre-wrap', wordBreak: 'break-all',
          }}>{query}</pre>
        </div>

        {/* Condition — threshold + window. The Watcher equivalent
            of `ctx.payload.hits.total > N over last Ws`. */}
        <div style={{ marginBottom: 12 }}>
          <div style={{ fontSize: 11, fontWeight: 700, color: 'var(--text2)',
            textTransform: 'uppercase', letterSpacing: 0.5, marginBottom: 4 }}>
            Condition
          </div>
          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 10 }}>
            <label>
              <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 4 }}>Matches &gt;</div>
              <input type="number" min={0} value={draft.threshold}
                onChange={e => onChange({ ...draft, threshold: Number(e.target.value) || 0 })}
                style={{ width: '100%' }} />
            </label>
            <label>
              <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 4 }}>Window (sec)</div>
              <input type="number" min={10} value={draft.windowSec}
                onChange={e => onChange({ ...draft, windowSec: Number(e.target.value) || 60 })}
                style={{ width: '100%' }} />
            </label>
          </div>
        </div>

        {/* Action — severity. Channels resolved server-side by the
            notifier match rules; no per-rule channel pick here. */}
        <div style={{ marginBottom: 14 }}>
          <div style={{ fontSize: 11, fontWeight: 700, color: 'var(--text2)',
            textTransform: 'uppercase', letterSpacing: 0.5, marginBottom: 4 }}>
            Action
          </div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
            <span style={{ fontSize: 12, color: 'var(--text3)' }}>Open a</span>
            <select value={draft.severity}
              onChange={e => onChange({ ...draft, severity: e.target.value })}
              style={{ minWidth: 130, color: sevColor, fontWeight: 600 }}>
              <option value="info">info</option>
              <option value="warning">warning</option>
              <option value="critical">critical</option>
            </select>
            <span style={{ fontSize: 12, color: 'var(--text3)' }}>
              incident · routed to matching notification channels
            </span>
          </div>
        </div>

        {msg && (
          <div style={{ marginBottom: 10, fontSize: 12,
            color: msg.kind === 'ok' ? 'var(--ok)' : 'var(--err)' }}>
            {msg.text}
          </div>
        )}
        <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
          <button className="sec" onClick={onClose} disabled={saving}>Cancel</button>
          <button onClick={onSave} disabled={saving}>
            {saving ? 'Saving…' : 'Create watcher'}
          </button>
        </div>
      </div>
    </div>
  );
}

// SimilarTracesPanel (v0.5.141) — collapsed-by-default block in
// the expanded log row. Click "Find similar" → POSTs the body to
// /api/logs/similar (Elastic more_like_this + trace.id terms
// agg) and renders the bucketed trace IDs as clickable links.
// CH-backend deployments see a tooltip explaining ES is needed
// rather than a noisy error; ES installs get a one-click pivot
// to "all traces that produced a log like this one".
function SimilarTracesPanel({ body }: { body: string }) {
  const [state, setState] = useState<
    | { kind: 'idle' }
    | { kind: 'loading' }
    | { kind: 'ok'; traces: Array<{ traceId: string; count: number }> }
    | { kind: 'err'; msg: string }
  >({ kind: 'idle' });
  const run = async () => {
    setState({ kind: 'loading' });
    try {
      const r = await api.logsSimilarTraces(body, 50);
      setState({ kind: 'ok', traces: r.traces ?? [] });
    } catch (err) {
      const msg = err instanceof Error ? err.message : 'similar-logs lookup failed';
      setState({ kind: 'err', msg });
    }
  };
  return (
    <div style={{
      marginTop: 8, paddingTop: 8,
      borderTop: '1px dashed var(--border)',
      fontSize: 11,
    }}>
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 8 }}>
        <button type="button" className="sec"
          onClick={run}
          disabled={state.kind === 'loading'}
          style={{ fontSize: 11, padding: '3px 10px' }}
          title="Find traces whose logs contain text similar to this one (more_like_this on the body field; Elasticsearch backend only).">
          {state.kind === 'loading' ? 'Searching…' : '⌕ Find similar traces'}
        </button>
        {state.kind === 'ok' && (
          <span style={{ color: 'var(--text3)' }}>
            {state.traces.length} trace{state.traces.length === 1 ? '' : 's'} match
          </span>
        )}
        {state.kind === 'err' && (
          <span style={{ color: 'var(--err)' }}>{state.msg}</span>
        )}
      </div>
      {state.kind === 'ok' && state.traces.length > 0 && (
        <div style={{ marginTop: 6, display: 'flex', flexWrap: 'wrap', gap: 4 }}>
          {state.traces.map(t => (
            <Link key={t.traceId}
              to={`/trace/${encodeURIComponent(t.traceId)}`}
              title={`${t.count} similar log line${t.count === 1 ? '' : 's'} in this trace`}
              style={{
                fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
                fontSize: 10, padding: '2px 6px', borderRadius: 3,
                background: 'var(--bg2)', border: '1px solid var(--border)',
                color: 'var(--text)', textDecoration: 'none',
              }}>
              {t.traceId.slice(0, 16)}<span style={{ color: 'var(--text3)' }}> · {t.count}</span>
            </Link>
          ))}
        </div>
      )}
    </div>
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
