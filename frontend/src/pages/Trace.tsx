import { Suspense, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { Topbar } from '@/components/Topbar';
import { DrillButton } from '@/components/DrillButton';
import { Spinner, Empty } from '@/components/Spinner';
import { perSpanLogSignals, spanEventLogRows, traceServicesWithoutTraceField } from '@/lib/traceEventLogs';
import { computeCriticalPath } from '@/lib/criticalPath';
import { CopyButton } from '@/components/CopyButton';
import { LogTable } from '@/components/LogTable';
import { CopilotExplain } from '@/components/CopilotExplain';
import { IconLink, IconCheck, IconDownload, IconSparkles } from '@/components/icons';
import { Button } from '@/components/ui/Button';
import { useAuth } from '@/components/AuthProvider';
import { useShortcuts } from '@/lib/keyboard';
import { api } from '@/lib/api';
import { useUrlRange } from '@/lib/useUrlRange';
import { useOutsideClose } from '@/lib/useOutsideClose';
import { useCorrelatedLogs, spanHasError, traceLogWindow } from '@/lib/otel';
import { fmtNs, tsLong, tsRel, displaySpanName } from '@/lib/utils';
import type { LogRow, SpanRow, TimeRange, PivotAnchor } from '@/lib/types';
import { TraceWaterfall, TraceServiceBreakdown } from '@/components/TraceWaterfall';
import { SpanDetail } from '@/components/SpanDetail';
import { TraceHonesty } from '@/components/traces/TraceHonesty';
import { CorrelationContextDrawer } from '@/components/CorrelationContextDrawer';
// v0.8.550 — this file used to OWN the strongest of the three hand-rolled
// clipboard copies (it alone fell back when writeText rejected). That
// version is now lib/clipboard, and the two local functions are gone.
import { copyToClipboard } from '@/lib/clipboard';

function TraceDetailInner() {
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const id = searchParams.get('id') ?? '';

  const [range, setRange] = useUrlRange('30m');
  const [spans, setSpans] = useState<SpanRow[] | null | undefined>(undefined);
  // v0.5.208 — "clickhouse" when the trace lives in Coremetry's
  // store, "tempo" when getTrace fell back to the external Tempo
  // backend (Coremetry sampled it out). Drives the small banner
  // above the waterfall so the operator doesn't mistake "trace
  // resolved" for "Coremetry has full retention".
  const [source, setSource] = useState<'clickhouse' | 'tempo' | 'mv_only' | undefined>(undefined);
  // v0.6.34 — aged-out stub: present only when source === 'mv_only'.
  // Carries the aggregate stats trace_summary_5m still holds for
  // traces whose raw spans have aged past the 30-day TTL.
  const [stub, setStub] = useState<NonNullable<import('@/lib/types').TraceDetailResponse['stub']> | undefined>(undefined);
  // selectedId + tab are URL-bound so a Share-button copy round-
  // trips: "open trace X with the rpc-call span focused on the Logs
  // tab" comes back identical when pasted in another browser.
  const [selectedId, setSelectedId] = useState<string | null>(
    () => searchParams.get('span'));
  // Side-tab state — Trace (waterfall + detail) vs Logs (entries
  // matching this trace_id, Uptrace-style). Logs are fetched lazily
  // on first tab click so the trace page stays fast for users who
  // never need them.
  const [tab, setTab] = useState<'trace' | 'logs'>(
    () => (searchParams.get('tab') === 'logs' ? 'logs' : 'trace'));
  // Correlated Signals (task #6) — "Correlate ◆" opens the pivot drawer anchored
  // on this trace, surfacing the METRICS lens (the anchor service's RED series)
  // the Trace page doesn't otherwise show, alongside the trace + correlated logs.
  const [correlateAnchor, setCorrelateAnchor] = useState<PivotAnchor | null>(null);

  // Trace-anchored log lookup window (Unix ns) — min(span.startTime)-1min ..
  // max(span.endTime)+1min. Bounds the trace→logs ES query to the trace's own
  // time window instead of a full-index scan by trace_id (v0.8.180). Anchored
  // to span times, NOT now(), so it doesn't reintroduce the v0.5.223 "old
  // traces vanish" bug. Stable per trace → no refetch churn from the key.
  const logWin = useMemo(() => traceLogWindow(spans), [spans]);

  // v0.8.407 — trace↔log correlation, zero-ES leg. Span events
  // (exceptions + log-bridge records) already ride the CH trace load;
  // they become pseudo log rows for the Logs tab and, combined with
  // whatever the lazy ES fetch has cached, per-span waterfall chips.
  const eventRows = useMemo(() => spanEventLogRows(spans ?? []), [spans]);

  // Correlated logs ride the shared OTel hook — every log line sharing this
  // trace_id, react-query-cached. Enabled lazily (only when the Logs tab is
  // open) so the trace page stays fast for operators who never need them.
  const logsQuery = useCorrelatedLogs(
    tab === 'logs' ? id : undefined, undefined,
    { limit: 500, from: logWin?.from, to: logWin?.to });
  // v0.8.407 — per-span correlated-row counts for the waterfall chips
  // (span events always; ES logs once the lazy fetch has cached them).
  const logSignals = useMemo(
    () => perSpanLogSignals(logsQuery.data?.logs, eventRows),
    [logsQuery.data, eventRows]);
  const logs: LogRow[] | null | undefined =
    tab !== 'logs' ? undefined
    : logsQuery.isLoading ? undefined
    : logsQuery.isError ? null
    : (logsQuery.data?.logs ?? []);
  // v0.8.332 (pivot Phase 3) — a slow/unreachable log backend answers HTTP
  // 200 {degraded:true, reason} with empty lists instead of an error; the
  // Logs tab shows a warning chip over the (partial/empty) table and never
  // blocks on the backend.
  const logsDegraded: string | null =
    tab === 'logs' && logsQuery.data?.degraded
      ? (logsQuery.data.reason || 'log backend slow/unreachable')
      : null;

  useEffect(() => {
    if (!id) return;
    setSpans(undefined);
    setSource(undefined);
    setStub(undefined);
    api.trace(id)
      .then(d => {
        setSpans(d.spans ?? []);
        setSource(d.source);
        setStub(d.stub);
      })
      .catch(() => setSpans(null));
  }, [id]);

  // Mirror selectedId + tab to the URL via replaceState so the Share
  // button captures the current view exactly. We don't push history
  // — selecting a span shouldn't add a back-button stop.
  useEffect(() => {
    if (typeof window === 'undefined' || !id) return;
    const url = new URL(window.location.href);
    if (selectedId) url.searchParams.set('span', selectedId);
    else url.searchParams.delete('span');
    if (tab === 'logs') url.searchParams.set('tab', 'logs');
    else url.searchParams.delete('tab');
    window.history.replaceState({}, '', url.toString());
  }, [selectedId, tab, id]);

  // Visible-order span list for j/k navigation. Same DFS the
  // waterfall renders — sort all spans by parent + start
  // time, then walk depth-first so j/k step through rows in
  // the order an operator's eye scans them.
  // Hoisted above the `if (!id)` early return so every hook
  // (this + useShortcuts + the criticalPath/spanFilter pair
  // below) runs unconditionally. The bodies already no-op on
  // an empty `spans` list, so the missing-id render is
  // unchanged.
  const orderedSpanIds = useMemo<string[]>(() => {
    if (!spans || spans.length === 0) return [];
    const byParent = new Map<string, SpanRow[]>();
    for (const sp of spans) {
      const pid = sp.parentSpanId || '';
      const list = byParent.get(pid);
      if (list) list.push(sp);
      else byParent.set(pid, [sp]);
    }
    for (const list of byParent.values()) {
      list.sort((a, b) => a.startTime - b.startTime);
    }
    const out: string[] = [];
    const walk = (parentId: string) => {
      for (const sp of byParent.get(parentId) ?? []) {
        out.push(sp.spanId);
        walk(sp.spanId);
      }
    };
    // Roots first: any span whose parent is empty OR refers to
    // a parent not in the trace. Sorted by start time so the
    // order is deterministic across multi-root edge cases.
    const ids = new Set(spans.map(s => s.spanId));
    const roots = spans
      .filter(s => !s.parentSpanId || !ids.has(s.parentSpanId))
      .sort((a, b) => a.startTime - b.startTime);
    for (const r of roots) {
      out.push(r.spanId);
      walk(r.spanId);
    }
    return out;
  }, [spans]);

  // j/k step + g g / G + Enter / Esc — the same vocabulary
  // useTableNav installs for list pages; the waterfall has
  // its own row layout so it gets a hand-rolled binding rather
  // than the hook.
  useShortcuts([
    {
      keys: 'j', label: 'Next span', group: 'Trace',
      handler: () => {
        if (orderedSpanIds.length === 0) return;
        const i = selectedId ? orderedSpanIds.indexOf(selectedId) : -1;
        const next = Math.min(orderedSpanIds.length - 1, i + 1);
        setSelectedId(orderedSpanIds[next] ?? null);
      },
    },
    {
      keys: 'k', label: 'Previous span', group: 'Trace',
      handler: () => {
        if (orderedSpanIds.length === 0) return;
        const i = selectedId ? orderedSpanIds.indexOf(selectedId) : 0;
        const prev = Math.max(0, i - 1);
        setSelectedId(orderedSpanIds[prev] ?? null);
      },
    },
    {
      keys: 'g g', label: 'Jump to first span', group: 'Trace',
      handler: () => {
        if (orderedSpanIds.length > 0) setSelectedId(orderedSpanIds[0]);
      },
    },
    {
      keys: 'shift+g', label: 'Jump to last span', group: 'Trace',
      handler: () => {
        if (orderedSpanIds.length > 0) {
          setSelectedId(orderedSpanIds[orderedSpanIds.length - 1]);
        }
      },
    },
    {
      keys: 'Escape', label: 'Close span detail', group: 'Trace',
      evenInInputs: true,
      handler: () => setSelectedId(null),
    },
  ], [orderedSpanIds, selectedId]);

  // Critical path — synchronous longest chain through the
  // span DAG. Cheap O(N) DFS; useMemo only recomputes when
  // the span list identity changes (i.e., when a new trace
  // is loaded). Operator can hide the highlight via the
  // toolbar toggle.
  const [showCritical, setShowCritical] = useState(true);
  const criticalPath = useMemo(() => {
    if (!spans || spans.length === 0) return null;
    return computeCriticalPath(spans.map(s => ({
      spanId: s.spanId,
      parentId: s.parentSpanId ?? '',
      startTime: s.startTime,
      duration: s.endTime - s.startTime,
    })));
  }, [spans]);

  // v0.5.383 — span filter within ONE trace. Operator searches
  // by substring across span name, service, displayed name,
  // and attribute values. Returns the set of matching span IDs
  // which TraceWaterfall dims non-matches and highlights matches
  // by. No tree restructure — keeps parent/child shape intact
  // so the operator can still read the call hierarchy around
  // each match.
  const [spanFilter, setSpanFilter] = useState('');
  // Critical-path FOCUS mode (distinct from the stripe show/hide
  // checkbox): when on, every row off the critical path dims so
  // the dominant latency chain reads as the only bright thing on
  // screen. Composes with the span filter's own dimming.
  const [critFocus, setCritFocus] = useState(false);
  const spanMatchIds = useMemo<Set<string> | undefined>(() => {
    const q = spanFilter.trim().toLowerCase();
    if (!q || !spans) return undefined;
    const hits = new Set<string>();
    for (const s of spans) {
      if (spanMatchesQuery(s, q)) hits.add(s.spanId);
    }
    return hits;
  }, [spans, spanFilter]);

  // v0.8.332 (pivot Phase 3) — log→trace deep-link scroll. selectedId is
  // already URL-seeded from ?span= (the wf-sel row style applies through the
  // existing selection state); what was missing is bringing that row into
  // view on a long waterfall. Fires ONCE, only when the page OPENED with
  // ?span= (LogTable's trace link now appends it) — user clicks never scroll.
  const urlSpanRef = useRef<string | null>(searchParams.get('span'));
  useEffect(() => {
    const want = urlSpanRef.current;
    if (!want || !spans || spans.length === 0) return;
    urlSpanRef.current = null; // once per page load
    if (!spans.some(s => s.spanId === want)) return;
    // rAF: the waterfall rows render in this same commit — scroll after paint.
    // On ?tab=logs the waterfall isn't mounted and the selector finds nothing.
    requestAnimationFrame(() => {
      document.querySelector('.wf-sel')?.scrollIntoView({ block: 'center' });
    });
  }, [spans]);

  // Operator-reported (v0.8.361): pressing the page background closes
  // the span panel — before this, only ✕/Esc dismissed it. The ref
  // wraps waterfall + panel together so a row press stays a re-select
  // (no close→reopen remount losing panel scroll + refetching).
  // Hooks, so hoisted above the `if (!id)` early return; active keys
  // off selectedId (a dangling id without a matching span renders no
  // panel, and nulling it is a no-op).
  const spanAreaRef = useRef<HTMLDivElement>(null);
  const closeSpanPanel = useCallback(() => setSelectedId(null), []);
  useOutsideClose(spanAreaRef, !!selectedId, closeSpanPanel);

  // Early return AFTER every hook so the hook call order is
  // stable across renders (react-hooks/rules-of-hooks). When
  // there's no id we render the missing-id placeholder — same
  // output as before, just relocated below the hooks.
  if (!id) {
    return (
      <>
        <Topbar title="Trace" range={range} onRangeChange={setRange} />
        <div id="content"><Empty icon="⚠" title="Missing trace id" /></div>
      </>
    );
  }

  // Plain derived values (not hooks) — fine to compute after the
  // early return since they only feed the render below.
  const sel = spans?.find(s => s.spanId === selectedId) ?? null;
  const root = spans?.find(s => !s.parentSpanId) ?? spans?.[0];
  const minT = spans && spans.length ? Math.min(...spans.map(s => s.startTime)) : 0;
  const maxT = spans && spans.length ? Math.max(...spans.map(s => s.endTime)) : 0;
  const criticalPathIds = (showCritical && criticalPath) ? criticalPath.ids : undefined;
  const totalNs = maxT - minT;
  // spanHasError is the honest "is this a failure" — error status OR a recorded
  // exception event (matches the waterfall tint + the trace-level badge).
  const hasErr = spans?.some(s => spanHasError(s)) ?? false;


  return (
    <>
      <Topbar title="Trace Detail" range={range} onRangeChange={setRange} />
      <div id="content">
        <div style={{ marginBottom: 10, display: 'flex', gap: 10, alignItems: 'center', flexWrap: 'wrap' }}>
          <Button variant="secondary" size="sm" onClick={() => navigate(-1)}>← Back</Button>
          <code style={{ fontSize: 11, color: 'var(--text2)', background: 'var(--bg2)', padding: '2px 6px', borderRadius: 4 }}>
            {id}<CopyButton value={id} title="Copy trace ID" />
          </code>
          {spans && spans.length > 0 && (
            <>
              <span className={`badge ${hasErr ? 'b-err' : 'b-ok'}`}>{hasErr ? 'ERROR' : 'OK'}</span>
              <span style={{ color: 'var(--text2)', fontSize: 12 }}>{spans.length} spans · {fmtNs(totalNs)}</span>
              {root && <span style={{ color: 'var(--text3)', fontSize: 12 }}>{tsLong(root.startTime)}</span>}
              <span style={{ marginLeft: 'auto', display: 'flex', gap: 6, alignItems: 'center' }}>
                {/* Critical path summary — when computed, the
                    chain's total duration tells the operator
                    how much of the trace's wall-clock time
                    happens on the dominant path. Toggle hides
                    the highlight without recomputing. */}
                {criticalPath && criticalPath.ids.size > 0 && (
                  <label style={{
                    display: 'inline-flex', alignItems: 'center', gap: 6,
                    fontSize: 11, color: 'var(--text2)', marginRight: 4,
                  }} title={`${criticalPath.ids.size} spans summing to ${fmtNs(criticalPath.totalNs)}. The synchronous longest chain through the trace's DAG.`}>
                    <input type="checkbox"
                           checked={showCritical}
                           onChange={e => setShowCritical(e.target.checked)} />
                    Critical path · {fmtNs(criticalPath.totalNs)}
                  </label>
                )}
                {/* Compare button — bumped to primary-accent in
                    v0.4.96 because the secondary-style version
                    blended into the action chip row and
                    operators kept asking "how do I diff two
                    traces". Same destination, just visually
                    promoted. */}
                <Link to={`/trace/compare?a=${encodeURIComponent(id)}`}
                      title="Compare this trace side-by-side with another (operation-level diff)"
                      style={{
                        fontSize: 12, padding: '4px 12px',
                        display: 'inline-flex', alignItems: 'center', gap: 6,
                        textDecoration: 'none', fontWeight: 600,
                        background: 'var(--accent-soft)',
                        color: 'var(--accent2)',
                        border: '1px solid color-mix(in oklab, var(--accent) 45%, transparent)',
                        borderRadius: 6,
                      }}>
                  ↔ Compare trace
                </Link>
                {/* Drill to logs scoped to this trace (v0.5.463).
                    Operators jump trace→logs constantly during
                    incident investigation; carrying the trace_id
                    saves the manual paste step. */}
                <DrillButton to="/logs"
                  params={{ traceId: id, from: logWin?.from, to: logWin?.to }}
                  title="Logs correlated to this trace_id"
                  label="≡ Logs" variant="secondary" />
                {/* Correlated Signals (task #6) — open the cross-signal pivot
                    drawer anchored on this trace. Surfaces the metrics lens the
                    Trace page doesn't show, plus the correlated logs, without a
                    page change. */}
                <Button variant="secondary" size="sm"
                  onClick={() => setCorrelateAnchor({ kind: 'trace', traceId: id })}
                  title="Correlated signals — trace ↔ logs ↔ metrics for this trace"
                  leftIcon={<IconLink />}>
                  <span>Correlate ◆</span>
                </Button>
                <SharePopover traceId={id} />
                <Button variant="secondary" size="sm"
                  onClick={() => exportTraceJSON(id, spans)}
                  title="Download this trace as JSON (full span list with attributes + events)"
                  leftIcon={<IconDownload />}>
                  <span>Export JSON</span>
                </Button>
              </span>
            </>
          )}
        </div>

        {spans === undefined && <Spinner />}
        {spans === null && <Empty icon="⚠" title="Failed to load trace" />}
        {spans && spans.length === 0 && source === 'mv_only' && stub && (
          // v0.6.34 — aged-out stub. trace_summary_5m still has
          // the aggregates but raw spans dropped past the 30-day
          // TTL. Render what we know so the operator gets context
          // instead of a blank "Trace not found".
          <div style={{
            padding: 16, border: '1px solid var(--border)',
            borderLeft: '3px solid var(--warn)',
            borderRadius: 6, background: 'var(--bg2)',
          }}>
            <div style={{ fontWeight: 600, fontSize: 14, marginBottom: 4 }}>
              Trace aged out of raw spans
            </div>
            <p style={{ fontSize: 12, color: 'var(--text2)', margin: '4px 0 12px', lineHeight: 1.5 }}>
              The 5-minute aggregate MV still holds this trace's summary
              (90-day retention), but the per-span detail data has been
              evicted by the raw spans TTL (default 30 days). Span
              waterfall isn't available for this trace anymore. To keep
              long-tail trace detail, configure Tempo backend in
              Settings → Tempo, or extend the raw-spans retention in
              <code> config.yaml</code>.
            </p>
            <div style={{
              display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(140px, 1fr))',
              gap: 8, marginTop: 8,
            }}>
              <KPI label="Root service" value={stub.rootService || '—'} />
              <KPI label="Root operation" value={stub.rootName || '—'} />
              <KPI label="Span count" value={stub.spanCount.toLocaleString()} />
              <KPI label="Errors" value={stub.errorCount.toLocaleString()}
                   tone={stub.errorCount > 0 ? 'err' : undefined} />
              <KPI label="Duration"
                   value={stub.durationMs.toFixed(stub.durationMs < 10 ? 2 : 0) + ' ms'} />
              <KPI label="Started"
                   value={tsLong(stub.startTimeNs)} />
            </div>
          </div>
        )}
        {spans && spans.length === 0 && source !== 'mv_only' && (
          <Empty icon="⋮" title="Trace not found" />
        )}
        {spans && spans.length > 0 && (
          <>
            {source === 'tempo' && (
              <div style={{
                marginBottom: 10, padding: '6px 10px',
                background: 'var(--bg2)',
                border: '1px solid var(--border)',
                borderLeft: '3px solid var(--accent2)',
                borderRadius: 4,
                fontSize: 11, color: 'var(--text2)',
                display: 'flex', alignItems: 'center', gap: 6,
              }} title="Coremetry didn't have this trace in its own store; the spans below were fetched from the external Tempo backend configured in Settings → Tempo backend.">
                <span style={{ fontFamily: 'monospace', color: 'var(--accent2)' }}>⇆</span>
                <span><b>Source:</b> Tempo fallback · Coremetry sampled this trace out, the spans were read from the external Tempo backend.</span>
              </div>
            )}
            {/* v0.8.332 (pivot Phase 3) — OTel span links, both directions.
                Renders NOTHING for the (vast) majority of traces that carry
                no links; see LinkedTracesSection. */}
            <LinkedTracesSection id={id} />
            <div style={{ marginBottom: 10, display: 'flex', gap: 8, flexWrap: 'wrap' }}>
              <CopilotExplain kind="trace" id={id} label={<><IconSparkles /> <span style={{ marginLeft: 6 }}>Explain this trace</span></>} />
              <CompareTracesButton aId={id} />
            </div>

            {/* Trace vs Logs (Uptrace-style) — uses the shared
                .tab-strip pattern so it visually matches Settings,
                Exceptions inbox, and Status Page admin tabs. */}
            <div className="tab-strip" style={{ marginBottom: 10 }}>
              <TabBtn active={tab === 'trace'} onClick={() => setTab('trace')}>
                Trace <span style={{ color: 'var(--text3)', marginLeft: 4 }}>{spans.length}</span>
              </TabBtn>
              <TabBtn active={tab === 'logs'} onClick={() => setTab('logs')}>
                {/* v0.8.407 — count covers ES logs + span-event rows once
                    fetched; before the lazy ES fetch a ● hints that the
                    trace already carries log-like span events (zero-cost
                    signal — no ES query fires until the tab opens). */}
                Logs {logs
                  ? <span style={{ color: 'var(--text3)', marginLeft: 4 }}>{logs.length + eventRows.length}</span>
                  : eventRows.length > 0
                    ? <span style={{ color: 'var(--text3)', marginLeft: 4 }} title={`${eventRows.length} span event(s) on this trace`}>●</span>
                    : null}
              </TabBtn>
            </div>

            {tab === 'trace' && (
              <>
                {/* Honest OTel provenance: W3C tracecontext linkage + sampling +
                    dropped-span counts so the operator never mistakes a partial
                    trace for a complete one. */}
                <TraceHonesty spans={spans} source={source} />
                <div ref={spanAreaRef} style={{ display: 'flex', alignItems: 'stretch', gap: 10, minHeight: 240 }}>
                  <div style={{ flex: 1, minWidth: 0 }}>
                    <TraceServiceBreakdown spans={spans} />
                    <SpanFilterBar spans={spans} value={spanFilter} onChange={setSpanFilter}
                      critCount={criticalPath?.ids.size ?? 0}
                      critFocus={critFocus} onCritFocus={setCritFocus} />
                    <TraceWaterfall spans={spans} selectedId={selectedId} onSelect={setSelectedId}
                      criticalPathIds={criticalPathIds} matchIds={spanMatchIds}
                      focusIds={critFocus && criticalPath ? criticalPath.ids : undefined}
                      logSignals={logSignals} onLogsClick={() => setTab('logs')} />
                  </div>
                  {sel && <SpanDetail span={sel} onClose={closeSpanPanel}
                    logsFrom={logWin?.from} logsTo={logWin?.to} />}
                </div>
              </>
            )}

            {tab === 'logs' && (
              <TraceLogsPanel logs={logs} degraded={logsDegraded}
                eventRows={eventRows}
                traceServices={[...new Set((spans ?? []).map(sp => sp.serviceName).filter(Boolean))]} />
            )}
          </>
        )}
      </div>
      <CorrelationContextDrawer
        anchor={correlateAnchor}
        onClose={() => setCorrelateAnchor(null)} />
    </>
  );
}

// SpanFilterBar — v0.5.383. In-trace substring search input
// + match count badge. Operator types → matching spans glow
// in the waterfall, non-matches dim. Tree structure stays
// intact so the call hierarchy around each match remains
// readable. Also hosts the critical-path focus chip so the
// waterfall's two dimming modes live on one toolbar row.
// One matcher for both the page's match-set memo and the filter
// bar's counter so the two can't drift. Substring across span
// name, service, display name, attribute values, AND the category
// tag (DB / HTTP / RPC / MQ) — "db" lights up every database span
// the way the waterfall's category chips classify them.
function spanCategoryTag(s: SpanRow): string {
  const a = s.attributes ?? {};
  if (a['db.system'])        return 'db';
  if (a['messaging.system']) return 'mq';
  if (a['rpc.system'])       return 'rpc';
  if (a['http.method'] || a['http.request.method']) return 'http';
  return '';
}

function spanMatchesQuery(s: SpanRow, q: string): boolean {
  if (s.name.toLowerCase().includes(q)
    || s.serviceName.toLowerCase().includes(q)
    || displaySpanName(s).toLowerCase().includes(q)
    || spanCategoryTag(s).includes(q)) {
    return true;
  }
  for (const v of Object.values(s.attributes ?? {})) {
    if (String(v).toLowerCase().includes(q)) return true;
  }
  return false;
}

function SpanFilterBar({ spans, value, onChange, critCount, critFocus, onCritFocus }: {
  spans: SpanRow[];
  value: string;
  onChange: (v: string) => void;
  critCount?: number;
  critFocus?: boolean;
  onCritFocus?: (v: boolean) => void;
}) {
  const matches = useMemo(() => {
    const q = value.trim().toLowerCase();
    if (!q) return 0;
    let n = 0;
    for (const s of spans) {
      if (spanMatchesQuery(s, q)) n++;
    }
    return n;
  }, [spans, value]);
  return (
    <div style={{ marginBottom: 8, display: 'flex', alignItems: 'center', gap: 8 }}>
      <input value={value} onChange={e => onChange(e.target.value)}
        aria-label="Filter spans by name, service, or attribute value"
        placeholder="Filter spans (name, service, attr value)…"
        style={{ flex: 1, maxWidth: 360, padding: '4px 10px', fontSize: 12,
                 background: 'var(--bg)', color: 'var(--text)',
                 border: '1px solid var(--border)', borderRadius: 4 }} />
      <span style={{ fontSize: 11, color: 'var(--text3)' }}>
        {value.trim()
          ? `${matches} / ${spans.length} matching`
          : `${spans.length} span${spans.length === 1 ? '' : 's'}`}
      </span>
      {onCritFocus && (critCount ?? 0) > 0 && (
        <span className={'facet' + (critFocus ? ' on' : '')}
          role="button" tabIndex={0}
          title="Dim every span that is NOT on the critical path"
          onClick={() => onCritFocus(!critFocus)}
          onKeyDown={e => {
            if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onCritFocus(!critFocus); }
          }}>
          Critical path focus <span className="n">{critCount}</span>
        </span>
      )}
    </div>
  );
}

function TabBtn({ active, onClick, children }: {
  active: boolean; onClick: () => void; children: React.ReactNode;
}) {
  return (
    <button onClick={onClick} className={active ? 'active' : ''}>{children}</button>
  );
}

// LinkedTracesSection — v0.8.332 (pivot Phase 3). OTel span links for this
// trace, BOTH directions, from /api/traces/{id}/links (span_links +
// span_links_reverse PK scans, pivot Phase 2). Lazy fetch-on-render with
// staleTime = the endpoint's 30s serveCached TTL. Space discipline: renders
// NOTHING until data arrives AND at least one link exists — most traces
// carry no links and the header must not grow a permanent empty section
// (no spinner either, for the same reason).
function LinkedTracesSection({ id }: { id: string }) {
  const q = useQuery({
    queryKey: ['trace-links', id],
    queryFn: () => api.traceLinks(id),
    enabled: !!id,
    staleTime: 30_000,
  });
  // Flatten both directions into display rows, deduped by (direction, other
  // trace): a batch consumer declares one link per consumed span, but the
  // operator pivots per TRACE. Self-links (spans linking within this same
  // trace) are skipped — "links to itself" is noise on this surface.
  const rows = useMemo(() => {
    const d = q.data;
    if (!d) return [];
    const seen = new Set<string>();
    const out: { dir: 'out' | 'in'; other: string; attrs: number }[] = [];
    for (const l of d.outgoing ?? []) {
      const key = `out:${l.linkedTraceId}`;
      if (!l.linkedTraceId || l.linkedTraceId === id || seen.has(key)) continue;
      seen.add(key);
      out.push({ dir: 'out', other: l.linkedTraceId, attrs: Object.keys(l.attrs ?? {}).length });
    }
    for (const l of d.incoming ?? []) {
      const key = `in:${l.traceId}`;
      if (!l.traceId || l.traceId === id || seen.has(key)) continue;
      seen.add(key);
      out.push({ dir: 'in', other: l.traceId, attrs: Object.keys(l.attrs ?? {}).length });
    }
    return out;
  }, [q.data, id]);
  if (rows.length === 0) return null;
  return (
    <div style={{
      marginBottom: 10, padding: '6px 10px',
      background: 'var(--bg2)',
      border: '1px solid var(--border)',
      borderLeft: '3px solid var(--accent2)',
      borderRadius: 4,
      fontSize: 12, display: 'flex', alignItems: 'center', gap: 12, flexWrap: 'wrap',
    }}>
      <span style={{
        fontSize: 11, fontWeight: 600, color: 'var(--text2)',
        textTransform: 'uppercase', letterSpacing: '0.3px',
      }} title="OTel span links — causal pointers this trace declares (→) or receives (←), e.g. producer→consumer or batch fan-in">
        ⛓ Linked traces
      </span>
      {rows.map(r => (
        <span key={`${r.dir}:${r.other}`}
          style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }}>
          <span style={{ color: 'var(--text3)', fontSize: 11 }}
            title={r.dir === 'out' ? 'This trace links to' : 'Linked from another trace'}>
            {r.dir === 'out' ? '→ links to' : '← linked from'}
          </span>
          <Link to={`/trace?id=${r.other}`}
            style={{ fontFamily: 'ui-monospace, monospace', fontSize: 11 }}>
            {r.other.slice(0, 8)}…
          </Link>
          <CopyButton value={r.other} title="Copy linked trace ID" />
          {r.attrs > 0 && (
            <span style={{ fontSize: 10, color: 'var(--text3)' }}
              title="Link attributes on the span link">
              · {r.attrs} attr{r.attrs === 1 ? '' : 's'}
            </span>
          )}
        </span>
      ))}
    </div>
  );
}

// TraceLogsPanel — flat list of log entries that share this trace
// id, ordered chronologically. Layout mirrors Uptrace's trace→logs
// tab: timestamp · severity · service · message preview, with the
// span_id shown as a smaller tag so the operator can correlate
// "this log line belongs to that span".
// v0.8.332 — `degraded` (a reason string) marks the pivot Phase 2 partial-
// result contract: warn chip instead of the "no logs" empty state (which
// would misread as an instrumentation gap), table still renders, tab never
// blocks.
function TraceLogsPanel({ logs, degraded, eventRows, traceServices }: {
  logs: LogRow[] | null | undefined;
  degraded?: string | null;
  // v0.8.407 — pseudo rows from the trace's own span events (zero-ES
  // leg): merged into the list, shown alone when the backend has
  // nothing / fails, so the tab is useful even without shipped logs.
  eventRows: LogRow[];
  traceServices: string[];
}) {
  if (logs === undefined) return <Spinner />;
  if (logs === null) {
    // Backend errored — the span events still tell part of the story.
    if (eventRows.length > 0) {
      return (
        <>
          <div style={{ padding: '0 10px 6px' }}>
            <span className="badge b-err">⚠ Failed to load logs — showing the trace's span events only</span>
          </div>
          <LogTable logs={[...eventRows].sort((a, b) => a.timestamp - b.timestamp)} hideTraceColumn />
        </>
      );
    }
    return <Empty icon="⚠" title="Failed to load logs" />;
  }
  if (logs.length === 0 && eventRows.length === 0 && !degraded) {
    return <TraceLogsEmptyDiagnostics traceServices={traceServices} />;
  }
  // Ascending chronological order — lines up with the trace
  // waterfall above. Display reuses the shared <LogTable> so
  // severity colouring / row expand layout / attribute tables
  // stay consistent with the /logs page; operators don't
  // re-learn a second viewer when they drill in from a trace.
  const sorted = [...logs, ...eventRows].sort((a, b) => a.timestamp - b.timestamp);
  return (
    <>
      {degraded && (
        <div style={{ padding: '0 10px 6px' }}>
          <span className="badge b-warn" title={degraded}>
            ⚠ Log backend slow/unreachable — partial results
          </span>
        </div>
      )}
      <div style={{
        display: 'flex', gap: 10, padding: '6px 10px',
        fontSize: 11, color: 'var(--text3)',
      }}>
        <span>
          {logs.length} log line{logs.length === 1 ? '' : 's'}
          {eventRows.length > 0 && ` + ${eventRows.length} span event${eventRows.length === 1 ? '' : 's'}`}
        </span>
      </div>
      <LogTable logs={sorted} hideTraceColumn />
    </>
  );
}

// TraceLogsEmptyDiagnostics (v0.8.407, D leg) — "0 log" has two very
// different causes and the old static hint couldn't tell them apart:
// the services genuinely logged nothing, or the shipper writes logs
// WITHOUT a trace field (correlation broken → every trace's tab is
// empty). The logstore trace-context report (field_caps + sampled
// coverage, 5-min server cache; viewer-safe route) names the broken
// services so the operator gets an actionable answer. Fetched ONLY
// when the tab is open AND empty — never on the hot path.
function TraceLogsEmptyDiagnostics({ traceServices }: { traceServices: string[] }) {
  const q = useQuery({
    queryKey: ['logstore', 'trace-context'],
    queryFn: () => api.logstoreTraceContext(),
    staleTime: 300_000, // matches the server's 5-min serveCached TTL
    retry: false,
  });
  const rep = q.data?.report;
  const broken = rep?.available
    ? traceServicesWithoutTraceField(traceServices, rep.services ?? [])
    : [];
  return (
    <Empty icon="≡" title="No logs for this trace">
      {broken.length > 0 ? (
        <>
          The log backend HAS log lines for{' '}
          <b>{broken.join(', ')}</b> — but none of them carry a trace
          field, so trace→log correlation can never match. Ship logs
          with the W3C trace context (trace.id / trace_id) populated,
          e.g. via the OTel Collector's log pipeline or your logging
          library's OTel appender.
        </>
      ) : rep?.available && !rep.pivotReady ? (
        <>
          The log backend's trace field looks unusable
          (field: <code>{rep.effectiveField || 'none'}</code>, type:{' '}
          <code>{rep.effectiveType || 'absent'}</code>). Check
          Settings → Elasticsearch → field mapping.
        </>
      ) : (
        <>Make sure your collector ships logs with the W3C trace context (trace_id + span_id) populated.</>
      )}
    </Empty>
  );
}

// Severity-helpers + per-log time formatter used to live
// here for the legacy custom .trace-logs grid. After v0.5.62
// the panel renders through the shared <LogTable> which
// handles all of that itself, so the helpers are gone.

// CompareTracesButton — opens an inline form asking for a
// second trace ID, then calls /api/copilot/compare-traces.
// Renders the model's diff explanation underneath in the
// same panel style as CopilotExplain so the trace-detail
// chrome stays visually consistent. Self-hides when the
// copilot isn't configured (same gate CopilotExplain uses).
function CompareTracesButton({ aId }: { aId: string }) {
  const [enabled, setEnabled] = useState<boolean | null>(null);
  const [open, setOpen] = useState(false);
  const [bId, setBId] = useState('');
  const [busy, setBusy] = useState(false);
  const [text, setText] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    api.copilotConfig().then(c => setEnabled(c.enabled)).catch(() => setEnabled(false));
  }, []);
  if (enabled !== true) return null;

  const submit = async () => {
    const b = bId.trim().toLowerCase().replace(/^0x/, '');
    if (!/^[0-9a-f]{16,32}$/.test(b)) {
      setError('Trace ID must be 16-32 hex characters.');
      return;
    }
    if (b === aId.toLowerCase()) {
      setError('Pick a DIFFERENT trace to compare with.');
      return;
    }
    setBusy(true); setError(null); setText(null);
    try {
      const r = await api.copilotCompareTraces(aId, b);
      setText(r.explanation);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Compare failed');
    } finally {
      setBusy(false);
    }
  };

  if (!open) {
    return (
      <Button variant="secondary" size="sm" onClick={() => setOpen(true)}
        title="Diff this trace against another by ID — model explains why they diverged"
        leftIcon={<IconSparkles />}>
        <span>Compare with…</span>
      </Button>
    );
  }
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8, width: '100%' }}>
      <div style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
        <input value={bId}
          onChange={e => setBId(e.target.value)}
          onKeyDown={e => { if (e.key === 'Enter') submit(); }}
          placeholder="Other trace ID (hex)"
          className="mono"
          style={{ width: 320, fontSize: 12 }} />
        <Button variant="primary" size="sm" onClick={submit} disabled={busy}>
          {busy ? 'Thinking…' : 'Compare'}
        </Button>
        <Button variant="secondary" size="sm"
          onClick={() => { setOpen(false); setText(null); setError(null); }}>
          Cancel
        </Button>
      </div>
      {error && (
        <div style={{
          padding: 10, borderRadius: 6, fontSize: 12,
          background: 'color-mix(in oklab, var(--err) 10%, transparent)', color: 'var(--err)',
          border: '1px solid color-mix(in oklab, var(--err) 25%, transparent)', maxWidth: 720,
        }}>{error}</div>
      )}
      {text && (
        <div style={{
          padding: 12, borderRadius: 6, fontSize: 13, lineHeight: 1.5,
          background: 'var(--accent-soft)',
          border: '1px solid color-mix(in oklab, var(--accent) 25%, transparent)',
          color: 'var(--text)', whiteSpace: 'pre-wrap', maxWidth: 720,
        }}>
          <div style={{ fontSize: 10, color: 'var(--accent2)', marginBottom: 6, fontWeight: 700, letterSpacing: '.5px',
                        display: 'inline-flex', alignItems: 'center', gap: 4 }}>
            <IconSparkles size={11} /> COMPARE
          </div>
          {text}
        </div>
      )}
    </div>
  );
}

export default function TraceDetailPage() {
  return (
    <Suspense fallback={<Spinner />}>
      <TraceDetailInner />
    </Suspense>
  );
}


// SharePopover — Grafana-style two-tab share popover. Internal link
// is the current URL (preserves span/tab/range from the page state
// mirror). Public link mints a 24h time-boxed token via
// /api/traces/{id}/share that resolves to a no-auth /public/trace
// page; useful for handing a trace to support, customers, or anyone
// outside Coremetry without giving them an account.
function SharePopover({ traceId }: { traceId: string }) {
  const { user } = useAuth();
  // v0.8.102 — minting a public link is now open to any authenticated
  // user, viewers included (operator request: viewers hand traces to
  // support/vendors too; the backend audits every mint with the
  // actor's email). Revoke stays editor+ so a viewer can't nuke the
  // shared pool of active links — hence the separate canRevoke gate
  // on the per-share Revoke button below.
  const canShare = !!user;
  const canRevoke = user?.role === 'admin' || user?.role === 'editor';
  const wrapRef = useRef<HTMLDivElement>(null);
  const [open, setOpen] = useState(false);
  const [internalCopied, setInternalCopied] = useState(false);
  const [publicURL, setPublicURL] = useState<string | null>(null);
  const [publicCopied, setPublicCopied] = useState(false);
  const [publicExpiresAt, setPublicExpiresAt] = useState<number | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // TTL picker — operator picks the share-link lifetime
  // instead of always getting 24h. Banking-scale operators
  // sharing with vendors / support tickets routinely want
  // 7-30d; in-team handoffs default to 24h.
  const [ttlHours, setTtlHours] = useState(24);
  // Active shares for this trace. Loaded when the popover
  // opens; refreshed after mint / revoke.
  type ShareRow = Awaited<ReturnType<typeof api.listTraceShares>>;
  const [shares, setShares] = useState<NonNullable<ShareRow>>([]);
  const reloadShares = async () => {
    try {
      const list = await api.listTraceShares(traceId);
      setShares(list ?? []);
    } catch { setShares([]); }
  };

  // Click-outside to close.
  useEffect(() => {
    if (!open) return;
    const onDoc = (e: MouseEvent) => {
      if (!wrapRef.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener('mousedown', onDoc);
    return () => document.removeEventListener('mousedown', onDoc);
  }, [open]);

  // Fetch active shares when popover opens so the operator sees
  // what's already out there before minting another.
  useEffect(() => {
    if (open) void reloadShares();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  const copyInternal = async () => {
    if (typeof window === 'undefined') return;
    await copyToClipboard(window.location.href);
    setInternalCopied(true);
    setTimeout(() => setInternalCopied(false), 2000);
  };

  const generatePublic = async () => {
    setBusy(true); setError(null);
    try {
      const res = await api.shareTrace(traceId, ttlHours);
      setPublicURL(res.url);
      setPublicExpiresAt(res.expiresAt);
      // Auto-copy on generate so the common case is one-click.
      await copyToClipboard(res.url);
      setPublicCopied(true);
      setTimeout(() => setPublicCopied(false), 2000);
      void reloadShares();
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to mint share link');
    } finally {
      setBusy(false);
    }
  };

  const copyPublic = async () => {
    if (!publicURL) return;
    await copyToClipboard(publicURL);
    setPublicCopied(true);
    setTimeout(() => setPublicCopied(false), 2000);
  };

  const revoke = async (token: string) => {
    try {
      await api.revokeTraceShare(token);
      // If we revoked the one just minted, clear the URL slot
      // so the popover returns to the "generate" affordance.
      if (publicURL && publicURL.includes(token)) {
        setPublicURL(null);
        setPublicExpiresAt(null);
      }
      void reloadShares();
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Revoke failed');
    }
  };

  return (
    <div ref={wrapRef} style={{ position: 'relative' }}>
      {/* v0.8.543 — same accent tint + leftIcon as ShareButton, so the
          Share control reads the same on /trace as on /problems, /logs and
          /explore. Operator-reported: it was the lone `secondary` grey.
          The atom's leftIcon already does the inline-flex/gap this used to
          hand-roll. Trigger only — the popover behind it stays a share
          MANAGER (public token mint, TTL, revoke), which is why it isn't
          folded into ShareButton. */}
      <Button variant="accent"
        onClick={() => setOpen(o => !o)}
        title="Share this trace"
        leftIcon={<IconLink />}>
        Share
      </Button>
      {open && (
        <div role="dialog" style={{
          position: 'absolute', right: 0, top: 'calc(100% + 6px)', zIndex: 60,
          width: 380, padding: 12,
          background: 'var(--bg2)', border: '1px solid var(--border)',
          borderRadius: 6, boxShadow: '0 8px 24px rgba(0,0,0,0.30)',
        }}>
          {/* Internal link section */}
          <div style={{ fontSize: 11, fontWeight: 600, color: 'var(--text2)',
                        textTransform: 'uppercase', letterSpacing: '0.3px', marginBottom: 6 }}>
            Internal link
          </div>
          <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 8, lineHeight: 1.5 }}>
            For Coremetry users — preserves your selected span, tab, and time range.
          </div>
          <Button variant="secondary" onClick={copyInternal}
            leftIcon={internalCopied ? <IconCheck /> : <IconLink />}
            style={{ width: '100%', display: 'inline-flex', justifyContent: 'center',
                     color: internalCopied ? 'var(--ok)' : undefined }}>
            {internalCopied ? 'Copied' : 'Copy current URL'}
          </Button>

          {canShare && (
            <>
          {/* Divider */}
          <div style={{ borderTop: '1px solid var(--border)', margin: '14px -12px' }} />

          {/* Public link section */}
          <div style={{ fontSize: 11, fontWeight: 600, color: 'var(--text2)',
                        textTransform: 'uppercase', letterSpacing: '0.3px', marginBottom: 6 }}>
            Public link
          </div>
          <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 8, lineHeight: 1.5 }}>
            Anyone with this URL can view a read-only snapshot — no Coremetry account needed.
          </div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 6, marginBottom: 8, fontSize: 11 }}>
            <span style={{ color: 'var(--text2)' }}>Expires in:</span>
            <select value={ttlHours}
              onChange={e => setTtlHours(Number(e.target.value))}
              style={{ fontSize: 11, padding: '2px 4px' }}>
              <option value={1}>1 hour</option>
              <option value={24}>24 hours</option>
              <option value={24 * 7}>7 days</option>
              <option value={24 * 30}>30 days</option>
            </select>
          </div>
          {!publicURL ? (
            <Button onClick={generatePublic} disabled={busy}
              leftIcon={<IconLink />}
              style={{ width: '100%', display: 'inline-flex', justifyContent: 'center' }}>
              {busy ? 'Generating…' : 'Generate public link'}
            </Button>
          ) : (
            <>
              <div style={{ display: 'flex', gap: 6 }}>
                <input value={publicURL} readOnly
                  onClick={e => (e.target as HTMLInputElement).select()}
                  style={{ flex: 1, fontSize: 11, fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace' }} />
                <Button variant="secondary" size="sm" onClick={copyPublic}
                  leftIcon={publicCopied ? <IconCheck /> : <IconLink />}
                  style={{ color: publicCopied ? 'var(--ok)' : undefined }}>
                  {publicCopied ? 'Copied' : 'Copy'}
                </Button>
              </div>
              {publicExpiresAt && (
                <div style={{ fontSize: 10, color: 'var(--text3)', marginTop: 6 }}
                     title={tsLong(publicExpiresAt)}>
                  Expires {tsRel(publicExpiresAt)}
                </div>
              )}
            </>
          )}
          {error && (
            <div style={{ marginTop: 8, fontSize: 11, color: 'var(--err)' }}>{error}</div>
          )}
          {/* Active shares for this trace — operator audits
              what's already out there + revokes leaks
              without having to remember tokens. Empty list
              hides the whole block so the popover stays
              tidy when there's nothing to manage. */}
          {shares.length > 0 && (
            <div style={{ marginTop: 14, paddingTop: 12, borderTop: '1px solid var(--border)' }}>
              <div style={{ fontSize: 11, fontWeight: 600, color: 'var(--text2)',
                            textTransform: 'uppercase', letterSpacing: '0.3px', marginBottom: 6 }}>
                Active shares ({shares.length})
              </div>
              <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
                {shares.map(s => (
                  <div key={s.token} style={{
                    display: 'flex', alignItems: 'center', gap: 6,
                    fontSize: 11, color: 'var(--text2)',
                  }}>
                    <span style={{
                      fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
                      flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                    }} title={`${s.createdBy || 'unknown'} · expires ${tsLong(s.expiresAt)}`}>
                      …{s.token.slice(-8)} · {s.createdBy || 'unknown'}
                    </span>
                    <span style={{ fontSize: 10, color: 'var(--text3)', whiteSpace: 'nowrap' }}
                          title={tsLong(s.expiresAt)}>
                      {tsRel(s.expiresAt)}
                    </span>
                    {canRevoke && (
                      <Button variant="secondary" size="sm"
                        onClick={() => revoke(s.token)}
                        style={{ color: 'var(--err)' }}>
                        Revoke
                      </Button>
                    )}
                  </div>
                ))}
              </div>
            </div>
          )}
            </>
          )}
        </div>
      )}
    </div>
  );
}


// exportTraceJSON triggers a browser download of the full trace as a
// pretty-printed JSON file. Filename includes a short trace-id prefix
// so a folder of exports stays scannable. Pure client-side — no
// extra round-trip; the spans are already loaded.
function exportTraceJSON(traceId: string, spans: unknown[]) {
  const payload = JSON.stringify({ traceId, spans }, null, 2);
  const blob = new Blob([payload], { type: 'application/json' });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = `trace-${traceId.slice(0, 12)}.json`;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}

// v0.6.34 — small inline KPI tile for the aged-out stub panel.
// Kept here rather than reusing /admin/clickhouse's KPI because
// that one carries the page's specific styling assumptions and
// would tangle the import graph.
function KPI({ label, value, tone }: {
  label: string;
  value: string;
  tone?: 'err';
}) {
  return (
    <div style={{
      padding: '8px 10px', borderRadius: 4,
      background: 'var(--bg)', border: '1px solid var(--border)',
    }}>
      <div style={{
        fontSize: 10, color: 'var(--text3)', textTransform: 'uppercase',
        letterSpacing: 0.4, marginBottom: 2,
      }}>{label}</div>
      <div style={{
        fontSize: 13, fontWeight: 600,
        color: tone === 'err' ? 'var(--err)' : 'var(--text)',
        fontFamily: 'ui-monospace, monospace',
      }}>{value}</div>
    </div>
  );
}
