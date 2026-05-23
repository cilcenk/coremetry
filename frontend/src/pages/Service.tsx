import { Suspense, useEffect, useMemo, useRef, useState } from 'react';
import { Link } from 'react-router-dom';
import { useSearchParams } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { ServiceStructure } from '@/components/ServiceStructure';
import { ServiceCharts } from '@/components/ServiceCharts';
import { LazyMount } from '@/components/LazyMount';
import { LatencyHeatmap } from '@/components/LatencyHeatmap';
import { ServiceCatalogPill } from '@/components/ServiceCatalogPill';
import { SamplingChip } from '@/components/SamplingChip';
import { TopologyMuteChip } from '@/components/TopologyMuteChip';
import { DBQueriesPanel } from '@/components/DBQueriesPanel';
import { DeployHistoryPanel } from '@/components/DeployHistoryPanel';
import { ServiceNeighbors } from '@/components/ServiceNeighbors';
import { ServiceInfra } from '@/components/ServiceInfra';
import { Sparkline } from '@/components/Sparkline';
import { MultiLineChart } from '@/components/MultiLineChart';
import { Modal } from '@/components/ui';
import { SpanBreakdownChart } from '@/components/SpanBreakdownChart';
import { ServiceProfilingPanel } from '@/components/ServiceProfilingPanel';
import { ServiceAttrsPanel } from '@/components/ServiceAttrsPanel';
import { api } from '@/lib/api';
import { fmtNum, timeRangeToNs } from '@/lib/utils';
import { encodeFilters, encodeRange, buildQuery } from '@/lib/urlState';
import { useQueryClient } from '@tanstack/react-query';
import { keys } from '@/lib/queries/keys';
import type { Service, Problem, TimeRange, OperationSummary, SpanMetricSeries } from '@/lib/types';

const SINCE_MAP: Record<string, string> = {
  '5m': '5m', '10m': '10m', '15m': '15m', '30m': '30m',
  '1h': '1h', '3h': '3h', '6h': '6h', '12h': '12h',
  '24h': '24h', '2d': '48h', '7d': '168h', '30d': '720h',
};

type ServiceTab = 'operations' | 'details';

function ServiceDetailInner() {
  const [searchParams, setSearchParams] = useSearchParams();
  const svc = searchParams.get('name') ?? '';

  const queryClient = useQueryClient();
  const [range, setRange] = useState<TimeRange>({ preset: '30m' });
  const [info, setInfo] = useState<Service | null>(null);
  const [problems, setProblems] = useState<Problem[]>([]);
  const [operations, setOperations] = useState<OperationSummary[]>([]);
  const [loading, setLoading] = useState(true);

  // Memoize the absolute window so JSX-level reads (passed as
  // fromNs/toNs props to child fetchers) don't change identity
  // on every render — without this, a relative range like
  // { preset: '30m' } evaluates a fresh now() each paint and
  // the children's useEffect([fromNs, toNs, …]) deps thrash
  // into an infinite refetch.
  const rangeNs = useMemo(() => timeRangeToNs(range), [range]);

  // v0.5.292 — tab in URL so a refresh / shareable link lands
  // on the same sub-view. Default = Operations (the operator's
  // daily entry point).
  const tab = (searchParams.get('tab') as ServiceTab | null) === 'details'
    ? 'details' as const
    : 'operations' as const;
  // v0.5.307 — scroll to a hash anchor (#deploys, etc.) once
  // the Details tab body actually exists in the DOM. Browser
  // doesn't auto-scroll because the target node is rendered
  // AFTER the initial paint (bundle fetch + tab gate). The
  // ?tab=details&#deploys link from /deploys depends on this.
  useEffect(() => {
    if (loading) return;
    const hash = window.location.hash;
    if (!hash) return;
    const id = hash.replace(/^#/, '');
    if (!id) return;
    // Wait one frame so the conditional <div id="..."> has
    // landed in the DOM before we try to scroll.
    requestAnimationFrame(() => {
      const el = document.getElementById(id);
      if (el) {
        el.scrollIntoView({ behavior: 'smooth', block: 'start' });
      }
    });
  }, [loading, tab]);
  const setTab = (next: ServiceTab) => setSearchParams(prev => {
    const p = new URLSearchParams(prev);
    if (next === 'operations') p.delete('tab'); else p.set('tab', next);
    return p;
  }, { replace: true });

  useEffect(() => {
    if (!svc) return;
    setLoading(true);
    const r = timeRangeToNs(range);
    // Single bundled fetch — the backend fans out KPI lookup,
    // problems list, operations table, and deploy markers to
    // CH in parallel goroutines and ships one JSON response,
    // cached 60s with SWR. At billion-span scale: 4 network
    // round trips + 4 serial CH cold-cache costs collapse
    // into 1 round trip and 1 parallel CH window. Heatmap /
    // RED charts / cluster breakdown stay separate; they
    // own their own time-window semantics (compare period,
    // lazy mount) the bundle can't bake in without coupling.
    //
    // Bundle's deploys slot is hand-seeded into the React
    // Query cache under the deploys-forService key, so the
    // ServiceCharts component's useServiceDeploys hook
    // resolves instantly (cache HIT) instead of firing its
    // own /api/services/.../deploys round trip below the
    // fold. Same window the bundle is for.
    let cancelled = false;
    const applyBundle = (b: Awaited<ReturnType<typeof api.serviceBundle>>) => {
      if (cancelled) return;
      setInfo(b?.service ?? null);
      setProblems(b?.problems ?? []);
      setOperations(b?.operations ?? []);
      if (b?.deploys) {
        queryClient.setQueryData(
          keys.deploys.forService(svc, r.from ?? 0, r.to ?? 0),
          b.deploys,
        );
      }
    };
    api.serviceBundle(svc, r)
      .then(async (b) => {
        applyBundle(b);
        // v0.5.300 — Operator-reported: at scale (test env) the
        // bundle occasionally returned operations=[] even when
        // the service summary itself shows spans > 0. Backend
        // now has an MV→raw-spans fallback (chstore repo), but
        // a stale Redis cache from BEFORE the backend fix might
        // still serve the empty array. Once. Auto-refresh
        // (?refresh=1 bypasses the cache + recomputes) when we
        // detect that signature: service has traffic AND
        // operations came up empty AND the bundle wasn't already
        // forced. Cached afterward so this is a one-shot rescue.
        if (!cancelled
            && b
            && b.service && b.service.spanCount > 0
            && (!b.operations || b.operations.length === 0)) {
          const refreshed = await api.serviceBundle(svc, r, { refresh: true })
            .catch(() => null);
          if (refreshed) applyBundle(refreshed);
        }
      })
      .catch(() => {
        if (cancelled) return;
        setInfo(null); setProblems([]); setOperations([]);
      })
      .finally(() => { if (!cancelled) setLoading(false); });
    return () => { cancelled = true; };
  }, [svc, range, queryClient]);

  if (!svc) {
    return (
      <>
        <Topbar title="Service" range={range} onRangeChange={setRange} />
        <div id="content"><Empty icon="⚠" title="Missing service name" /></div>
      </>
    );
  }

  const openProbs = problems.filter(p => p.status === 'open');

  return (
    <>
      <Topbar title={`Service · ${svc}`} range={range} onRangeChange={setRange} />
      <div id="content">
        <div style={{ display: 'flex', gap: 12, alignItems: 'center', marginBottom: 14, flexWrap: 'wrap' }}>
          <Link to="/services" className="sec" style={{
            padding: '5px 12px', border: '1px solid var(--border)',
            borderRadius: 6, fontSize: 12, color: 'var(--text)', textDecoration: 'none',
          }}>← All services</Link>
          {info && (
            <>
              <KPI label="Spans" value={fmtNum(info.spanCount)} />
              <KPI label="Errors" value={`${info.errorRate.toFixed(2)}%`}
                cls={info.errorRate > 5 ? 'err' : info.errorRate > 0 ? 'warn' : 'ok'} />
              <KPI label="Avg" value={`${info.avgDurationMs.toFixed(1)}ms`} />
              <KPI label="P99" value={`${info.p99DurationMs.toFixed(1)}ms`} />
              <SamplingChip service={svc} spanCount={info.spanCount} range={range} />
              <TopologyMuteChip service={svc} />
            </>
          )}
          <Link to={`/service/backtrace?name=${encodeURIComponent(svc)}`} style={{
            marginLeft: 'auto', fontSize: 12, padding: '5px 12px',
            background: 'var(--bg3)', border: '1px solid var(--border)',
            borderRadius: 6, color: 'var(--accent2)', textDecoration: 'none',
          }} title="Inbound callers — service / pod / IP backtrace">↩ Backtrace</Link>
          <Link to={`/traces?service=${encodeURIComponent(svc)}`} style={{
            fontSize: 12, padding: '5px 12px',
            background: 'var(--bg3)', border: '1px solid var(--border)',
            borderRadius: 6, color: 'var(--accent2)', textDecoration: 'none',
          }}>⋮ View traces</Link>
          {/* Profiling deep-link (v0.5.161). Opens /profiling
              pre-filtered to this service so the operator goes
              from "latency looks weird" → flamegraph in one
              hop. Always shown — if profiling isn't wired up
              yet, the page surfaces a setup-recipes CTA. */}
          <Link to={`/profiling?service=${encodeURIComponent(svc)}`} style={{
            fontSize: 12, padding: '5px 12px',
            background: 'var(--bg3)', border: '1px solid var(--border)',
            borderRadius: 6, color: 'var(--accent2)', textDecoration: 'none',
          }} title="Continuous profiling — CPU + heap flamegraphs for this service">
            🔥 Profiles
          </Link>
          {/* Logs deep-link (v0.5.225). Same one-hop jump as
              Profiles — opens /logs filtered to this service so
              an operator who spots an error spike on the RED
              charts above lands on the matching log stream
              without a sidebar trip. */}
          <Link to={`/logs?service=${encodeURIComponent(svc)}`} style={{
            fontSize: 12, padding: '5px 12px',
            background: 'var(--bg3)', border: '1px solid var(--border)',
            borderRadius: 6, color: 'var(--accent2)', textDecoration: 'none',
          }} title="Open /logs filtered to this service">
            ≡ Logs
          </Link>
        </div>
        {/* Service catalog metadata — owner team / oncall /
            runbook / repo. Operator-curated; falls back to a
            single "+ Add catalog metadata" CTA when empty.
            Editor+ role can edit inline. */}
        <div style={{ marginBottom: 12 }}>
          <ServiceCatalogPill service={svc} />
        </div>

        {openProbs.length > 0 && (
          <div style={{
            border: '1px solid rgba(255,82,82,.4)',
            background: 'rgba(255,82,82,.06)',
            borderRadius: 6, padding: 12, marginBottom: 14,
          }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8 }}>
              <span style={{ color: 'var(--err)', fontWeight: 600 }}>
                ! {openProbs.length} open problem{openProbs.length === 1 ? '' : 's'} on {svc}
              </span>
              <span style={{ flex: 1 }} />
              <Link to={`/problems?service=${encodeURIComponent(svc)}`} style={{ fontSize: 11 }}>
                View all for this service →
              </Link>
            </div>
            <div style={{
              display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(380px, 1fr))', gap: 8,
            }}>
              {openProbs.map(p => {
                const sevCls = p.severity === 'critical' ? 'b-err' : 'b-warn';
                return (
                  <div key={p.id} style={{
                    padding: 8, borderRadius: 4,
                    background: 'var(--bg2)', border: '1px solid var(--border)',
                  }}>
                    <div style={{ display: 'flex', alignItems: 'center', gap: 6, marginBottom: 4 }}>
                      <span className={`badge ${sevCls}`} style={{ fontSize: 10 }}>
                        {p.severity.toUpperCase()}
                      </span>
                      <span style={{ fontSize: 12, fontWeight: 600 }}>{p.ruleName}</span>
                    </div>
                    <div style={{ fontSize: 11, color: 'var(--text2)' }}>
                      <span style={{ fontFamily: 'monospace' }}>{p.metric}</span>
                      {' = '}
                      <b style={{ color: 'var(--err)' }}>{Number(p.value).toFixed(2)}</b>
                      {' '}(threshold {Number(p.threshold).toFixed(2)})
                    </div>
                    {p.description && (
                      <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
                        {p.description}
                      </div>
                    )}
                    <div style={{ fontSize: 10, color: 'var(--text3)', marginTop: 4, fontFamily: 'monospace' }}>
                      since {new Date(p.startedAt / 1e6).toLocaleString()}
                    </div>
                  </div>
                );
              })}
            </div>
          </div>
        )}

        {loading && <Spinner />}
        {!loading && (
          <>
            {/* v0.5.293 — Operator-reported: tabs go immediately
                under the KPI / problems header so the
                Operations table is the FIRST body element on
                the page. DeployHistoryPanel + ServiceCharts
                moved into Details (they remain the headline
                summary view but no longer outrank the
                per-endpoint table). Tab persists in the URL
                so a saved link / refresh lands on the same
                sub-view. */}
            <TabStrip
              tab={tab}
              onChange={setTab}
              opCount={operations.length} />

            {tab === 'operations' && (
              <OperationsTable service={svc} rows={operations} range={range}
                preset={range.preset}
                onWiden={() => setRange({ preset: '1h' })} />
            )}
            {tab === 'details' && (
              <>
                {/* v0.5.302 — Above-the-fold panels stay eager
                    (operator sees them instantly); everything
                    further down wraps in <LazyMount> so it
                    fetches only when scrolled into view. Before:
                    Details open fired ~12 parallel CH queries
                    in a burst → CH busy → blank-then-pop flash.
                    Now: 3 immediate queries, the rest queue
                    progressively. Mounted panels stay mounted
                    so scroll-back keeps the data. */}
                {/* Above-the-fold (eager): ServiceInfra +
                    DeployHistory + ServiceCharts. The operator
                    lands on these without scrolling. */}
                <ServiceInfra     service={svc} since={SINCE_MAP[range.preset] ?? '15m'} />
                {/* v0.5.307 — #deploys anchor so /deploys page
                    "history →" link can scroll-to here after
                    landing on Details tab. */}
                <div id="deploys">
                  <DeployHistoryPanel service={svc} />
                </div>
                <ServiceCharts service={svc} range={range}
                  onZoom={(fromUnixSec, toUnixSec) => {
                    setRange({
                      preset: 'custom',
                      fromMs: Math.round(fromUnixSec * 1000),
                      toMs: Math.round(toUnixSec * 1000),
                    });
                  }} />
                {/* Below-the-fold (lazy): each panel waits
                    until ~200px from the viewport before mounting
                    + fetching. minHeight is sized to the typical
                    rendered panel so the page doesn't jump as
                    panels resolve. */}
                <LazyMount minHeight={220}>
                  <SpanBreakdownChart service={svc}
                                      fromNs={rangeNs.from}
                                      toNs={rangeNs.to} />
                </LazyMount>
                {/* Profiling tile — Dynatrace-style "Top methods"
                    card. Self-hides when the service hasn't
                    pushed profiles, so it's a no-op for
                    services not yet wired up. */}
                <LazyMount minHeight={120}>
                  <ServiceProfilingPanel service={svc} range={range} />
                </LazyMount>
                {/* Attrs browser (v0.5.381) — "what attrs is my
                    SDK emitting" answered without opening a
                    trace. Self-hides when no sample data lands
                    yet (fresh services). */}
                <LazyMount minHeight={120}>
                  <ServiceAttrsPanel service={svc} range={range} />
                </LazyMount>
                <LazyMount minHeight={300}>
                  <ServiceNeighbors service={svc} since={SINCE_MAP[range.preset] ?? '1h'} defaultOpen />
                </LazyMount>
                <LazyMount minHeight={300}>
                  <ServiceStructure service={svc} since={SINCE_MAP[range.preset] ?? '1h'} defaultOpen />
                </LazyMount>
                <LazyMount minHeight={140}>
                  <ServiceClusterBreakdown service={svc} range={range} />
                </LazyMount>
                <LazyMount minHeight={300}>
                  <DBQueriesPanel   service={svc}
                                    from={rangeNs.from}
                                    to={rangeNs.to}
                                    defaultOpen />
                </LazyMount>
                <LazyMount minHeight={360}>
                  <ServiceLatencyHeatmap service={svc} range={range} />
                </LazyMount>
              </>
            )}
          </>
        )}
      </div>
    </>
  );
}

function KPI({ label, value, cls }: { label: string; value: string; cls?: string }) {
  return (
    <div style={{
      padding: '4px 12px', border: '1px solid var(--border)',
      borderRadius: 6, background: 'var(--bg1)', fontSize: 12,
    }}>
      <span style={{ color: 'var(--text2)' }}>{label}: </span>
      <b style={{
        color: cls === 'err' ? 'var(--err)' : cls === 'warn' ? 'var(--warn)'
          : cls === 'ok' ? 'var(--ok)' : 'var(--text)',
      }}>{value}</b>
    </div>
  );
}

// OperationsTable — per-operation aggregate (count / err / avg / p50 /
// p95 / p99 / apdex). Click an operation to drill into Explore with
// `name = <op>` pre-filtered alongside the service. Sortable; aggregate
// "All" row at the top mirrors the services page so totals are visible
// without scrolling.
type OpSortKey = 'name' | 'spanCount' | 'errorRate' | 'avg' | 'p99' | 'apdex' | 'impact';
const OP_NATURAL_DIR: Record<OpSortKey, 'asc' | 'desc'> = {
  name: 'asc', spanCount: 'desc', errorRate: 'desc',
  avg: 'desc', p99: 'desc', apdex: 'asc', impact: 'desc',
};

// Elastic-APM's "Impact" = avg_duration × count. Surfaces the
// heaviest cumulative consumers — the operation that's slow OR
// runs a lot. A 5ms operation called 100k times shows up; a
// once-an-hour 30s job doesn't. Default sort so the top of the
// table answers "what should I optimise first" without the
// operator combining columns by eye.
function impactOf(r: OperationSummary): number {
  return r.avgDurationMs * r.spanCount;
}

// v0.5.292 — TabStrip sits between the persistent header
// (KPIs / problems / deploy markers / RED charts) and the
// per-tab body. Style mirrors the existing Topology tabs
// (no separate component yet; both surfaces use a plain
// button row + active-tab outline).
function TabStrip({ tab, onChange, opCount }: {
  tab: ServiceTab;
  onChange: (t: ServiceTab) => void;
  opCount: number;
}) {
  const items: { key: ServiceTab; label: string; hint?: string }[] = [
    { key: 'operations', label: 'Operations', hint: opCount > 0 ? `${opCount}` : undefined },
    { key: 'details',    label: 'Details' },
  ];
  return (
    <div style={{
      display: 'flex', gap: 0, marginTop: 16, marginBottom: 12,
      borderBottom: '1px solid var(--border)',
    }}>
      {items.map(it => {
        const active = tab === it.key;
        return (
          <button key={it.key} type="button"
            onClick={() => onChange(it.key)}
            style={{
              all: 'unset', cursor: 'pointer',
              padding: '8px 18px',
              fontSize: 13, fontWeight: active ? 700 : 500,
              color: active ? 'var(--text)' : 'var(--text2)',
              borderBottom: active ? '2px solid var(--accent2)' : '2px solid transparent',
              marginBottom: -1,
            }}>
            {it.label}
            {it.hint && (
              <span style={{
                marginLeft: 6, fontSize: 11, color: 'var(--text3)',
                fontFamily: 'ui-monospace, monospace',
              }}>{it.hint}</span>
            )}
          </button>
        );
      })}
    </div>
  );
}

function OperationsTable({ service, rows, range, preset, onWiden }: {
  service: string;
  rows: OperationSummary[];
  range: TimeRange;
  // v0.5.292 — when the table comes back empty (typically
  // because the user's 15m default window had no traffic),
  // surface a one-click "widen to 1h" CTA rather than the
  // bare "no operations" message. preset is read to scope
  // the suggestion ("widen to 1h" only makes sense on short
  // windows; on a 7d range, empty really means empty).
  preset?: string;
  onWiden?: () => void;
}) {
  const [sortBy, setSortBy] = useState<OpSortKey>('impact');
  const [sortDir, setSortDir] = useState<'asc' | 'desc'>('desc');
  // v0.5.374 — client-side filter. At 500+ operations on a
  // monolith service the scroll-then-eyeball loop fails;
  // typing narrows live with no server round-trip.
  const [filter, setFilter] = useState('');
  // v0.5.392 — per-row metric drill-in. Clicking the sparkline
  // opens a Modal with three synced uPlot charts (calls, errors,
  // p99) for the same (service, op) tuple. Same pattern the
  // endpoints page uses; here it pulls from the row's stored
  // sparkline + companion errors/p99 sparklines added in the
  // same release.
  const [opDetail, setOpDetail] = useState<OperationSummary | null>(null);

  // v0.5.313 — Operator-reported: drill-down used to land on
  // /traces (familiar view with the trace list + aggregate
  // tabs). Recent refactor pushed it to /explore which the
  // operator finds less direct. Reverted to /traces with the
  // service pre-selected and the operation name as the search
  // term. /traces' free-text search matches span name out of
  // the box, so an operation like "POST /payment" lands on
  // exactly the traces that touched it.
  // v0.5.317 — Operator-reported: prior link landed on the
  // Aggregated tab (default) where filter+search produced no
  // results, and rootOnly defaulted ON, hiding partial traces.
  // Now: explicit ?view=list&rootOnly=false so the operator
  // lands on the list view with every matching trace visible.
  const opHref = (op: string) =>
    `/traces?service=${encodeURIComponent(service)}&search=${encodeURIComponent(op)}&range=${encodeURIComponent(encodeRange(range))}&view=list&rootOnly=false`;

  const sorted = useMemo(() => {
    const cmp = (a: OperationSummary, b: OperationSummary): number => {
      switch (sortBy) {
        case 'name':      return a.name.localeCompare(b.name);
        case 'spanCount': return a.spanCount - b.spanCount;
        case 'errorRate': return a.errorRate - b.errorRate;
        case 'avg':       return a.avgDurationMs - b.avgDurationMs;
        case 'impact':    return impactOf(a) - impactOf(b);
        case 'p99':       return a.p99DurationMs - b.p99DurationMs;
        case 'apdex':     return (a.apdex ?? 0) - (b.apdex ?? 0);
      }
    };
    // v0.5.374 — apply filter before sort. Case-insensitive
    // substring match on the operation name, same idiom as the
    // /endpoints page filter.
    const trimmed = filter.trim().toLowerCase();
    const source = trimmed ? rows.filter(r => r.name.toLowerCase().includes(trimmed)) : rows;
    const arr = [...source].sort(cmp);
    return sortDir === 'desc' ? arr.reverse() : arr;
  }, [rows, sortBy, sortDir, filter]);

  // Same weighted-aggregate scheme as the services page totals row:
  // sum spans/errs, weight avg/apdex by span count, take max for p99.
  const agg = useMemo(() => {
    if (rows.length === 0) return null;
    let totalSpans = 0, totalErrs = 0, wAvg = 0, wApdex = 0, maxP99 = 0;
    for (const r of rows) {
      totalSpans += r.spanCount;
      totalErrs += r.errorCount;
      wAvg += r.avgDurationMs * r.spanCount;
      wApdex += (r.apdex ?? 0) * r.spanCount;
      if (r.p99DurationMs > maxP99) maxP99 = r.p99DurationMs;
    }
    return {
      spans: totalSpans, errs: totalErrs,
      errorRate: totalSpans > 0 ? (totalErrs / totalSpans) * 100 : 0,
      avgMs: totalSpans > 0 ? wAvg / totalSpans : 0,
      p99Ms: maxP99,
      apdex: totalSpans > 0 ? wApdex / totalSpans : 0,
    };
  }, [rows]);

  // Element-wise sum of every operation's sparkline → service-wide
  // call-rate trend rendered on the "All" aggregate row. Uses the
  // longest sparkline as the canvas length; shorter ones (only one
  // bucket of activity, e.g. a single-call operation) just contribute
  // zeros to the trailing slots.
  const aggSparkline = useMemo(() => {
    let len = 0;
    for (const r of rows) {
      if (r.sparkline && r.sparkline.length > len) len = r.sparkline.length;
    }
    if (len === 0) return [];
    const out = new Array(len).fill(0);
    for (const r of rows) {
      if (!r.sparkline) continue;
      for (let i = 0; i < r.sparkline.length; i++) {
        out[i] += r.sparkline[i];
      }
    }
    return out;
  }, [rows]);

  const toggleSort = (col: OpSortKey) => {
    if (sortBy === col) setSortDir(d => d === 'desc' ? 'asc' : 'desc');
    else { setSortBy(col); setSortDir(OP_NATURAL_DIR[col]); }
  };

  if (rows.length === 0) {
    // v0.5.292 — short-window (≤30 min) default hits "no
    // traffic" often enough that operators reported the page
    // as broken. Surface a one-click widen-to-1h instead of
    // the bare-empty message. Wider windows keep the plain
    // empty state since "no ops in 24h" is genuinely "no
    // ops".
    const isShortWindow = preset
      && ['5m', '10m', '15m', '30m'].includes(preset);
    return (
      <div style={{ marginTop: 18 }}>
        <h3 style={{ fontSize: 13, fontWeight: 700, marginBottom: 6 }}>⊙ Operations</h3>
        <div className="empty" style={{ padding: 30 }}>
          {isShortWindow ? (
            <>
              <div style={{ marginBottom: 12 }}>
                No traffic for <b>{service}</b> in the last <b>{preset}</b>.
                Idle or low-traffic services often don't produce
                spans in a short window.
              </div>
              {onWiden && (
                <button type="button" onClick={onWiden}
                  style={{ fontSize: 12, padding: '6px 14px' }}>
                  Widen to last 1h
                </button>
              )}
            </>
          ) : (
            <>No operations seen in this window</>
          )}
        </div>
      </div>
    );
  }

  return (
    <div style={{ marginTop: 18 }}>
      <div style={{ marginBottom: 6, display: 'flex', alignItems: 'baseline', gap: 8 }}>
        <h3 style={{ fontSize: 13, fontWeight: 700 }}>⊙ Operations</h3>
        <span style={{ fontSize: 11, color: 'var(--text3)' }}>
          {filter.trim()
            ? `${sorted.length} / ${rows.length} matching`
            : `${rows.length} distinct span name${rows.length === 1 ? '' : 's'} in ${service}`}
        </span>
        <input value={filter} onChange={e => setFilter(e.target.value)}
          placeholder="Filter by name…"
          style={{ marginLeft: 'auto', width: 240, padding: '4px 10px', fontSize: 12,
                   background: 'var(--bg)', color: 'var(--text)',
                   border: '1px solid var(--border)', borderRadius: 4 }} />
      </div>
      {/* Cap the operations table at 540 px tall and let it
          scroll inside that container — at 500+ operations on
          one service the previous full-height render put the
          KPIs above and the rest of the page sections below
          way out of reach. Sticky thead keeps the column
          labels visible while scrolling. */}
      <div className="table-wrap" style={{ maxHeight: 540, overflowY: 'auto' }}>
        <table>
          <thead style={{ position: 'sticky', top: 0, zIndex: 1, background: 'var(--bg1)' }}>
            <tr>
              <OpSortTh col="name"      label="Operation"  sort={sortBy} dir={sortDir} onSort={toggleSort} />
              <th style={{ textAlign: 'left', width: 92 }}>Trend</th>
              <OpSortTh col="impact"    label="Impact"     sort={sortBy} dir={sortDir} onSort={toggleSort} align="right" />
              <OpSortTh col="spanCount" label="Calls"      sort={sortBy} dir={sortDir} onSort={toggleSort} align="right" />
              <OpSortTh col="errorRate" label="Err %"      sort={sortBy} dir={sortDir} onSort={toggleSort} align="right" />
              <OpSortTh col="avg"       label="Avg"        sort={sortBy} dir={sortDir} onSort={toggleSort} align="right" />
              <th style={{ textAlign: 'right' }}>P50</th>
              <th style={{ textAlign: 'right' }}>P95</th>
              <OpSortTh col="p99"       label="P99"        sort={sortBy} dir={sortDir} onSort={toggleSort} align="right" />
              <OpSortTh col="apdex"     label="Apdex"      sort={sortBy} dir={sortDir} onSort={toggleSort} align="right" />
            </tr>
          </thead>
          <tbody>
            {agg && (
              <tr className="agg-row">
                <td><span style={{ fontWeight: 700 }}>All ({rows.length})</span></td>
                <td>
                  {/* Aggregate trend = element-wise sum across all
                      per-operation sparklines so the "All" row
                      shows the service-wide call rate at a glance,
                      using the same window + bucket boundaries as
                      every row beneath it. */}
                  <Sparkline values={aggSparkline} title={`total calls/bucket × ${rows.length} ops`} />
                </td>
                <td className="mono" style={{ textAlign: 'right', fontWeight: 700 }}>
                  {fmtImpact(rows.reduce((n, r) => n + impactOf(r), 0))}
                </td>
                <td className="mono" style={{ textAlign: 'right' }}>{fmtNum(agg.spans)}</td>
                <td className="mono" style={{ textAlign: 'right' }}>
                  <span className={`badge b-${agg.errorRate > 5 ? 'err' : agg.errorRate > 0 ? 'warn' : 'ok'}`}>
                    {agg.errorRate.toFixed(2)}%
                  </span>
                </td>
                <td className="mono" style={{ textAlign: 'right' }}>{agg.avgMs.toFixed(1)}ms</td>
                <td className="mono" style={{ textAlign: 'right', color: 'var(--text3)' }}>—</td>
                <td className="mono" style={{ textAlign: 'right', color: 'var(--text3)' }}>—</td>
                <td className="mono" style={{ textAlign: 'right' }}>{agg.p99Ms.toFixed(1)}ms</td>
                <td className="mono" style={{ textAlign: 'right' }}>
                  {isFinite(agg.apdex) ? agg.apdex.toFixed(2) : '—'}
                </td>
              </tr>
            )}
            {sorted.map(op => {
              const errCls = op.errorRate > 5 ? 'err' : op.errorRate > 0 ? 'warn' : 'ok';
              // Tone the per-row sparkline with the same severity
              // colour as the err-rate badge so the eye reads "this
              // op is hot" from one glance at the trend column,
              // before reading the numbers.
              const sparkColor = errCls === 'err' ? 'var(--err)'
                              : errCls === 'warn' ? 'var(--warn)'
                              : undefined;
              return (
                <tr key={op.name}
                    style={{ contentVisibility: 'auto', containIntrinsicSize: 'auto 36px' }}>
                  <td>
                    <Link
                      to={opHref(op.name)}
                      style={{ fontWeight: 500 }}
                      title="Open this operation in Explore — service + name pre-filtered"
                    >{op.name}</Link>
                  </td>
                  <td>
                    <button
                      type="button"
                      onClick={() => setOpDetail(op)}
                      title={`${fmtNum(op.spanCount)} calls — click for calls / errors / p99 detail`}
                      style={{
                        background: 'transparent', border: 0, padding: 0,
                        cursor: 'pointer', display: 'inline-block',
                      }}
                    >
                      <Sparkline values={op.sparkline ?? []}
                        color={sparkColor}
                        title="" />
                    </button>
                  </td>
                  <td className="mono" style={{ textAlign: 'right' }}>
                    <ImpactBar value={impactOf(op)}
                               max={Math.max(...rows.map(impactOf))} />
                  </td>
                  <td className="mono" style={{ textAlign: 'right' }}>{fmtNum(op.spanCount)}</td>
                  <td className="mono" style={{ textAlign: 'right' }}>
                    <span className={`badge b-${errCls === 'err' ? 'err' : errCls === 'warn' ? 'warn' : 'ok'}`}>
                      {op.errorRate.toFixed(2)}%
                    </span>
                  </td>
                  <td className="mono" style={{ textAlign: 'right' }}>{op.avgDurationMs.toFixed(1)}ms</td>
                  <td className="mono" style={{ textAlign: 'right' }}>{op.p50DurationMs.toFixed(1)}ms</td>
                  <td className="mono" style={{ textAlign: 'right' }}>{op.p95DurationMs.toFixed(1)}ms</td>
                  <td className="mono" style={{ textAlign: 'right' }}>{op.p99DurationMs.toFixed(1)}ms</td>
                  <td className="mono" style={{ textAlign: 'right' }}>
                    {isFinite(op.apdex) ? op.apdex.toFixed(2) : '—'}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      <OperationMetricModal
        service={service}
        op={opDetail}
        onClose={() => setOpDetail(null)}
        range={range}
      />
    </div>
  );
}

// OperationMetricModal — opens on per-op sparkline click. Same
// three-RED-dimensions pattern as the Endpoints modal: calls,
// errors, p99 latency, drawn as full uPlot charts with a synced
// crosshair so the operator correlates spikes across all three
// at one instant. v0.5.392 — applies the metric drill-in pattern
// to /service per-operation rows so the operator gets the same
// reading affordance everywhere they see a sparkline.
function OperationMetricModal({
  service, op, onClose, range,
}: {
  service: string;
  op: OperationSummary | null;
  onClose: () => void;
  range: TimeRange;
}) {
  const series = useMemo(() => {
    if (!op) return { calls: [] as SpanMetricSeries[], errors: [] as SpanMetricSeries[], p99: [] as SpanMetricSeries[] };
    const { from, to } = timeRangeToNs(range);
    const calls = op.sparkline ?? [];
    const errs = op.errorsSparkline ?? [];
    const p99s = op.p99Sparkline ?? [];
    const n = Math.max(calls.length, errs.length, p99s.length);
    if (n === 0 || to <= from) {
      return { calls: [], errors: [], p99: [] };
    }
    const bucketNs = (to - from) / n;
    const t = (i: number) => from + bucketNs * i + bucketNs / 2;
    const pts = (arr: number[]) => arr.map((v, i) => ({ time: t(i), value: v }));
    return {
      calls: calls.length ? [{ groupKey: ['calls'], points: pts(calls) }] : [],
      errors: errs.length ? [{ groupKey: ['errors'], points: pts(errs) }] : [],
      p99: p99s.length ? [{ groupKey: ['p99 ms'], points: pts(p99s) }] : [],
    };
  }, [op, range]);

  if (!op) return <Modal open={false} onClose={onClose} />;
  const peakCalls = (op.sparkline ?? []).reduce((m, v) => Math.max(m, v), 0);
  const totalErrs = (op.errorsSparkline ?? []).reduce((s, v) => s + v, 0);
  const maxP99 = (op.p99Sparkline ?? []).reduce((m, v) => Math.max(m, v), 0);
  const errCls = op.errorRate >= 5 ? 'err' : op.errorRate >= 1 ? 'warn' : '';
  const tracesHref =
    `/traces?service=${encodeURIComponent(service)}` +
    `&search=${encodeURIComponent(op.name)}` +
    `&range=${encodeURIComponent(encodeRange(range))}` +
    `&view=list&rootOnly=false`;
  return (
    <Modal
      open
      onClose={onClose}
      size="lg"
      title={
        <span className="mono" style={{ fontSize: 13 }}>
          {op.name}
          <span style={{ color: 'var(--text3)', marginLeft: 8, fontSize: 11 }}>
            ({service})
          </span>
        </span>
      }
    >
      <div style={{
        display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)',
        gap: 12, marginBottom: 14,
      }}>
        <OpMetricTile label="Calls" big={fmtNum(op.spanCount)}
          sub={`peak ${fmtNum(peakCalls)} / bucket`}
          series={series.calls} />
        <OpMetricTile label="Errors" big={fmtNum(op.errorCount)}
          sub={`${op.errorRate.toFixed(2)}% rate`}
          subCls={errCls}
          series={series.errors} />
        <OpMetricTile label="P99 latency"
          big={`${op.p99DurationMs.toFixed(0)} ms`}
          sub={`peak ${maxP99.toFixed(0)} ms · avg ${op.avgDurationMs.toFixed(0)} ms`}
          series={series.p99} unit="ms" />
      </div>
      <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 14 }}>
        Hover any chart to read the bucket value; crosshair syncs
        across all three so you can correlate calls / errors /
        p99 at the same instant. Total errors in window:
        {' '}<strong>{fmtNum(totalErrs)}</strong>.
      </div>
      <div style={{ display: 'flex', gap: 10 }}>
        <Link to={tracesHref} style={{ fontSize: 12, color: 'var(--accent2)' }}>
          View traces →
        </Link>
      </div>
    </Modal>
  );
}

function OpMetricTile({
  label, big, sub, subCls, series, unit,
}: {
  label: string; big: string; sub: string; subCls?: string;
  series: SpanMetricSeries[]; unit?: string;
}) {
  return (
    <div style={{
      padding: '10px 12px', border: '1px solid var(--border)',
      borderRadius: 6, background: 'var(--bg1)',
    }}>
      <div style={{ fontSize: 11, color: 'var(--text2)', marginBottom: 4 }}>{label}</div>
      <div style={{ fontSize: 22, fontWeight: 600, marginBottom: 2 }}>{big}</div>
      <div style={{
        fontSize: 11, marginBottom: 8,
        color: subCls === 'err' ? 'var(--err)' : subCls === 'warn' ? 'var(--warn)' : 'var(--text3)',
      }}>{sub}</div>
      {series.length > 0 && series[0].points.length > 0 ? (
        <MultiLineChart series={series} unit={unit} height={140} syncKey="op-detail" />
      ) : (
        <div style={{
          height: 140, display: 'flex', alignItems: 'center',
          justifyContent: 'center', color: 'var(--text3)', fontSize: 11,
        }}>no data in window</div>
      )}
    </div>
  );
}

// ImpactBar renders a horizontal proportion bar + numeric label —
// Elastic APM's signature pattern in the transaction list. Bar
// width = row impact / max impact across the visible rows so the
// busiest operation always fills the cell. Keeps the heaviest
// cumulative consumer visually obvious without forcing the
// operator to read tabular numbers.
function ImpactBar({ value, max }: { value: number; max: number }) {
  const pct = max > 0 ? (value / max) * 100 : 0;
  return (
    <div style={{ position: 'relative', minWidth: 90, display: 'inline-block' }}>
      <div style={{
        position: 'absolute', inset: 0, width: `${pct}%`,
        background: 'rgba(56,139,253,0.12)',
        borderRadius: 3,
      }} />
      <span style={{ position: 'relative', paddingRight: 4 }}>
        {fmtImpact(value)}
      </span>
    </div>
  );
}

// fmtImpact renders impact in time units. Below a second we keep
// ms with at most one decimal; past a second we promote to s/min/h
// so a 30M-ms ops job reads as "8.3h" rather than "30000000".
function fmtImpact(ms: number): string {
  if (ms < 1) return `${ms.toFixed(2)}ms`;
  if (ms < 1000) return `${ms.toFixed(1)}ms`;
  const sec = ms / 1000;
  if (sec < 60) return `${sec.toFixed(1)}s`;
  const min = sec / 60;
  if (min < 60) return `${min.toFixed(1)}min`;
  const hr = min / 60;
  return `${hr.toFixed(1)}h`;
}

function OpSortTh({ col, label, sort, dir, onSort, align }: {
  col: OpSortKey; label: string;
  sort: OpSortKey; dir: 'asc' | 'desc';
  onSort: (c: OpSortKey) => void;
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


export default function ServiceDetailPage() {
  return (
    <Suspense fallback={<Spinner />}>
      <ServiceDetailInner />
    </Suspense>
  );
}

// ServiceClusterBreakdown — sortable RED stats per cluster the
// service emitted spans from. Renders silently when there's
// only one cluster (or none), so single-cluster operators don't
// see noise. Click a row to pivot to /services?cluster=<name>
// scoped to that one cluster. The sort defaults to spanCount
// desc so the heaviest cluster lands at top — usually what an
// operator triaging "is this service slow?" wants first.
type ClusterSortKey = 'cluster' | 'calls' | 'errRate' | 'avg' | 'p99';
function ServiceClusterBreakdown({ service, range }: {
  service: string;
  range: import('@/lib/types').TimeRange;
}) {
  const [data, setData] = useState<import('@/lib/types').ServiceClusterStat[] | null | undefined>(undefined);
  const [sortBy, setSortBy] = useState<ClusterSortKey>('calls');
  const [sortDir, setSortDir] = useState<'asc' | 'desc'>('desc');

  useEffect(() => {
    setData(undefined);
    const { from, to } = timeRangeToNs(range);
    api.serviceClusters(service, from, to)
      .then(r => setData(r?.clusters ?? []))
      .catch(() => setData(null));
  }, [service, range]);

  const sorted = useMemo(() => {
    if (!data) return data;
    const arr = [...data];
    arr.sort((a, b) => {
      switch (sortBy) {
        case 'cluster': return a.cluster.localeCompare(b.cluster);
        case 'calls':   return a.spanCount - b.spanCount;
        case 'errRate': return a.errorRate - b.errorRate;
        case 'avg':     return a.avgDurationMs - b.avgDurationMs;
        case 'p99':     return a.p99DurationMs - b.p99DurationMs;
      }
    });
    return sortDir === 'desc' ? arr.reverse() : arr;
  }, [data, sortBy, sortDir]);

  // Silent when fewer than 2 clusters — single-cluster (or
  // zero-cluster, e.g. SDK without resource attrs) deployments
  // don't need the panel. Loading / error states stay quiet
  // for the same reason.
  if (!sorted || sorted.length < 2) return null;

  const toggleSort = (col: ClusterSortKey) => {
    if (sortBy === col) {
      setSortDir(d => (d === 'desc' ? 'asc' : 'desc'));
    } else {
      setSortBy(col);
      setSortDir(col === 'cluster' ? 'asc' : 'desc');
    }
  };

  return (
    <div style={{ marginBottom: 14 }}>
      <div style={{
        fontSize: 11, fontWeight: 700, marginBottom: 6, color: 'var(--text2)',
        textTransform: 'uppercase', letterSpacing: 0.4,
      }}>
        Per-cluster breakdown <span style={{
          fontWeight: 400, color: 'var(--text3)', textTransform: 'none',
        }}>· {sorted.length} clusters</span>
      </div>
      <div className="table-wrap">
        <table>
          <thead>
            <tr>
              <ClusterTh col="cluster" label="Cluster" sort={sortBy} dir={sortDir} onSort={toggleSort} />
              <ClusterTh col="calls"   label="Calls"   sort={sortBy} dir={sortDir} onSort={toggleSort} align="right" />
              <ClusterTh col="errRate" label="Err %"   sort={sortBy} dir={sortDir} onSort={toggleSort} align="right" />
              <ClusterTh col="avg"     label="Avg"     sort={sortBy} dir={sortDir} onSort={toggleSort} align="right" />
              <ClusterTh col="p99"     label="P99"     sort={sortBy} dir={sortDir} onSort={toggleSort} align="right" />
            </tr>
          </thead>
          <tbody>
            {sorted.map(c => {
              const errCls = c.errorRate > 5 ? 'err' : c.errorRate > 0 ? 'warn' : 'ok';
              return (
                <tr key={c.cluster}>
                  <td>
                    <Link to={`/services?cluster=${encodeURIComponent(c.cluster)}`}
                          style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }}
                          title={`Filter /services to cluster ${c.cluster}`}>
                      {c.cluster}
                    </Link>
                  </td>
                  <td className="num mono">{fmtNum(c.spanCount)}</td>
                  <td className="num mono">
                    <span className={`badge b-${errCls}`}>{c.errorRate.toFixed(2)}%</span>
                  </td>
                  <td className="num mono">{c.avgDurationMs.toFixed(1)}ms</td>
                  <td className="num mono">{c.p99DurationMs.toFixed(1)}ms</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </div>
  );
}

// ServiceLatencyHeatmap fetches the heatmap for the current
// service + window and renders it under a collapsible
// section. Uses the existing /api/spans/heatmap endpoint
// with a single service_name filter — that endpoint already
// uses the primary-key partition prune so this is cheap
// even on a 24h window.
function ServiceLatencyHeatmap({ service, range }: {
  service: string;
  range: import('@/lib/types').TimeRange;
}) {
  const [data, setData] = useState<import('@/lib/types').LatencyHeatmap | null | undefined>(undefined);
  // Same service usually runs across multiple clusters
  // simultaneously (eu-west + eu-central + us-east as one
  // logical fleet). We load the cluster set from the
  // per-service breakdown endpoint that already powers the
  // table above and let the operator pivot the heatmap to
  // any single cluster — or "All clusters" (the merged
  // distribution, default) which is the union view a
  // global-latency owner cares about.
  const [clusters, setClusters] = useState<string[]>([]);
  const [picked, setPicked] = useState<string>(''); // '' = all
  // Collapse state — defaults open. Persisted to localStorage
  // so an operator who'd rather hide the panel doesn't fight
  // it on every reload. Keyed globally (not per-service) so
  // the preference is a one-time setting.
  const [collapsed, setCollapsed] = useState<boolean>(() => {
    try { return localStorage.getItem('svc.heatmap.collapsed') === '1'; }
    catch { return false; }
  });

  // Lazy-mount gate — the heatmap is below the RED chart row
  // and the operations table, so on a wide enough viewport
  // it's often only seen after a scroll. Until the section
  // has been visible at least once (or sits within 200px of
  // the viewport), the spanHeatmap fetch is deferred. Keeps
  // the cold initial render from competing for CH bandwidth
  // with the above-the-fold queries.
  //
  // Sticky: once the operator has seen the heatmap, we leave
  // hasBeenVisible=true so subsequent scrolls / re-mounts
  // don't toggle it back to "deferred" and re-skip fetches.
  const containerRef = useRef<HTMLDivElement | null>(null);
  const [hasBeenVisible, setHasBeenVisible] = useState(false);
  useEffect(() => {
    if (hasBeenVisible) return;
    if (!containerRef.current) return;
    if (typeof IntersectionObserver === 'undefined') {
      // Old browser fallback — eagerly mount, same behaviour
      // as before lazy-mount existed.
      setHasBeenVisible(true);
      return;
    }
    const io = new IntersectionObserver(entries => {
      for (const e of entries) {
        if (e.isIntersecting) {
          setHasBeenVisible(true);
          io.disconnect();
          break;
        }
      }
    }, {
      // Start fetching 200px before the panel enters the
      // viewport so the data is usually ready by the time
      // the operator reads its top edge.
      rootMargin: '200px',
    });
    io.observe(containerRef.current);
    return () => io.disconnect();
  }, [hasBeenVisible]);

  // Pull the cluster set whenever the service or window
  // changes. Cheap (cached server-side 30s) and keeps the
  // dropdown in sync with whatever traffic is in the window.
  // Gated on visibility too — no point loading the cluster
  // list for a panel the operator hasn't scrolled to.
  useEffect(() => {
    if (!hasBeenVisible || collapsed) return;
    const { from, to } = timeRangeToNs(range);
    api.serviceClusters(service, from, to)
      .then(r => {
        const names = (r?.clusters ?? []).map(c => c.cluster);
        setClusters(names);
        // If the previously-picked cluster vanished from the
        // window (e.g. window moved past its traffic), drop
        // back to "All" instead of querying for nothing.
        if (picked && !names.includes(picked)) setPicked('');
      })
      .catch(() => setClusters([]));
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [service, range, hasBeenVisible, collapsed]);

  useEffect(() => {
    if (collapsed || !hasBeenVisible) return;
    setData(undefined);
    const { from, to } = timeRangeToNs(range);
    const f: { key: string; op: string; value: string }[] = [
      { key: 'service.name', op: '=', value: service },
    ];
    if (picked) {
      // Hit the resource-attr key directly. The OTLP ingest
      // path materialises k8s.cluster.name as a span attr,
      // so a single predicate is enough (no coalesce across
      // resource + span attrs needed at query time).
      f.push({ key: 'k8s.cluster.name', op: '=', value: picked });
    }
    api.spanHeatmap({ from, to, filters: JSON.stringify(f), buckets: 60 })
      .then(r => setData(r ?? null))
      .catch(() => setData(null));
  }, [service, range, collapsed, picked, hasBeenVisible]);

  const toggle = () => {
    const next = !collapsed;
    setCollapsed(next);
    try { localStorage.setItem('svc.heatmap.collapsed', next ? '1' : '0'); }
    catch { /* private browsing — best-effort only */ }
  };

  return (
    <div ref={containerRef} style={{ marginTop: 24, marginBottom: 14 }}>
      <div style={{
        display: 'flex', alignItems: 'baseline', gap: 8, marginBottom: 6,
      }}>
        <button type="button" onClick={toggle}
          style={{
            all: 'unset', cursor: 'pointer',
            fontSize: 11, fontWeight: 700, color: 'var(--text2)',
            textTransform: 'uppercase', letterSpacing: 0.4,
            display: 'inline-flex', alignItems: 'center', gap: 6,
          }}
          title={collapsed ? 'Expand' : 'Collapse'}>
          <span style={{ color: 'var(--text3)' }}>{collapsed ? '▸' : '▾'}</span>
          Latency distribution
        </button>
        {!collapsed && clusters.length >= 2 && (
          <select value={picked}
            onChange={e => setPicked(e.target.value)}
            title="Same service runs across multiple clusters — pivot the heatmap to any single cluster, or stay on the union view."
            style={{ fontSize: 11, padding: '2px 6px', marginLeft: 4 }}>
            <option value="">All clusters ({clusters.length})</option>
            {clusters.map(c => <option key={c} value={c}>{c}</option>)}
          </select>
        )}
        {!collapsed && data && data.maxCount > 0 && (
          <span style={{ fontSize: 11, color: 'var(--text3)' }}>
            peak {data.maxCount.toLocaleString()} spans/cell · log-scale y-axis
          </span>
        )}
      </div>
      {!collapsed && (
        <div style={{
          padding: 10, borderRadius: 6,
          background: 'var(--bg2)', border: '1px solid var(--border)',
        }}>
          {data === undefined && <Spinner />}
          {data === null && (
            <div style={{ fontSize: 12, color: 'var(--err)' }}>
              Failed to load latency distribution.
            </div>
          )}
          {data && (data.maxCount === 0 ? (
            <div style={{ fontSize: 12, color: 'var(--text3)' }}>
              No spans in this window.
            </div>
          ) : (
            <LatencyHeatmap data={data} height={240} />
          ))}
        </div>
      )}
    </div>
  );
}

function ClusterTh({ col, label, sort, dir, onSort, align }: {
  col: ClusterSortKey; label: string;
  sort: ClusterSortKey; dir: 'asc' | 'desc';
  onSort: (c: ClusterSortKey) => void;
  align?: 'left' | 'right';
}) {
  const active = sort === col;
  return (
    <th className={`sortable${active ? ' sorted' : ''}`}
        style={{ textAlign: align ?? 'left' }}
        aria-sort={active ? (dir === 'desc' ? 'descending' : 'ascending') : 'none'}>
      <button type="button" onClick={() => onSort(col)}
        style={{
          all: 'unset', display: 'inline-flex', alignItems: 'baseline',
          gap: 4, width: '100%', cursor: 'pointer',
          justifyContent: align === 'right' ? 'flex-end' : 'flex-start',
        }}>
        {label}
        <span className="sort-arrow">{active ? (dir === 'desc' ? '▼' : '▲') : '↕'}</span>
      </button>
    </th>
  );
}
